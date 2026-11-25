// Package queue shares a machine between everything errand runs on it ( ADR 0014, 0018; the guide's "Sharing a
// machine" ).
//
// A timing taken while something else was running is not a timing, so a run takes a claim on the machine it runs on,
// and the claims of every errand on that machine -- two terminals, two projects, the same user -- are counted
// together. That means state outside any process, and the smallest honest form of it is a directory:
//
//	<queue>/claims/<id>.json     one file per claim held
//	<queue>/waiting/<id>.json    a request that waits, or a claim that waits to grow ( `<id>.grow.json` )
//	<queue>/evict/<id>           somebody needs what this claim only keeps warm
//	<queue>/lock                 held only while deciding, never while working
//
// The directory is per user and per host ( a home shared over NFS must not merge two machines ). Requests are served
// first come, first served: whatever arrives after a request that waits, waits behind it -- a steady stream of small
// work never starves a benchmark. A claim that grows is ahead of any new request.
//
// A claim lives as long as its holder. Who holds one is known by its process ( its id and when it started, where the
// two can be seen from here ) and by a heartbeat on its file: a holder that was killed gives the machine back at
// once, one that is only slow ( stopped, swapped out ) keeps it.
//
// When somebody else owns the machine -- inside a Slurm, PBS, OAR or LSF allocation --, errand does not queue: the
// scheduler has decided what this process may have. And a claim is inherited: `ERRAND_CLAIM` says the machine was
// granted to this process tree already, so nothing in it queues again ( a run that starts another errand would wait
// for a claim its own parent holds ).
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/system"
)

const (
	Heartbeat  = 5 * time.Second  // between two touches of a claim
	StaleAfter = 30 * time.Second // a claim older than this, whose holder cannot be seen, has nobody breathing for it
	poll       = 250 * time.Millisecond

	EnvClaim      = "ERRAND_CLAIM"      // the cards granted to this process tree: nothing in it queues again
	EnvClaimFile  = "ERRAND_CLAIM_FILE" // where that claim is: what `require` grows
	EnvAllocation = "ERRAND_ALLOCATION" // an allocation errand holds: the machine is the allocation, carved by this queue
	EnvNoQueue    = "ERRAND_NO_QUEUE"
)

// ErrRerun: a claim that grows waits for what a run holds that waits for what this one holds. The younger of the two
// is stopped, to be run again with what it requires up front ( exit 75 ).
var ErrRerun = errors.New("it waits for what is held by a run that waits for what this one holds: to be run again, with what it requires up front")

// ErrGone: the claim to grow is not there any more.
var ErrGone = errors.New("the claim is gone: there is nothing to grow")

// Share is memory on a card: a part of it ( Percent ), or a size ( MB ). The zero Share: the card, whole.
type Share struct {
	Percent float64
	MB      float64
}

func (s Share) IsZero() bool { return s.Percent == 0 && s.MB == 0 }

// On: what the share takes on a card of `size` MB; ok false: the card whole ( no share, or no size known )
func (s Share) On(size float64) (mb float64, ok bool) {
	switch {
	case s.IsZero() || size == 0:
		return 0, false
	case s.Percent != 0:
		return size * s.Percent / 100, true
	}
	return s.MB, true
}

func (s Share) String() string {
	if s.Percent != 0 {
		return strconv.FormatFloat(s.Percent, 'g', -1, 64) + "%"
	}
	return strconv.FormatFloat(s.MB, 'g', -1, 64)
}

// MarshalJSON: `"75%"`, a number of MB, or null
func (s Share) MarshalJSON() ([]byte, error) {
	switch {
	case s.IsZero():
		return []byte("null"), nil
	case s.Percent != 0:
		return json.Marshal(s.String())
	}
	return json.Marshal(s.MB)
}

func (s *Share) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	got, err := shareOf(v)
	*s = got
	return err
}

func shareOf(v any) (Share, error) {
	switch x := v.(type) {
	case nil:
		return Share{}, nil
	case float64:
		return Share{MB: x}, nil
	case int64:
		return Share{MB: float64(x)}, nil
	case string:
		return ParseShare(x)
	}
	return Share{}, fmt.Errorf("gpu_ram = %v: a size ( 40G ) or a share of the card ( 75%% )", v)
}

// ParseSize: `8G`, `512M`, `2048` ( MB ) -> MB
func ParseSize(text string) (float64, error) {
	t := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(text)), "B")
	scale := 1.0
	if t != "" {
		switch t[len(t)-1] {
		case 'K':
			scale, t = 1.0/1024, t[:len(t)-1]
		case 'M':
			t = t[:len(t)-1]
		case 'G':
			scale, t = 1024, t[:len(t)-1]
		case 'T':
			scale, t = 1024*1024, t[:len(t)-1]
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a size ( 8G, 512M, or a number of MB )", text)
	}
	return n * scale, nil
}

// ParseShare: `gpu_ram` -- a size ( `40G` ) or a share of the card ( `75%` )
func ParseShare(text string) (Share, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return Share{}, nil
	}
	if strings.HasSuffix(t, "%") {
		pct, err := strconv.ParseFloat(strings.TrimSpace(t[:len(t)-1]), 64)
		if err != nil || pct <= 0 || pct > 100 {
			return Share{}, fmt.Errorf("gpu_ram = %q: a share between 0 and 100 %%", text)
		}
		return Share{Percent: pct}, nil
	}
	mb, err := ParseSize(t)
	return Share{MB: mb}, err
}

// Needs is what a run takes of a machine. Zero: a slot, counted against nothing.
type Needs struct {
	CPUs   float64 `json:"cpus,omitempty"`
	RAM    float64 `json:"ram,omitempty"` // MB
	GPUs   int     `json:"gpus,omitempty"`
	GPURam Share   `json:"gpu_ram"` // on each card
}

// Normalized: memory on a card is a card; several ranks need what one needs, each
func (n Needs) Normalized(ranks int) Needs {
	if !n.GPURam.IsZero() && n.GPUs == 0 {
		n.GPUs = 1
	}
	if ranks > 1 {
		if n.CPUs < 1 {
			n.CPUs = 1
		}
		n.CPUs *= float64(ranks)
		n.RAM *= float64(ranks)
		n.GPUs *= ranks
	}
	return n
}

func (n Needs) IsZero() bool { return n.CPUs == 0 && n.RAM == 0 && n.GPUs == 0 && n.GPURam.IsZero() }

// Claim is what a file of the queue says: a claim held, or a request that waits.
type Claim struct {
	V     int     `json:"v"`
	Label string  `json:"label"`
	Host  string  `json:"host"`
	PID   int     `json:"pid"`
	Start string  `json:"start,omitempty"` // when that process started: a pid reused is another process
	NS    string  `json:"ns,omitempty"`    // where its pid means something ( a container has its own numbers )
	Since float64 `json:"since"`
	Needs
	Exclusive   bool      `json:"exclusive,omitempty"`
	Devices     []int     `json:"devices"`
	CardRAM     []float64 `json:"card_ram,omitempty"` // what it takes on each of its cards, when it shares them
	Preemptible bool      `json:"preemptible,omitempty"`

	// a claim that grew ( `require` ): what it was started with, what its code asked, what that comes to
	Base          *Needs `json:"base,omitempty"`
	Required      *Needs `json:"required,omitempty"`
	RequiredExtra *Needs `json:"required_extra,omitempty"`
	RequiredExcl  bool   `json:"required_exclusive,omitempty"`
	Wanted        *Needs `json:"wanted,omitempty"`
	Rerun         bool   `json:"rerun,omitempty"`
	Grows         bool   `json:"grows,omitempty"` // a request: a claim that waits to grow
	Of            string `json:"claim,omitempty"` // ... this one
	Want          *Needs `json:"want,omitempty"`  // ... to this
	Path          string `json:"-"`
}

// Capacity is what a machine has.
type Capacity struct {
	CPUs    float64   `json:"cpus"`
	RAM     float64   `json:"ram"` // MB
	GPUs    int       `json:"gpus"`
	CardRAM []float64 `json:"card_ram,omitempty"` // MB, per card; empty when nothing says: `gpu_ram` then takes cards whole
}

func (c Capacity) cardSize(d int) float64 {
	if d >= 0 && d < len(c.CardRAM) {
		return c.CardRAM[d]
	}
	return 0
}

// Queue is the queue of one machine, seen by one process.
type Queue struct {
	Dir  string
	Host string
	Cap  Capacity
	// what tests replace
	now   func() time.Time
	alive func(Claim) (bool, bool) // ( the holder is alive, that can be told from here )
}

// Open: this machine's queue, for this user
func Open() *Queue {
	return &Queue{Dir: Dir(), Host: Host(), Cap: Machine(), now: time.Now, alive: holderAlive}
}

// At: a queue in `dir`, of a machine that has `cap` ( a test's, or a machine described from elsewhere )
func At(dir string, cap Capacity) *Queue {
	return &Queue{Dir: dir, Host: Host(), Cap: cap, now: time.Now, alive: holderAlive}
}

func Host() string {
	h, _ := os.Hostname()
	if h = strings.SplitN(h, ".", 2)[0]; h == "" {
		return "localhost"
	}
	return h
}

// Dir: per user AND per host. `XDG_RUNTIME_DIR` when it is usable ( set is not the same as usable: a service, a
// container can inherit the variable without the directory ), the temporary directory otherwise. Two allocations of
// one user on one node are two machines: one queue each.
func Dir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if st, err := os.Stat(base); base == "" || err != nil || !st.IsDir() || !system.Writable(base) {
		base = os.TempDir()
	}
	alloc := ""
	if a := os.Getenv(EnvAllocation); a != "" {
		alloc = "-alloc" + a
	}
	return filepath.Join(base, fmt.Sprintf("errand-queue-%d-%s%s", os.Getuid(), Host(), alloc))
}

// Scheduler: the name of the scheduler that already owns this process, if any -- not in an allocation errand holds,
// which errand carves itself
func Scheduler() string {
	if os.Getenv(EnvAllocation) != "" {
		return ""
	}
	for _, v := range []string{"SLURM_JOB_ID", "PBS_JOBID", "OAR_JOB_ID", "LSB_JOBID", "SGE_TASK_ID", "FLUX_JOB_ID"} {
		if os.Getenv(v) != "" {
			return strings.ToLower(strings.SplitN(v, "_", 2)[0])
		}
	}
	return ""
}

// ── the files ────────────────────────────────────────────────────────────────

func (q *Queue) sub(name string) string {
	d := filepath.Join(q.Dir, name)
	_ = os.MkdirAll(d, 0o700)
	return d
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	part := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".part")
	if err := os.WriteFile(part, b, 0o600); err != nil {
		return err
	}
	return os.Rename(part, path)
}

// read: a file of the queue
func read(path string) (Claim, bool) {
	c := Claim{Path: path}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	c.Path = path
	return c, true
}

// live: whether the claim at `path` still has a holder. One whose process can be seen from here is as alive as that
// process; another, as its heartbeat.
func (q *Queue) live(c Claim, mtime time.Time) bool {
	if c.Host == q.Host {
		if alive, known := q.alive(c); known {
			return alive
		}
	}
	return q.now().Sub(mtime) <= StaleAfter
}

func (q *Queue) list(dir string) []Claim {
	var out []Claim
	listed, _ := os.ReadDir(q.sub(dir))
	for _, e := range listed {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(q.Dir, dir, name)
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		c, ok := read(path)
		if !ok {
			if q.now().Sub(st.ModTime()) > StaleAfter { // ( half written a moment ago is not garbage yet )
				_ = os.Remove(path)
			}
			continue
		}
		if !q.live(c, st.ModTime()) {
			// Nobody breathes for it: reclaimed. Its owner, if it ever comes back, finds it gone and is told so.
			_ = os.Remove(path)
			_ = os.Remove(filepath.Join(q.Dir, "evict", idOf(path)))
			continue
		}
		out = append(out, c)
	}
	return out
}

func idOf(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, ".json")
}

// Held: every live claim, those nobody holds any more cleared away as they are noticed
func (q *Queue) Held() []Claim {
	out := q.list("claims")
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Waiting: the requests that wait, the oldest first; claims that grow before them
func (q *Queue) Waiting() []Claim {
	out := q.list("waiting")
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Grows != b.Grows {
			return a.Grows
		}
		if a.Since != b.Since {
			return a.Since < b.Since
		}
		return a.Path < b.Path
	})
	return out
}

// lock: held only while deciding
func (q *Queue) lock() (func(), error) {
	_ = os.MkdirAll(q.Dir, 0o700)
	f, err := os.OpenFile(filepath.Join(q.Dir, "lock-"+q.Host), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := system.Lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// ── does it fit ──────────────────────────────────────────────────────────────

// leftOn: what is left of card `d`'s memory, in MB ( 0 when its size is unknown and nobody holds it ); whole false
// when somebody holds it whole
func (q *Queue) leftOn(d int, others []Claim) (left float64, free bool) {
	size := q.Cap.cardSize(d)
	left = size
	for _, o := range others {
		for i, held := range o.Devices {
			if held != d {
				continue
			}
			if i >= len(o.CardRAM) || size == 0 {
				return 0, false
			}
			left -= o.CardRAM[i]
		}
	}
	if left < 0 {
		left = 0
	}
	return left, true
}

func label(c Claim) string {
	if c.Label == "" {
		return "something"
	}
	return c.Label
}

func g(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// fits: why `needs` must wait ( "" when it fits ), and the cards it gets. `ahead`: the requests queued before this
// one, and the claims that grow. `keep`: the cards a growing claim already has, given first when they still fit.
func (q *Queue) fits(needs Needs, exclusive bool, others, ahead []Claim, keep []int) (string, []int) {
	if len(ahead) > 0 {
		first := ahead[0]
		why := "its turn"
		if first.Grows {
			why = "more than it holds"
		} else if first.Exclusive {
			why = "the machine to itself"
		}
		more := ""
		if len(ahead) > 1 {
			more = fmt.Sprintf(" ( %d ahead )", len(ahead))
		}
		return fmt.Sprintf("%s, which is waiting for %s%s", label(first), why, more), nil
	}
	if exclusive && len(others) > 0 {
		return fmt.Sprintf("the machine to itself (%d running)", len(others)), nil
	}
	for _, o := range others {
		if o.Exclusive {
			return label(o) + ", which has the machine to itself", nil
		}
	}
	for _, r := range []struct {
		name                string
		wanted, total, used float64
	}{
		{"cpus", needs.CPUs, q.Cap.CPUs, sum(others, func(c Claim) float64 { return c.CPUs })},
		{"ram", needs.RAM, q.Cap.RAM, sum(others, func(c Claim) float64 { return c.RAM })},
	} {
		if r.wanted == 0 || r.total == 0 {
			continue
		}
		if r.wanted > r.total {
			// Asking for more than exists would wait for ever: let through alone rather than held for an impossible
			// promise.
			if len(others) > 0 {
				return fmt.Sprintf("the machine to itself (needs %s=%s of %s)", r.name, g(r.wanted), g(r.total)), nil
			}
			continue
		}
		if r.used+r.wanted > r.total {
			return fmt.Sprintf("%s (%s of %s taken, needs %s)", r.name, g(r.used), g(r.total), g(r.wanted)), nil
		}
	}
	// Cards are ASSIGNED, not merely counted: a run that asked for one has to be told which, or two of them pick the
	// same card and the numbers mean nothing.
	all := make([]int, q.Cap.GPUs)
	for i := range all {
		all[i] = i
	}
	if exclusive {
		return "", all
	}
	if needs.GPUs == 0 || len(all) == 0 {
		return "", nil // no card asked, or none here to share: nothing to wait for
	}
	// A card's memory is counted per card: two runs that each take 30 % share one, two that take 75 % do not. Without
	// `gpu_ram`, or without the card's size, a card is taken whole.
	kept := map[int]bool{}
	for _, d := range keep {
		kept[d] = true
	}
	type card struct {
		d    int
		left float64
	}
	var free []card
	for _, d := range all {
		left, ok := q.leftOn(d, others)
		if !ok {
			continue
		}
		size := q.Cap.cardSize(d)
		want, shared := needs.GPURam.On(size)
		if shared && want > size {
			shared = false // more than the card has: the card, whole
		}
		if (!shared && left < size) || (shared && want > left) {
			continue
		}
		free = append(free, card{d, left})
	}
	// The cards given are those with the most left: two runs that share no card do not slow each other down.
	sort.SliceStable(free, func(i, j int) bool {
		a, b := free[i], free[j]
		if kept[a.d] != kept[b.d] {
			return kept[a.d]
		}
		if a.left != b.left {
			return a.left > b.left
		}
		return a.d < b.d
	})
	got := make([]int, 0, len(free))
	for _, c := range free {
		got = append(got, c.d)
	}
	if len(got) < needs.GPUs {
		if len(others) == 0 {
			return "", got // more than exists: run alone with what there is
		}
		how := ""
		if !needs.GPURam.IsZero() {
			how = ", " + needs.GPURam.String() + " of each"
		}
		return fmt.Sprintf("gpus (%d free of %d, needs %d%s)", len(got), len(all), needs.GPUs, how), nil
	}
	return "", got[:needs.GPUs]
}

func sum(cs []Claim, of func(Claim) float64) float64 {
	t := 0.0
	for _, c := range cs {
		t += of(c)
	}
	return t
}

// shares: what a claim writes of its cards' memory, when it shares them
func (q *Queue) shares(needs Needs, got []int, exclusive bool) []float64 {
	if exclusive || needs.GPURam.IsZero() || len(got) == 0 {
		return nil
	}
	out := make([]float64, len(got))
	for i, d := range got {
		mb, ok := needs.GPURam.On(q.Cap.cardSize(d))
		if !ok {
			return nil
		}
		out[i] = mb
	}
	return out
}

// ── a claim ──────────────────────────────────────────────────────────────────

// Request is what is asked of the machine.
type Request struct {
	Needs     Needs
	Exclusive bool
	Label     string
	Base      *Needs       // what the run is started with by what it is told, when Needs is more ( claimed up front )
	Say       func(string) // told what it waits for, each time that changes
	Waiting   func(string) // told what it waits for ( "" once it has arrived ), for the run's record ( ADR 0009 )
	// For a job started by an agent, which is not the process the job's surroundings are those of:
	Inherited *string // the cards a run this one is started inside was granted already ( its `ERRAND_CLAIM` )
	NoQueue   bool    // asked not to queue
}

// Granted is what the machine gave: where the claim is, and which cards are ours.
type Granted struct {
	q       *Queue
	Path    string // "" when nothing was claimed ( inherited, or somebody else schedules )
	Devices []int
	stop    chan struct{}
	done    chan struct{}
}

func (q *Queue) mine(c *Claim) {
	c.V, c.Host, c.PID = 1, q.Host, os.Getpid()
	c.Start, c.NS = processStart(c.PID), pidNamespace()
}

func (q *Queue) newID() string {
	return fmt.Sprintf("%s-%d-%d", q.Host, os.Getpid(), q.now().UnixNano())
}

func seconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func devicesOf(spelt string) []int {
	out := []int{}
	for _, d := range strings.Split(spelt, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(d)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// Acquire: wait until the request fits, then hold it until Release. Nothing is claimed -- and nothing waited for --
// when the machine was granted to this process tree already, when queueing is off, or when a scheduler decides.
func (q *Queue) Acquire(ctx context.Context, r Request) (*Granted, error) {
	if r.Inherited != nil {
		return &Granted{q: q, Devices: devicesOf(*r.Inherited)}, nil
	}
	if inherited, ok := os.LookupEnv(EnvClaim); ok {
		return &Granted{q: q, Devices: devicesOf(inherited)}, nil
	}
	if sched := Scheduler(); sched != "" {
		if r.Say != nil {
			r.Say(fmt.Sprintf("  (%s already decided what this process may have)", sched))
		}
		all := make([]int, q.Cap.GPUs)
		for i := range all {
			all[i] = i
		}
		return &Granted{q: q, Devices: all}, nil
	}
	if r.NoQueue || os.Getenv(EnvNoQueue) != "" {
		return &Granted{q: q, Devices: []int{}}, nil
	}
	id := q.newID()
	mine := filepath.Join(q.sub("claims"), id+".json")
	queued := filepath.Join(q.sub("waiting"), id+".json")
	since := seconds(q.now())
	waitedFor := ""
	asked := map[string]bool{} // the claims kept warm this request asked to give way
	defer func() {
		_ = os.Remove(queued)
		if waitedFor != "" && r.Waiting != nil {
			r.Waiting("")
		}
	}()
	for {
		unlock, err := q.lock()
		if err != nil {
			return nil, err
		}
		others := q.Held()
		var ahead []Claim
		for _, w := range q.Waiting() {
			if w.Path != queued && (w.Grows || w.Since < since || (w.Since == since && w.Path < queued)) {
				ahead = append(ahead, w)
			}
		}
		blocked, got := q.fits(r.Needs, r.Exclusive, others, ahead, nil)
		if blocked != "" {
			// What is only kept warm gives way: ask it, and wait for it to go.
			var keen, gives []Claim
			for _, o := range others {
				if o.Preemptible {
					gives = append(gives, o)
				} else {
					keen = append(keen, o)
				}
			}
			if why, _ := q.fits(r.Needs, r.Exclusive, keen, ahead, nil); len(gives) > 0 && why == "" {
				names := make([]string, len(gives))
				for i, o := range gives {
					q.evict(o.Path)
					asked[o.Path] = true
					names[i] = label(o)
				}
				blocked = strings.Join(names, ", ") + ", kept warm, to make room"
			}
		}
		if blocked == "" {
			c := Claim{Label: r.Label, Since: seconds(q.now()), Needs: r.Needs, Exclusive: r.Exclusive, Devices: got,
				CardRAM: q.shares(r.Needs, got, r.Exclusive), Base: r.Base}
			if c.Devices == nil {
				c.Devices = []int{}
			}
			q.mine(&c)
			err := writeJSON(mine, c)
			unlock()
			if err != nil {
				return nil, err
			}
			granted := &Granted{q: q, Path: mine, Devices: c.Devices, stop: make(chan struct{}), done: make(chan struct{})}
			go granted.beat()
			return granted, nil
		}
		// Every waiter is recorded, the oldest first: a later request that fits does not pass it.
		now := q.now()
		if os.Chtimes(queued, now, now) != nil {
			w := Claim{Label: r.Label, Since: since, Needs: r.Needs, Exclusive: r.Exclusive, Devices: []int{}}
			q.mine(&w)
			_ = writeJSON(queued, w)
		}
		unlock()
		if blocked != waitedFor {
			if r.Say != nil {
				r.Say("  waiting for " + blocked)
			}
			if r.Waiting != nil {
				r.Waiting(fmt.Sprintf("room on %s: %s", q.Host, blocked))
			}
			waitedFor = blocked
		}
		select {
		case <-ctx.Done():
			// It gives up: what it asked to give way is not asked any more. ( Another waiter that needs the same
			// asks again at its next look. ) Left there, a service kept warm would be stopped for nobody.
			for claim := range asked {
				_ = os.Remove(filepath.Join(q.Dir, "evict", idOf(claim)))
			}
			return nil, fmt.Errorf("stopped waiting for %s: %w", blocked, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// Try: a claim taken only if it fits now ( what is kept warm ahead of use ) -- nil when it does not, or when nothing
// is to be claimed
func (q *Queue) Try(r Request, preemptible bool) *Granted {
	if _, ok := os.LookupEnv(EnvClaim); ok || os.Getenv(EnvNoQueue) != "" || Scheduler() != "" {
		return nil
	}
	unlock, err := q.lock()
	if err != nil {
		return nil
	}
	defer unlock()
	others := q.Held()
	if why, _ := q.fits(r.Needs, false, others, q.Waiting(), nil); why != "" {
		return nil
	}
	_, got := q.fits(r.Needs, false, others, nil, nil)
	if got == nil {
		got = []int{}
	}
	c := Claim{Label: r.Label, Since: seconds(q.now()), Needs: r.Needs, Devices: got, Preemptible: preemptible,
		CardRAM: q.shares(r.Needs, got, false)}
	q.mine(&c)
	mine := filepath.Join(q.sub("claims"), q.newID()+".json")
	if writeJSON(mine, c) != nil {
		return nil
	}
	granted := &Granted{q: q, Path: mine, Devices: got, stop: make(chan struct{}), done: make(chan struct{})}
	go granted.beat()
	return granted
}

func (g *Granted) beat() {
	defer close(g.done)
	t := time.NewTicker(Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-g.stop:
			return
		case now := <-t.C:
			if os.Chtimes(g.Path, now, now) != nil {
				return // reclaimed while we were away: nothing to keep alive
			}
		}
	}
}

// Release: the machine given back
func (g *Granted) Release() {
	if g == nil || g.Path == "" {
		return
	}
	if g.stop != nil {
		close(g.stop)
		<-g.done
		g.stop = nil
	}
	_ = os.Remove(g.Path)
	_ = os.Remove(filepath.Join(g.q.Dir, "evict", idOf(g.Path)))
}

// Lost: the claim was reclaimed meanwhile
func (g *Granted) Lost() bool {
	if g.Path == "" {
		return false
	}
	_, err := os.Stat(g.Path)
	return err != nil
}

// HandedDown: what everything this run starts must be told -- the cards, and where the claim is, for `require` to
// grow it
func (g *Granted) HandedDown() map[string]string {
	out := map[string]string{EnvClaim: joinInts(g.Devices)}
	if g.Path != "" {
		out[EnvClaimFile] = g.Path
	}
	return out
}

// Env: what a child has to be told so that it uses the cards it was given. Every framework reads one of these, and
// each renumbers from zero for whoever it is set on.
func (g *Granted) Env() map[string]string {
	if len(g.Devices) == 0 {
		return map[string]string{}
	}
	listed := joinInts(g.Devices)
	return map[string]string{"CUDA_VISIBLE_DEVICES": listed, "HIP_VISIBLE_DEVICES": listed, "ROCR_VISIBLE_DEVICES": listed}
}

func joinInts(l []int) string {
	parts := make([]string, len(l))
	for i, d := range l {
		parts[i] = strconv.Itoa(d)
	}
	return strings.Join(parts, ",")
}

// Row: the claim as it is now -- what `require` made of it
func (g *Granted) Row() (Claim, bool) {
	if g.Path == "" {
		return Claim{}, false
	}
	return read(g.Path)
}

// ── kept warm, and giving way ────────────────────────────────────────────────
//
// A claim marked preemptible holds what is only kept warm. A request that does not fit, but would without such
// claims, leaves an eviction request beside each of them and waits; their holders give the room back as soon as they
// are idle.

func (q *Queue) evict(claim string) {
	f, err := os.OpenFile(filepath.Join(q.sub("evict"), idOf(claim)), os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		f.Close()
	}
}

// Evicted: somebody needs what this holds, and it said it could give it up
func (g *Granted) Evicted() bool {
	if g.Path == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(g.q.Dir, "evict", idOf(g.Path)))
	return err == nil
}

// SetPreemptible: say, in the claim itself, whether it may be asked to give way
func (g *Granted) SetPreemptible(flag bool) {
	if g.Path == "" {
		return
	}
	unlock, err := g.q.lock()
	if err != nil {
		return
	}
	defer unlock()
	if c, ok := read(g.Path); ok && c.Preemptible != flag {
		c.Preemptible = flag
		_ = writeJSON(g.Path, c)
	}
	if !flag {
		_ = os.Remove(filepath.Join(g.q.Dir, "evict", idOf(g.Path)))
	}
}

// Resize: what a claim holds, said again ( what is kept warm grows with what it loads )
func (g *Granted) Resize(needs Needs) {
	if g.Path == "" {
		return
	}
	unlock, err := g.q.lock()
	if err != nil {
		return
	}
	defer unlock()
	if c, ok := read(g.Path); ok {
		c.Needs = needs
		_ = writeJSON(g.Path, c)
	}
}

// ── looking at it ────────────────────────────────────────────────────────────

// Idle: nothing holds the machine and nothing is queued for it
func (q *Queue) Idle() bool { return len(q.Held()) == 0 && len(q.Waiting()) == 0 }

// WaitIdle: return once the machine is idle
func (q *Queue) WaitIdle(ctx context.Context) error {
	for !q.Idle() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}

// Describe: one line per request that waits, then per claim held
func (q *Queue) Describe() []string {
	var out []string
	for _, w := range q.Waiting() {
		why := "waits for its turn"
		if w.Grows {
			why = "waits to grow its claim"
		} else if w.Exclusive {
			why = "waits for the whole machine"
		}
		out = append(out, fmt.Sprintf("%s  %s  pid %d", label(w), why, w.PID))
	}
	for _, c := range q.Held() {
		what := "the whole machine"
		if !c.Exclusive {
			var parts []string
			if c.CPUs != 0 {
				parts = append(parts, "cpus="+g(c.CPUs))
			}
			if c.RAM != 0 {
				parts = append(parts, "ram="+g(c.RAM))
			}
			if c.GPUs != 0 {
				parts = append(parts, "gpus="+strconv.Itoa(c.GPUs))
			}
			if what = strings.Join(parts, ", "); what == "" {
				what = "a slot"
			}
			if !c.GPURam.IsZero() {
				what += ", gpu_ram=" + c.GPURam.String()
			}
		}
		if len(c.Devices) > 0 {
			what += "  gpu " + joinInts(c.Devices)
		}
		warm := ""
		if c.Preemptible {
			warm = "  ( kept warm: gives way )"
		}
		out = append(out, fmt.Sprintf("%s  %s  %.0fs  pid %d%s", label(c), what, seconds(q.now())-c.Since, c.PID, warm))
	}
	return out
}
