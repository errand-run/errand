// A run handed to a batch system ( ADR 0035; the guide's "Slurm" ).
//
// One path, whether the command waits for the run or not: an **allocation**, asked with `sbatch`, whose step is the
// agent of the node it lands on ( `errand place --alloc …` ). That agent holds the runs it is handed as any agent
// holds its jobs, and stays for a while after the last one -- a minute unless the layer says ( `keep_alive` ) --, so
// that the command that follows runs in it at once: no queue, no new job. Then it goes, and the allocation with it.
//
//	<cache>/alloc/<key>/            an allocation, on the disk the login node and the nodes share
//	    alloc.json                  the batch system's number for it, what it was sized for, how long it is kept
//	    handed.jsonl                what it is handed, a line each: a run, or a run to stop
//	    runs/<run>.jsonl            what its agent says of that run, a line each: taken, what it waits for, how it ended
//
// **An allocation is named after what declares it** ( the layers up to `Slurm` ), not after the environment: two
// environments that differ after it share it. A command runs in it only if it fits -- its cores and cards within
// those the allocation was sized for, exclusive only if the allocation is, its time within what the job has left;
// otherwise it gets one of its own, the first given back before if nothing runs in it.
//
// **No link is kept between the login node and the node.** A run is handed over as a line of a file the node's
// agent opens again every second -- a file with a name it knows: over NFS, opening a file asks the server how it is
// now, where a directory's listing may be a memory half a minute old ( seen on cinaps: a run handed over as a new
// file was taken after 28 s ). It is followed by reading what it writes on the shared disk; how the job is, is asked
// of the batch system. So the agent that submitted may stop, start again, be of another version: nothing
// of a run is held in it.
//
// **Nothing is ever looked for before it exists.** Asked for a file that is not there, a machine remembers "not there"
// for up to a minute after another machine made it ( seen on cinaps, twice: a run that had passed said lost; a run in
// an allocation that was there taking 13 s ). So whoever will read a file makes it, empty, before whoever will write
// it is told: the run's output files, and the file its agent says its state in.
//
// Inside an allocation, errand's queue carves it ( `ERRAND_ALLOCATION` ): a run that says what it needs takes that
// much, and another runs beside it; one that says nothing takes it whole.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

func schedulerAt(stack []project.Layer) int {
	for i, l := range stack {
		if layer.IsScheduler(l) {
			return i
		}
	}
	return -1
}

// batchJob is a run handed to a batch system, as the agent that submitted it knows it.
type batchJob struct {
	a       *Place
	meta    jobMeta
	dir     string // its record, with the agent that submitted it
	mu      sync.Mutex
	state   string // waiting | running | ended
	more    string // what it waits for; the node it runs on
	code    *int
	changed chan struct{}
	spec    runinfo.LaunchSpec // what it was asked as, for what is made again when it asks for what it does not have ( rerun.go )
	known   bool               // ... and the spec is known: it was kept with the record, which an agent that adopts the run reads
	next    []runinfo.Launched // the runs made after it
	where   stepReader         // where it says it is
	noAgain bool               // it ended without saying how: nothing is made again from what it may have asked
}

const specName = "spec.json"

func (b *batchJob) wake() { // ( under b.mu )
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *batchJob) said() (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.code != nil {
		if r, ok := runinfo.ReadRecord(filepath.Join(b.meta.Leaf, runinfo.ResultName)); ok {
			return "ended", r.Status
		}
		return "ended", fmt.Sprintf("exit %d", *b.code)
	}
	return b.state, b.more
}

// allocation is what is known of an allocation, where the login node and the nodes both read it.
type allocation struct {
	V         int     `json:"v"`
	Slurm     string  `json:"slurm"`
	CPUs      float64 `json:"cpus"` // what it was sized for; 0: whatever the batch system gives by default
	GPUs      int     `json:"gpus"`
	RAM       float64 `json:"ram"`
	Exclusive bool    `json:"exclusive"`
	Tasks     int     `json:"tasks"` // how many ranks it has room for
	Keep      float64 `json:"keep"`  // seconds it stays with nothing running in it
	Made      float64 `json:"made"`
}

func allocsDir() string { return filepath.Join(kit.LocalCache(), "allocations") }

func readAllocation(dir string) (allocation, bool) {
	var al allocation
	data, err := os.ReadFile(filepath.Join(dir, "alloc.json"))
	return al, err == nil && json.Unmarshal(data, &al) == nil && al.Slurm != ""
}

// keepOf: how long an allocation stays with nothing running in it -- a minute unless the layer says: seconds, or
// `30m`, `2h`
func keepOf(l project.Layer) float64 {
	switch v := l["keep_alive"].(type) {
	case float64:
		return v
	case string:
		unit := map[byte]float64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}[v[len(v)-1]]
		if unit == 0 {
			unit, v = 1, v+"s"
		}
		if n, err := strconv.ParseFloat(v[:len(v)-1], 64); err == nil {
			return n * unit
		}
	}
	return 60
}

// handed is one line of what an allocation is handed.
type handed struct {
	Run  string              `json:"run,omitempty"`
	Spec *runinfo.LaunchSpec `json:"spec,omitempty"`
	Stop string              `json:"stop,omitempty"`
}

func handedFile(dir string) string { return filepath.Join(dir, "handed.jsonl") }

// readHanded: what an allocation was handed, from `from` on -- whole lines only -> them, and where they end
func readHanded(dir string, from int64) ([]handed, int64) {
	f, err := os.Open(handedFile(dir)) // ( opened anew each time: that is what asks a shared disk how the file is now )
	if err != nil {
		return nil, from
	}
	defer f.Close()
	data := make([]byte, 0, 4096)
	buf := make([]byte, 1<<16)
	for at := from; ; {
		n, err := f.ReadAt(buf, at)
		data = append(data, buf[:n]...)
		at += int64(n)
		if err != nil || n == 0 {
			break
		}
	}
	var out []handed
	for {
		i := strings.IndexByte(string(data), '\n')
		if i < 0 {
			break
		}
		var h handed
		if json.Unmarshal(data[:i], &h) == nil {
			out = append(out, h)
		}
		data = data[i+1:]
		from += int64(i + 1)
	}
	return out, from
}

func runFile(dir, id string) string { return filepath.Join(dir, "runs", id+".jsonl") }

// runSaid is what an allocation's agent said of a run, so far.
type runSaid struct {
	taken   bool
	waiting string
	done    *int
}

// runState: what the allocation's agent said of a run -- read from a file made before that agent was told of the run
func runState(dir, id string) runSaid {
	var out runSaid
	f, err := os.Open(runFile(dir, id))
	if err != nil {
		return out
	}
	defer f.Close()
	data := make([]byte, 0, 1024)
	buf := make([]byte, 1<<14)
	for {
		n, err := f.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil || n == 0 {
			break
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		var l map[string]any
		if json.Unmarshal([]byte(line), &l) != nil {
			continue
		}
		if _, ok := l["taken"]; ok {
			out.taken = true
		}
		if w, ok := l["waiting"].(string); ok {
			out.waiting = w
		}
		if c, ok := l["done"]; ok {
			code := int(kit.Num(c, 1))
			out.done = &code
		}
	}
	return out
}

// activeIn: the runs an allocation was handed that have not ended -- nor were told to stop before they started
func activeIn(dir string) []string {
	all, _ := readHanded(dir, 0)
	stopped := map[string]bool{}
	for _, h := range all {
		if h.Stop != "" {
			stopped[h.Stop] = true
		}
	}
	var out []string
	for _, h := range all {
		if h.Run == "" {
			continue
		}
		st := runState(dir, h.Run)
		if st.done != nil || (stopped[h.Run] && !st.taken) {
			continue
		}
		out = append(out, h.Run)
	}
	return out
}

// poke: the agent of an allocation told to look at what it was handed
func poke(number string) { _ = exec.Command("scancel", "--signal=USR1", "--batch", number).Run() }

// submit: the run handed to an allocation -- one that is there and that it fits in, else one asked for it
func (a *Place) submit(s runinfo.LaunchSpec, at int, id, leaf, label, root, cwd string) error {
	slurm := s.Stack[at]
	step := s // what the allocation's agent is told: the rest of the stack, the run's id and its directory
	step.Stack, step.ID, step.Leaf, step.Environ = s.Stack[at+1:], id, leaf, nil
	step.Batch = true // nobody types to it from a login node
	step.Tty = nil    // and there is no link to its node to carry a terminal over: pipes
	step.MPI = slurm.String("mpi")
	if err := layer.Check(step.Stack); err != nil {
		return fmt.Errorf("the environment %s: %w", s.Env, err)
	}
	keep := keepOf(slurm)
	b, _ := json.Marshal(s.Stack[:at+1])
	sum := sha256.Sum256(b)
	key := hex.EncodeToString(sum[:])[:12]
	if keep <= 0 {
		key += "-" + id[:8] // one for this command alone, given back when it ends
	}
	dir := filepath.Join(allocsDir(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// one decision at a time about an allocation: the lock is in its directory, which another login node sees too
	lock := filepath.Join(dir, "lock")
	for i := 0; os.Mkdir(lock, 0o755) != nil; i++ {
		if st, err := os.Stat(lock); (err == nil && time.Since(st.ModTime()) > time.Minute) || i > 600 {
			_ = os.Remove(lock)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer os.Remove(lock)

	// what the allocation is sized for: what the layer declares ( `allocation = { … }` over its own fields ), filled
	// in from what this command asks for
	sized := project.Layer{}
	for k, v := range slurm {
		sized[k] = v
	}
	if over, ok := slurm["allocation"].(map[string]any); ok {
		for k, v := range over {
			sized[k] = v
		}
	}
	want := allocation{V: 1, Keep: keep, Made: kit.Seconds(time.Now()), CPUs: s.Needs.CPUs, GPUs: s.Needs.GPUs, RAM: s.Needs.RAM, Exclusive: s.Exclusive}
	if v, ok := sized["cpus"].(float64); ok {
		want.CPUs = v
	}
	if v, ok := sized["gpus"].(float64); ok {
		want.GPUs = int(v)
	}
	want.Tasks = max(1, s.Ranks)
	if nodes, ok := sized["nodes"].(float64); ok { // it outlives the command that asked for it: sized from what is declared
		if per, ok := sized["ntasks_per_node"].(float64); ok {
			want.Tasks = max(want.Tasks, int(nodes*per))
		}
	}
	al, there := readAllocation(dir)
	state := ""
	if there {
		var left string
		state, _, _, left = slurmSays(al.Slurm)
		fits := state != "" && state != "?" &&
			(al.CPUs == 0 || s.Needs.CPUs <= al.CPUs) && s.Needs.GPUs <= al.GPUs &&
			(!s.Exclusive || al.Exclusive) && (al.RAM == 0 || s.Needs.RAM <= al.RAM) && max(1, s.Ranks) <= max(1, al.Tasks) &&
			(s.Time == "" || left == "" || layer.SlurmSeconds(s.Time) <= layer.SlurmSeconds(left))
		if state != "" && !fits {
			if len(activeIn(dir)) == 0 { // nothing runs in it: given back first, it may hold the very nodes
				_ = exec.Command("scancel", al.Slurm).Run()
			}
			// an allocation of its own, sized for it
			dir = filepath.Join(allocsDir(), key+"-"+id[:8])
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			there = false
		} else if state == "" {
			there = false // the batch system knows it no more: what is left of it is of before
			_ = os.Remove(handedFile(dir))
			_ = os.RemoveAll(filepath.Join(dir, "runs"))
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return err
	}
	if !there {
		environ := s.Environ
		if environ == nil {
			environ = os.Environ()
		}
		flags := layer.SlurmFlags(sized, want.CPUs, want.RAM, want.GPUs, want.Exclusive, s.Time, want.Tasks)
		line := "exec " + layer.Quote(kit.SelfPath()) + " place --alloc " + layer.Quote(dir)
		argv := append(append([]string{"sbatch", "--parsable"}, flags...),
			"--job-name", "errand-"+filepath.Base(dir), "--output", dir+".out", "--chdir", root, "--wrap", line)
		cmd, err := layer.Wrap(s.Stack[:at], layer.Command{Argv: argv}, layer.Context{Root: root, Tags: s.Tags, Environ: environ})
		if err != nil {
			return fmt.Errorf("the environment %s: %w", s.Env, err)
		}
		path, err := kit.LookIn(cmd.Argv[0], environ, root)
		if err != nil {
			return fmt.Errorf("the environment %s hands its work to Slurm, and there is no %s here", s.Env, cmd.Argv[0])
		}
		proc := &exec.Cmd{Path: path, Args: cmd.Argv, Env: environ, Dir: root}
		for k, v := range cmd.Env {
			proc.Env = append(proc.Env, k+"="+v)
		}
		var said strings.Builder
		proc.Stderr = &said
		out, err := proc.Output()
		number := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), ";", 2)[0])
		if err != nil || number == "" {
			return fmt.Errorf("the run could not be submitted: %s", strings.TrimSpace(said.String()+" "+string(out)))
		}
		al = want
		al.Slurm = number
		if err := kit.WriteRecord(filepath.Join(dir, "alloc.json"), al); err != nil {
			return err
		}
	}
	// made before the node's agent is told: what it will write is never looked for before it exists
	step.Waits = runFile(dir, id)
	for _, made := range []string{runFile(dir, id), filepath.Join(leaf, runinfo.StreamNames["out"]), filepath.Join(leaf, runinfo.StreamNames["err"]), filepath.Join(leaf, runinfo.StreamNames["both"])} {
		if f, err := os.OpenFile(made, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err == nil {
			f.Close()
		}
	}
	if err := kit.AppendLine(handedFile(dir), handed{Run: id, Spec: &step}); err != nil { // ( one writer at a time: the lock above )
		return err
	}
	poke(al.Slurm)
	slurmKept.Lock()
	slurmKept.at = time.Time{} // ( what was known of the jobs is of before this one was handed over )
	slurmKept.Unlock()
	record := filepath.Join(a.Dir, "jobs", id)
	if err := os.MkdirAll(record, 0o755); err != nil {
		return err
	}
	first := "slurm job " + al.Slurm + ": submitted"
	if there {
		first = "" // an allocation that is there: nothing to say of it yet
	}
	bj := &batchJob{a: a, dir: record, state: "waiting", more: first, changed: make(chan struct{}), spec: s, known: true,
		meta: jobMeta{V: 1, ID: id, Leaf: leaf, What: label, Env: s.Env, Project: root, Started: s.When, Agent: os.Getpid(), Slurm: al.Slurm, Alloc: dir}}
	_ = kit.WriteRecord(filepath.Join(record, "meta.json"), bj.meta)
	_ = kit.WriteRecord(filepath.Join(record, specName), s)
	a.mu.Lock()
	a.batch[id] = bj
	a.mu.Unlock()
	go bj.watch()
	a.touched()
	return nil
}

// adopt: a run handed to a batch system by an agent that is gone -- of before, of another version --, known again
// from its record: nothing of it was held in that agent
func (a *Place) adopt(record string, m jobMeta) *batchJob {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b := a.batch[m.ID]; b != nil {
		return b
	}
	b := &batchJob{a: a, dir: record, meta: m, state: "waiting", more: "slurm job " + m.Slurm, changed: make(chan struct{})}
	if data, err := os.ReadFile(filepath.Join(record, specName)); err == nil && json.Unmarshal(data, &b.spec) == nil {
		b.known = true
	}
	a.batch[m.ID] = b
	go b.watch()
	return b
}

// What the batch system says of this user's jobs, asked once for all of them and kept two seconds: an agent that
// follows many runs asks no more often than one that follows one.
var slurmKept struct {
	sync.Mutex
	at   time.Time
	ok   bool
	jobs map[string][4]string
}

// slurmSays: how the batch system says the job is -- its state, why it waits, where it runs, the time it has left.
// "" once it knows it no more; "?" when it could not be asked ( which says nothing of the job )
func slurmSays(number string) (state, reason, node, left string) {
	slurmKept.Lock()
	defer slurmKept.Unlock()
	if time.Since(slurmKept.at) >= 2*time.Second {
		args := []string{"-h", "-o", "%i|%T|%r|%N|%L"}
		if name := os.Getenv("USER"); name != "" {
			args = append(args, "-u", name)
		}
		out, err := exec.Command("squeue", args...).Output()
		slurmKept.at, slurmKept.ok, slurmKept.jobs = time.Now(), err == nil, map[string][4]string{}
		for _, line := range strings.Split(string(out), "\n") {
			parts := strings.Split(strings.TrimSpace(line), "|")
			if len(parts) < 4 || parts[0] == "" {
				continue
			}
			var row [4]string
			copy(row[:], parts[1:])
			if !strings.ContainsAny(row[3], "0123456789") {
				row[3] = ""
			}
			slurmKept.jobs[strings.SplitN(parts[0], "_", 2)[0]] = row
		}
	}
	if !slurmKept.ok {
		return "?", "", "", ""
	}
	row := slurmKept.jobs[number]
	return row[0], row[1], row[2], row[3]
}

// watch: the job followed to its end, by what the batch system says and what the run wrote
func (b *batchJob) watch() {
	leaf := b.meta.Leaf
	gone := 0
	since, asked := time.Now(), time.Time{}
	var state, reason, node string
	for {
		said := runState(b.meta.Alloc, b.meta.ID)
		if said.done != nil { // its agent said it ended: its result is there, written before that was said
			code := *said.done
			for i := 0; i < 20; i++ {
				if r, ok := runinfo.ReadRecord(filepath.Join(leaf, runinfo.ResultName)); ok {
					if r.Exit != nil {
						code = *r.Exit
					}
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			b.end(code)
			return
		}
		if time.Since(asked) >= 2*time.Second { // the batch system is asked now and then; the run's files are looked at more often
			state, reason, node, _ = slurmSays(b.meta.Slurm)
			asked = time.Now()
		}
		now, more := "waiting", "slurm job "+b.meta.Slurm+": "+strings.ToLower(state)
		switch {
		case state == "?": // the batch system could not be asked: that says nothing of the run
			time.Sleep(time.Second)
			continue
		case state == "":
			// The batch system knows it no more, and the run's result is not seen. On a disk shared over a network
			// that proves little: this machine asked for that file while it was not there, and may be told "not
			// there" from memory for up to a minute after the node wrote it ( seen on cinaps: a run that passed was
			// said lost after 5 s ). So the directory is looked at afresh, and waited for past that minute, before
			// anything is said for the run.
			freshLook(leaf)
			if r, ok := runinfo.ReadRecord(filepath.Join(leaf, runinfo.ResultName)); ok { // the run did say how it ended: its word stands
				code := 1
				if r.Exit != nil {
					code = *r.Exit
				}
				b.end(code)
				return
			}
			patience := 5 // seconds: on a disk of this machine alone, what is not there is not there
			if m, _, ok := caches.MountOf(leaf); !ok || m.Network {
				patience = 70
			}
			if gone++; gone < patience {
				time.Sleep(time.Second)
				asked = time.Time{}
				continue
			}
			b.fail()
			return
		case state == "RUNNING" || state == "COMPLETING" || said.taken: // ( its agent's word is fresher than the batch system's )
			if !said.taken { // handed over, not started yet: said once it lasts, an agent that is there takes it within the second
				more = ""
				if time.Since(since) > 2*time.Second {
					more = "its turn in the allocation ( slurm job " + b.meta.Slurm + " )"
				}
				break
			}
			now, more = "running", ""
			if node != "" {
				more = "on " + node
			}
			if what := said.waiting; what != "" { // its turn inside the allocation
				if i := strings.Index(what, ": "); i >= 0 && strings.HasPrefix(what, "room on ") {
					what = what[i+2:]
				}
				now, more = "waiting", what+", in the allocation"
			}
		case reason != "" && reason != "None":
			more += " ( " + reason + " )"
		}
		gone = 0
		b.mu.Lock()
		if now != b.state || more != b.more {
			b.state, b.more = now, more
			b.wake()
			b.mu.Unlock()
			if now == "waiting" {
				_ = kit.WriteRecord(filepath.Join(b.dir, "waiting"), map[string]any{"what": more, "since": kit.Seconds(time.Now())})
			} else {
				_ = os.Remove(filepath.Join(b.dir, "waiting"))
			}
			b.a.touched()
		} else {
			b.mu.Unlock()
		}
		switch age := time.Since(since); { // a run in an allocation that is there may end within the second
		case age < 5*time.Second:
			time.Sleep(100 * time.Millisecond)
		case age < 30*time.Second:
			time.Sleep(500 * time.Millisecond)
		default:
			time.Sleep(2 * time.Second)
		}
	}
}

func (b *batchJob) end(code int) {
	next := b.again(code)
	_ = kit.WriteWhole(filepath.Join(b.dir, "exit"), fmt.Sprint(code))
	_ = os.Remove(filepath.Join(b.dir, "waiting"))
	b.mu.Lock()
	b.code, b.state, b.more, b.next = &code, "ended", "", next
	b.wake()
	b.mu.Unlock()
	b.a.touched()
}

// again: the run ended having asked for what it did not have, and its node said so in the record on the disk they
// share: this agent -- the one that submitted it, or that adopted it -- launches it again, as it launched it ( rerun.go ).
// A run whose node could not say how it ended is not made again from what it may have asked.
func (b *batchJob) again(code int) []runinfo.Launched {
	if !b.known || b.noAgain {
		return nil
	}
	path := filepath.Join(b.meta.Leaf, runinfo.ResultName)
	rec, ok := runinfo.ReadRecord(path)
	asks := asksOf(b.spec, b.meta.Leaf, rec, code)
	if !ok || len(asks) == 0 || rec.Again != nil {
		return nil
	}
	next, said, refused := b.a.again(b.spec, asks)
	text := "errand: asked for " + runinfo.AsksSaid(asks) + ": run again\n"
	if refused != "" {
		text = "errand: " + refused + "\n"
	}
	for _, name := range []string{runinfo.StreamNames["err"], runinfo.StreamNames["both"]} { // said to whoever follows it, as the node's agent says
		if f, err := os.OpenFile(filepath.Join(b.meta.Leaf, name), os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			_, _ = f.WriteString(text)
			f.Close()
		}
	}
	rec.NoteAsked(asks, b.spec.Redo == nil, next, said, refused)
	_ = kit.WriteRecord(path, rec)
	runinfo.Settled(b.meta.Leaf, nil, "")
	return next
}

// fail: the allocation ended and the run did not say how -- cancelled, out of time, a node that went: said for it,
// with what the batch system says of the job and the end of what its step printed
func (b *batchJob) fail() {
	b.noAgain = true
	freshLook(b.meta.Leaf)
	if r, ok := runinfo.ReadRecord(filepath.Join(b.meta.Leaf, runinfo.ResultName)); ok { // it did say, after all: its word stands
		code := 1
		if r.Exit != nil {
			code = *r.Exit
		}
		b.end(code)
		return
	}
	why := "the allocation ended before the run said how it ended ( slurm job " + b.meta.Slurm
	if out, err := exec.Command("sacct", "-n", "-X", "-j", b.meta.Slurm, "-o", "State").Output(); err == nil {
		if state := strings.TrimSpace(string(out)); state != "" {
			why += ": " + strings.ToLower(strings.Fields(state)[0])
		}
	}
	why += " )"
	if tail := runinfo.LastLines(b.meta.Alloc+".out", 6); tail != "" {
		why += "\n" + tail
	}
	if rec, ok := runinfo.ReadRecord(filepath.Join(b.meta.Leaf, runinfo.LaunchName)); ok {
		code := 1
		rec.Ended(code, filepath.Join(b.meta.Leaf, runinfo.StreamNames["err"]))
		rec.Status, rec.Error = "FAIL", &why
		out := runinfo.StreamNames["both"]
		rec.Output = &out
		rec.Results = runinfo.MergeResults(filepath.Join(b.meta.Leaf, "results.jsonl"))
		_ = kit.WriteRecord(filepath.Join(b.meta.Leaf, runinfo.ResultName), rec)
		runinfo.Settled(b.meta.Leaf, nil, "")
	}
	b.end(1)
}

// freshLook: a directory of a shared disk, looked at as it is now and not as this machine remembers it -- something
// written in it from here makes this machine ask the server again what it holds
func freshLook(dir string) {
	probe := filepath.Join(dir, fmt.Sprintf(".errand-look-%d", os.Getpid()))
	if f, err := os.Create(probe); err == nil {
		f.Close()
		_ = os.Remove(probe)
	}
}

// stop: this run stopped, and not the allocation it shares -- its agent is told; an allocation still queued that
// nobody else waits for is given back to the batch system
func (b *batchJob) stop() {
	if b.meta.Alloc == "" {
		_ = exec.Command("scancel", b.meta.Slurm).Run()
		return
	}
	_ = kit.AppendLine(handedFile(b.meta.Alloc), handed{Stop: b.meta.ID})
	if state, _, _, _ := slurmSays(b.meta.Slurm); state == "PENDING" {
		if len(activeIn(b.meta.Alloc)) == 0 {
			_ = exec.Command("scancel", b.meta.Slurm).Run()
			_ = os.RemoveAll(b.meta.Alloc)
		}
		if rec, ok := runinfo.ReadRecord(filepath.Join(b.meta.Leaf, runinfo.LaunchName)); ok {
			why, code := "stopped while it waited for the allocation", 130
			rec.Status, rec.Error, rec.Exit = "FAIL", &why, &code
			_ = kit.WriteRecord(filepath.Join(b.meta.Leaf, runinfo.ResultName), rec)
			runinfo.Settled(b.meta.Leaf, nil, "")
		}
		return
	}
	poke(b.meta.Slurm)
}

// follow: the run, on a channel, from the files it writes on the disk shared with its node -- what it wrote and
// writes, then how it ended. While the batch system keeps it waiting, for what.
func (b *batchJob) follow(req map[string]any, f *reach.FarChan) {
	both, _ := req["both"].(bool)
	f.Reply(map[string]any{"ok": true, "live": true})
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case m := <-f.Inbox:
				if m.Kind == kit.SIGNAL { // a stop is said: the batch system is told
					var s map[string]any
					if json.Unmarshal(m.Payload, &s) == nil && s["stop"] == true {
						b.stop()
					}
				}
			case <-f.Gone:
				return
			case <-done:
				return
			}
		}
	}()
	names := []string{"out", "err"}
	if both {
		names = []string{"both"}
	}
	kinds := map[string]byte{"out": kit.OUT, "err": kit.ERR, "both": kit.OUT}
	at := map[string]int64{}
	buf := make([]byte, kit.Chunk)
	said, last := "", false
	wanted := map[string]bool{}
	var steps *draw.StepTail
	if show, _ := req["status"].(bool); show {
		steps = &draw.StepTail{Path: filepath.Join(b.meta.Leaf, "steps.jsonl")}
	}
	for {
		b.mu.Lock()
		state, more, code, wait, next := b.state, b.more, b.code, b.changed, b.next
		b.mu.Unlock()
		if waiting := map[bool]string{true: more}[state == "waiting" && code == nil]; waiting != said {
			said = waiting
			note, _ := json.Marshal(map[string]any{"waiting": waiting})
			f.Out(kit.NOTE, note)
		}
		for _, w := range b.a.pendingFor(b.meta.Project) {
			if !wanted[w.ID] {
				wanted[w.ID] = true
				note, _ := json.Marshal(map[string]any{"want": w})
				f.Out(kit.NOTE, note)
			}
		}
		if steps != nil {
			if st, ok := steps.Poll(); ok {
				note, _ := json.Marshal(map[string]any{"step": st})
				f.Out(kit.NOTE, note)
			}
		}
		for _, n := range names {
			file, err := os.Open(filepath.Join(b.meta.Leaf, runinfo.StreamNames[n]))
			if err != nil {
				continue
			}
			for {
				got, err := file.ReadAt(buf, at[n])
				if got > 0 {
					f.Out(kinds[n], append([]byte{}, buf[:got]...))
					at[n] += int64(got)
				}
				if err != nil || got == 0 {
					break
				}
			}
			file.Close()
		}
		if code != nil {
			if last { // ( read once more after its end was known: what it wrote last is not missed )
				if len(next) > 0 { // whoever follows it is carried to what was made after it
					again, _ := json.Marshal(map[string]any{"again": next})
					f.Out(kit.NOTE, again)
				}
				end, _ := json.Marshal(map[string]any{"code": *code})
				f.Out(kit.EXIT, end)
				return
			}
			last = true
			continue
		}
		select {
		case <-wait:
		case <-time.After(300 * time.Millisecond): // the files are a node's: nothing says when they grow
		case <-f.Gone:
			return
		}
	}
}

// serveAllocation: the agent of an allocation takes the runs it is handed, for as long as it lives -- told to look
// by a signal, and looking by itself now and then ( the directory is on a shared disk: looked at afresh )
func (a *Place) serveAllocation(dir string, looked chan struct{}) {
	woken := make(chan os.Signal, 4)
	signal.Notify(woken, system.Poked)
	_ = os.MkdirAll(filepath.Join(dir, "runs"), 0o755)
	var at int64
	stopped := map[string]bool{}
	for first := true; ; first = false {
		var lines []handed
		lines, at = readHanded(dir, at)
		for _, h := range lines {
			if h.Stop != "" {
				stopped[h.Stop] = true
				a.mu.Lock()
				j := a.jobs[h.Stop]
				a.mu.Unlock()
				if j != nil {
					j.stop(syscall.SIGINT)
				}
				continue
			}
			if h.Spec == nil || h.Spec.V != 1 || stopped[h.Run] {
				continue
			}
			spec, id := *h.Spec, h.Run
			if spec.Needs.CPUs == 0 && spec.Needs.RAM == 0 && spec.Needs.GPUs == 0 { // it says nothing of what it needs: all of it
				spec.Exclusive = true
			}
			_ = kit.AppendLine(runFile(dir, id), map[string]any{"taken": queue.Host()})
			got, err := a.launch(spec)
			if err != nil { // said in the run's record: nobody is connected to hear it
				if rec, ok := runinfo.ReadRecord(filepath.Join(spec.Leaf, runinfo.LaunchName)); ok {
					why, code := err.Error(), 1
					rec.Status, rec.Error, rec.Exit = "FAIL", &why, &code
					_ = kit.WriteRecord(filepath.Join(spec.Leaf, runinfo.ResultName), rec)
				}
				_ = kit.AppendLine(runFile(dir, id), map[string]any{"done": 1})
				continue
			}
			a.mu.Lock()
			j := a.jobs[got.Note.ID]
			a.mu.Unlock()
			select { // first handed, first in line: the next one is started once this one has its place, or its turn
			case <-j.queued:
			case <-time.After(30 * time.Second):
			}
			go func(id string) { // its end, said where the login node reads it; nothing of it is kept by this agent
				<-j.done
				code := 1
				j.mu.Lock()
				if j.code != nil {
					code = *j.code
				}
				j.mu.Unlock()
				_ = kit.AppendLine(runFile(dir, id), map[string]any{"done": code})
				a.mu.Lock()
				delete(a.jobs, id)
				a.mu.Unlock()
				_ = os.RemoveAll(j.dir)
				a.touched()
			}(id)
		}
		if first {
			close(looked)
		}
		select {
		case <-woken:
		case <-time.After(time.Second):
		}
	}
}

// attended: how a shell is started in an allocation -- by Slurm's own `srun --pty`, which links this machine to the node
// for as long as the shell lasts: what a run handed over does without ( it outlives whoever asked )
func attended(s runinfo.LaunchSpec, at int, root string) ([]string, error) {
	environ := s.Environ
	if environ == nil {
		environ = os.Environ()
	}
	inner, err := layer.Wrap(s.Stack[at+1:], layer.Command{Argv: s.Words}, layer.Context{Root: root, Tags: s.Tags, Environ: environ})
	if err != nil {
		return nil, fmt.Errorf("the environment %s: %w", s.Env, err)
	}
	if _, err := kit.LookIn("srun", environ, root); err != nil {
		return nil, fmt.Errorf("the environment %s hands its work to Slurm, and there is no srun here", s.Env)
	}
	flags := layer.SlurmFlags(s.Stack[at], s.Needs.CPUs, s.Needs.RAM, int(s.Needs.GPUs), s.Exclusive, s.Time, 1)
	argv := append(append([]string{"srun"}, flags...), "--job-name", "errand-shell", "--chdir", root, "--pty")
	if len(inner.Env) > 0 {
		argv = append(argv, "env")
		names := make([]string, 0, len(inner.Env))
		for k := range inner.Env {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			argv = append(argv, k+"="+inner.Env[k])
		}
	}
	return append(argv, inner.Argv...), nil
}
