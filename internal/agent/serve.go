// Servers kept while used ( the guide's "Servers kept while used"; ADR 0018 ), by the agent of the machine they run on.
//
// A service is started on demand, never twice, and stopped when nobody uses it. The agent holds everything of one: its
// place in the queue -- firm while it is used, kept warm, giving way, otherwise --, its announced ports, where it listens
// itself and relays each connection to the server's side ( lease.go ), and the server, a process group of its own. One per machine,
// user and definition ( ADR 0040 ), whichever version of errand answers: an agent that was retired keeps what it serves, and is
// asked.
//
// What a service is comes from the request ( ADR 0040 ); a page's, from what was installed on this machine for it.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/system"
)

const (
	probeEvery   = 2 * time.Second
	probeTimeout = 5 * time.Second // a probe that does not answer within this counts as busy
)

// ── one service ──────────────────────────────────────────────────────────────

// service is one service of this machine, held by this agent.
type service struct {
	a      *Place
	spec   svc.ServiceSpec
	config string // the hash of its definition
	key    string // ... and of the program found for it
	dir    string

	mu       sync.Mutex
	state    string // waiting, starting, in use, busy, warm, stopped, failed
	detail   string
	server   map[string]int
	conns    int
	waiting  int
	refused  int
	wants    int // commands that wait for it
	wantedAt time.Time
	started  time.Time
	pid      int
	ready    chan struct{} // closed once the server answers
	quit     chan struct{} // closed when it is asked to stop
	done     chan struct{}
	changed  chan struct{}
	ended    bool
	holds    map[string]hold // what was asked of how it is kept, by whom
	log      *os.File
}

func (s *service) say(state, detail string) {
	s.mu.Lock()
	if state != "" {
		s.state = state
	}
	s.detail = detail
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	s.a.touched()
}

func (s *service) note(format string, args ...any) {
	fmt.Fprintf(s.log, "%s errand: %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// status: how a service is, for whoever asks
func (s *service) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	ports := []map[string]any{}
	for _, p := range s.spec.Ports {
		ports = append(ports, map[string]any{"name": p.Name, "server": s.server[p.Name]})
	}
	detail := s.detail
	switch {
	case s.conns > 0:
		detail = strings.TrimSpace(fmt.Sprintf("%d connection%s %s", s.conns, reach.Plural(s.conns), detail))
	}
	return map[string]any{"name": s.spec.Name, "state": s.state, "detail": detail, "ports": ports, "conns": s.conns, "waiting": s.waiting,
		"refused": s.refused, "source": s.spec.Source, "config": s.config, "key": s.key, "started": kit.Seconds(s.started), "pid": s.pid, "log": filepath.Join(s.dir, "keeper.log"), "ended": s.ended}
}

func (s *service) row() reach.ServingRow {
	st := s.status()
	row := reach.ServingRow{Name: s.spec.Name, State: kit.Text(st["state"]), More: kit.Text(st["detail"]), Started: kit.Num(st["started"], 0), On: layer.Describe(s.spec.On)}
	row.Command, row.Source, row.Idle, row.Log, row.Agent = s.spec.Command, s.spec.Source, s.idle(), filepath.Join(s.dir, "keeper.log"), os.Getpid()
	if s.spec.Claims() {
		row.Needs = s.spec.NeedsText()
	}
	s.mu.Lock()
	row.Users, row.Waiting, row.Pid = s.conns, s.waiting, s.pid
	row.Config, row.Key, row.Vars, row.Cwd = s.config, s.key, s.spec.ShownVars(), s.spec.Dir()
	for _, p := range s.spec.Ports {
		row.Ports = append(row.Ports, reach.ServingPort{Name: p.Name, Server: s.server[p.Name]})
	}
	s.mu.Unlock()
	return row
}

// serving: the services this agent holds
func (a *Place) serving() []reach.ServingRow {
	a.mu.Lock()
	all := make([]*service, 0, len(a.services))
	for _, s := range a.services {
		all = append(all, s)
	}
	a.mu.Unlock()
	out := []reach.ServingRow{}
	for _, s := range all {
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		if !ended {
			out = append(out, s.row())
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

const restedShown = 7 * 24 * time.Hour

// rested: the services that ran on this machine, whichever agent held them, and that none of `held` is -- as their directory
// says: what errand last wrote in their journal
func rested(held []reach.ServingRow) []reach.ServingRow {
	on := map[string]bool{}
	for _, r := range held {
		on[r.Key] = true
	}
	var out []reach.ServingRow
	for _, d := range svc.ServingDirs() {
		if on[d.Key] || time.Since(d.Last) > restedShown || kit.Exists(filepath.Join(d.Dir, "server.json")) {
			continue
		}
		config, _, _ := strings.Cut(d.Key, ".")
		path := filepath.Join(d.Dir, "keeper.log")
		row := reach.ServingRow{Name: d.Label, Config: config, Key: d.Key, State: "stopped", Log: path, Ended: kit.Seconds(d.Last)}
		if said := lastSaid(path, d.Size); said != "" {
			row.More = strings.TrimPrefix(said, "stopped: ")
			if strings.HasPrefix(said, "failed") {
				row.State, row.More = "failed", strings.TrimPrefix(said, "failed: ")
			}
			if row.More == row.State {
				row.More = ""
			}
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// lastSaid: the last line errand wrote in a service's journal, among what the server wrote
func lastSaid(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, min(size, 16<<10))
	n, _ := f.ReadAt(buf, size-int64(len(buf)))
	lines := strings.Split(string(buf[:n]), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := errandSaid.FindStringSubmatch(lines[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

var errandSaid = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d errand: (.*)$`)

// keeping: how many services, leases, and machines errand boots, are held
func (a *Place) keeping() int {
	a.leaseMu.Lock()
	n := 0
	for _, l := range a.leases {
		if !l.rec.Spec.Always || l.conns > 0 { // ( the system listens for one that is always: no agent needs to wait for it )
			n++
		}
	}
	a.leaseMu.Unlock()
	return n + len(a.serving()) + len(a.vms())
}

// otherServer is a server another agent of this machine holds.
type otherServer struct{ dir, key string }

// serverFor: the server of a definition -- the one that runs, this agent's or another's, else one that starts. `again`: tried
// even if it has just failed to start.
func (a *Place) serverFor(spec svc.ServiceSpec, config string, again bool) (*service, otherServer, error) {
	_, stamp, missing := spec.Program(a.serviceEnviron())
	key := config + "." + stamp
	is := func(c, k string) bool { return k == key || (missing != nil && c == config) } // ( a program that is gone: the server that runs will do )
	for _, s := range a.runningServices() {
		if is(s.config, s.key) {
			return s, otherServer{}, nil
		}
	}
	for dir, o := range a.othersSay() {
		for _, r := range o.Serving {
			if is(r.Config, r.Key) {
				return nil, otherServer{dir, r.Key}, nil
			}
		}
	}
	if missing != nil {
		return nil, otherServer{}, missing
	}
	if f, ok := a.failureOf(key); ok && !again && f.left() > 0 {
		return nil, otherServer{}, errors.New(f.why)
	}
	if now, err := spec.Hash(); err != nil || now != config {
		return nil, otherServer{}, errors.New("the files that make it have changed: it is to be asked for again")
	}
	s, err := a.keepService(spec, config, key)
	return s, otherServer{}, err
}

func (a *Place) failureOf(key string) (failure, bool) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	f, ok := a.failed[key]
	return f, ok
}

// noteFailure: a server did not start ( "": it did )
func (a *Place) noteFailure(key, why string) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	if why == "" {
		delete(a.failed, key)
		return
	}
	if a.failed == nil {
		a.failed = map[string]failure{}
	}
	a.failed[key] = failure{why, a.failed[key].n + 1, time.Now()}
}

// runningServices: the services of this agent that have not ended
func (a *Place) runningServices() []*service {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*service
	for _, s := range a.services {
		s.mu.Lock()
		if !s.ended {
			out = append(out, s)
		}
		s.mu.Unlock()
	}
	return out
}

// othersSay: what each other agent of this machine holds, by its directory
func (a *Place) othersSay() map[string]reach.OverviewDoc {
	out := map[string]reach.OverviewDoc{}
	for _, dir := range reach.Others(a.Root, a.Dir) {
		got, err := reach.AskAgent(dir, map[string]any{"op": "own"})
		var o reach.OverviewDoc
		if err == nil && reach.DecodeInto(got["own"], &o) == nil {
			out[dir] = o
		}
	}
	return out
}

// keepService: a server of this agent's, that starts -- or the one another request has just started
func (a *Place) keepService(spec svc.ServiceSpec, config, key string) (*service, error) {
	if err := spec.Check(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.services == nil {
		a.services = map[string]*service{}
	}
	if s := a.services[key]; s != nil {
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		if !ended {
			return s, nil
		}
	}
	dir := svc.ServingDir(spec.Name, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(dir, "keeper.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &service{a: a, spec: spec, config: config, key: key, dir: dir, state: "starting", server: map[string]int{}, holds: map[string]hold{},
		ready: make(chan struct{}), quit: make(chan struct{}), done: make(chan struct{}), changed: make(chan struct{}), started: time.Now(), log: log, wantedAt: time.Now()}
	a.services[key] = s
	a.idleSince = time.Now()
	go s.run()
	return s, nil
}

// kept: how long it stays unused, and between being asked to stop and being killed: the longest of what is asked of it now; else
// what it was started with
func (s *service) kept() (idle, grace float64) {
	asked := s.a.holdsOf(s.config)
	now := kit.Seconds(time.Now())
	s.mu.Lock()
	for _, h := range s.holds {
		asked = append(asked, h)
	}
	s.mu.Unlock()
	for _, h := range asked {
		if h.Until > now {
			idle, grace = max(idle, h.Idle), max(grace, h.Grace)
		}
	}
	if idle == 0 {
		idle = s.spec.Idle
	}
	if grace == 0 {
		grace = s.spec.Grace
	}
	if sharedHere() {
		idle = min(idle, sharedIdle)
	}
	return idle, grace
}

func (s *service) idle() float64 { i, _ := s.kept(); return i }

func (s *service) grace() float64 { _, g := s.kept(); return g }

// OwnConnection: whether a connection to one of our ports was opened by this account ( Linux: `/proc/net/tcp` says by
// which; elsewhere it cannot be told, and every one is relayed )
func OwnConnection(c net.Conn) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	remote, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return true
	}
	data, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return true
	}
	want := fmt.Sprintf(":%04X", remote.Port)
	local := fmt.Sprintf(":%04X", c.LocalAddr().(*net.TCPAddr).Port)
	uid := strconv.Itoa(os.Getuid())
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 7 && strings.HasSuffix(f[1], want) && strings.HasSuffix(f[2], local) {
			return f[7] == uid
		}
	}
	return true
}

// open: a way to the server's side of a port, once it answers; the connection counts as a use from here to its close, or
// to the moment its client goes ( `gone`, nil when that cannot be told ): nobody waits for it any more
func (s *service) open(name string, gone <-chan struct{}) (net.Conn, func(), error) {
	s.mu.Lock()
	s.conns++
	ready := s.ready
	s.mu.Unlock()
	s.say("", "")
	release := func() {
		s.mu.Lock()
		s.conns--
		s.mu.Unlock()
		s.say("", "")
	}
	select {
	case <-ready:
	default:
		s.mu.Lock()
		s.waiting++
		s.mu.Unlock()
		select {
		case <-ready:
		case <-s.done:
		case <-gone:
		}
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}
	select {
	case <-s.done:
		release()
		return nil, nil, errors.New("it stopped")
	case <-gone: // ( asked first: one that left sends nothing to a server that answers at the same moment )
		release()
		return nil, nil, errors.New("its client left")
	default:
	}
	s.mu.Lock()
	port := s.server[name]
	s.mu.Unlock()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second)
	if err != nil {
		release()
		return nil, nil, err
	}
	return c, release, nil
}

// closeWrite: nothing more is sent on it; what comes is still read
func closeWrite(c net.Conn) {
	if t, ok := c.(interface{ CloseWrite() error }); ok {
		_ = t.CloseWrite()
	} else {
		_ = c.Close()
	}
}

// bothWays: two streams joined, until both sides are through
func bothWays(a, b net.Conn) {
	var wg sync.WaitGroup
	pump := func(from, to net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(to, from)
		closeWrite(to)
	}
	wg.Add(2)
	go pump(a, b)
	go pump(b, a)
	wg.Wait()
}

// ── its probes ───────────────────────────────────────────────────────────────

// probe: a question asked of the server -> its answer; false when it could not be asked
func (s *service) probe(what string, how any) (bool, bool) {
	type got struct{ yes, ok bool }
	answer := make(chan got, 1)
	go func() {
		var g got
		line, h, _ := svc.ProbeOf(how)
		switch {
		case line != "":
			s.mu.Lock()
			line, err := svc.FillPorts(line, s.server)
			s.mu.Unlock()
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", line)
				cmd.Env = s.a.serviceEnviron()
				err = cmd.Run()
				var exit *exec.ExitError
				g = got{err == nil, err == nil || (errors.As(err, &exit) && ctx.Err() == nil)}
			}
		case h != nil:
			{
				s.mu.Lock()
				port := 0
				if h.Port != "" {
					port = s.server[h.Port]
				} else if len(s.spec.Ports) > 0 {
					port = s.server[s.spec.Ports[0].Name]
				}
				s.mu.Unlock()
				client := http.Client{Timeout: probeTimeout}
				if r, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, h.Path)); err == nil {
					var v any
					err = json.NewDecoder(r.Body).Decode(&v)
					r.Body.Close()
					g = got{caches.Truthy(h.In(v)), err == nil || errors.Is(err, io.EOF)}
				}
			}
		}
		answer <- g
	}()
	select {
	case g := <-answer:
		return g.yes, g.ok
	case <-time.After(probeTimeout + time.Second):
		return false, false
	}
}

// busy: whether the server is in the middle of something. A probe that fails counts as busy: a saturated server is.
func (s *service) busy() bool {
	if s.spec.Busy == nil {
		return false
	}
	yes, ok := s.probe("busy", s.spec.Busy)
	return yes || !ok
}

func (s *service) answers() bool {
	if s.spec.Ready != nil {
		yes, ok := s.probe("ready", s.spec.Ready)
		return yes && ok
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, port := range s.server {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			return false
		}
		c.Close()
	}
	return true
}

// serviceEnviron: the surroundings a server starts in
func (a *Place) serviceEnviron() []string { return loginEnviron() }

// ── its life ─────────────────────────────────────────────────────────────────

func (s *service) used() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns > 0 || s.wants > 0 || time.Since(s.wantedAt) < 2*time.Second
}

func (s *service) run() {
	why := "stopped"
	defer func() {
		s.mu.Lock()
		s.ended, s.pid = true, 0
		s.mu.Unlock()
		state := "stopped"
		if strings.HasPrefix(why, "failed") {
			state = "failed"
			s.a.noteFailure(s.key, strings.TrimPrefix(why, "failed: "))
		}
		s.note("%s", why)
		_ = os.Remove(filepath.Join(s.dir, "server.json")) // ( before it is said: it rests from then on )
		close(s.done)
		s.say(state, why)
		s.log.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // given up when it is asked to stop, or when nobody waits for it any more
		idle := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.quit:
				cancel()
				return
			case <-time.After(300 * time.Millisecond):
			}
			select {
			case <-s.ready:
				return
			default:
			}
			if s.used() {
				idle = time.Now()
			} else if time.Since(idle).Seconds() > s.idle() {
				cancel()
				return
			}
		}
	}()
	env := []string{}
	var granted held
	label := s.spec.Label
	if label == "" {
		label = s.spec.Name
	}
	if s.spec.Claims() {
		s.say("waiting", "")
		g, err := s.a.claim(ctx, queue.Request{Needs: s.spec.Asks().Normalized(1), Exclusive: s.spec.Exclusive, Label: label + " ( service )",
			Waiting: func(what string) { s.say("waiting", what) }})
		if err != nil {
			why = "gave up: nobody waited for it any more"
			if ctx.Err() == nil {
				why = "failed: " + err.Error()
			}
			return
		}
		granted = g
		defer granted.Release()
		for k, v := range granted.Env() {
			env = append(env, k+"="+v)
		}
		for k, v := range granted.HandedDown() {
			env = append(env, k+"="+v)
		}
	}
	why = s.hold(env, granted)
}

// hold: the server started, kept while it is used, stopped -> why it ended
func (s *service) hold(env []string, granted held) string {
	for _, p := range s.spec.Ports {
		port := p.Server
		if port == 0 {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return "failed: " + err.Error()
			}
			port = l.Addr().(*net.TCPAddr).Port
			l.Close()
		} else if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			return fmt.Sprintf("failed: port %s: the server's side, %d, is taken", p.Name, port)
		} else {
			l.Close()
		}
		s.mu.Lock()
		s.server[p.Name] = port
		s.mu.Unlock()
		env = append(env, fmt.Sprintf("PORT_%s=%d", strings.ToUpper(p.Name), port))
		if len(s.spec.Ports) == 1 {
			env = append(env, fmt.Sprintf("PORT=%d", port))
		}
	}
	more, vars := map[string]string{}, map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		more[k] = v
	}
	for k, v := range s.spec.Env {
		vars[k], _ = svc.FillPorts(v, s.server)
	}
	var argv []string
	for _, w := range s.spec.Command {
		w, _ = svc.FillPorts(w, s.server)
		argv = append(argv, w)
	}
	full := s.a.serviceEnviron()
	words := strings.Join(argv, " ")
	argv, vars, err := s.spec.Started(argv, vars, more, full)
	if err != nil {
		return "failed: " + err.Error()
	}
	for k, v := range vars {
		full = append(full, k+"="+v)
	}
	cwd := s.spec.Dir()
	path, err := kit.LookIn(argv[0], full, cwd)
	if err != nil {
		return fmt.Sprintf("failed: %s: %v", argv[0], err)
	}
	s.note("starting %s  ( %s )", words, portsText(s.server))
	cmd := &exec.Cmd{Path: path, Args: argv, Env: full, Dir: cwd, Stdout: s.log, Stderr: s.log, SysProcAttr: system.Apart()}
	if err := cmd.Start(); err != nil {
		return "failed: " + err.Error()
	}
	s.mu.Lock()
	s.pid = cmd.Process.Pid
	s.mu.Unlock()
	_ = kit.WriteWholeJSON(filepath.Join(s.dir, "server.json"), map[string]any{"pid": cmd.Process.Pid, "start": fmt.Sprint(kit.Started(cmd.Process.Pid)), "agent": os.Getpid()})
	gone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(gone) }()
	stop := func() {
		_, grace := s.kept()
		for _, step := range []struct {
			sig  syscall.Signal
			wait time.Duration
		}{{syscall.SIGINT, 2 * time.Second}, {syscall.SIGTERM, time.Duration(max(grace-2, 1) * float64(time.Second))}, {syscall.SIGKILL, 5 * time.Second}} {
			_ = system.SignalGroup(cmd.Process.Pid, step.sig)
			select {
			case <-gone:
				_ = system.SignalGroup(cmd.Process.Pid, syscall.SIGKILL) // what it left of its group
				return
			case <-time.After(step.wait):
			}
		}
	}
	defer stop()
	s.say("starting", "")
	for !s.answers() {
		select {
		case <-gone:
			return "failed: it ended before it answered ( " + cmd.ProcessState.String() + " )"
		case <-s.quit:
			return "stopped"
		case <-time.After(100 * time.Millisecond):
		}
	}
	close(s.ready)
	s.a.noteFailure(s.key, "")
	var g *queue.Granted
	if own, ok := granted.(*queue.Granted); ok {
		g = own
	}
	firm := true
	idle := time.Now()
	probed := time.Time{}
	busy := false
	for {
		s.mu.Lock()
		conns, wait := s.conns, s.changed
		s.mu.Unlock()
		state := "warm"
		switch {
		case conns > 0:
			state, busy = "in use", false
		default:
			if time.Since(probed) >= probeEvery {
				busy, probed = s.busy(), time.Now()
			}
			if busy {
				state = "busy"
			}
		}
		if state != "warm" || s.used() {
			idle = time.Now()
		}
		if g != nil && firm != (state != "warm") {
			firm = state != "warm"
			g.SetPreemptible(!firm)
		}
		s.mu.Lock()
		differs := s.state != state
		s.mu.Unlock()
		if differs {
			s.say(state, "")
		}
		switch {
		case !firm && g != nil && g.Evicted():
			return "gave way to a computation"
		case time.Since(s.started) > sharedService && sharedHere():
			return "stopped: two hours on a machine shared with others"
		case time.Since(idle).Seconds() > s.idle():
			return fmt.Sprintf("stopped: unused for %s", reach.Span(s.idle()))
		}
		select {
		case <-gone:
			return "it ended ( " + cmd.ProcessState.String() + " )"
		case <-s.quit:
			return "stopped"
		case <-wait:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *service) stop() {
	s.mu.Lock()
	select {
	case <-s.quit:
	default:
		close(s.quit)
	}
	s.mu.Unlock()
}

// sweepServices: a server an agent that was killed left -- nobody relays to it any more
func (a *Place) sweepServices() {
	files, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "serving", "*@"+queue.Host(), "server.json"))
	booted, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "vm", "*.json")) // ( QEMUs, likewise )
	files = append(files, booted...)
	for _, f := range files {
		var was struct {
			Pid   int    `json:"pid"`
			Start string `json:"start"`
			Agent int    `json:"agent"`
		}
		data, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(data, &was) != nil || was.Pid <= 0 {
			continue
		}
		if was.Agent > 0 && was.Agent != os.Getpid() && system.Signal(was.Agent, 0) == nil {
			continue // its agent lives
		}
		if fmt.Sprint(kit.Started(was.Pid)) == was.Start && was.Start != "" {
			_ = system.SignalGroup(was.Pid, syscall.SIGTERM)
			time.AfterFunc(3*time.Second, func() {
				if fmt.Sprint(kit.Started(was.Pid)) == was.Start {
					_ = system.SignalGroup(was.Pid, syscall.SIGKILL)
				}
			})
		}
		_ = os.Remove(f)
	}
}

// ── what is asked of an agent ────────────────────────────────────────────────

// askedService: what a request asks for -- said in it ( `spec` ); what a page asks for ( `page`: NAME#VERSION ); an
// authorization's hash ( `hash`, to `only` when the link is restricted ); a lease's label or hash ( `name` )
func (a *Place) askedService(req map[string]any) (spec svc.ServiceSpec, err error) {
	name, page, hash, only := kit.Text(req["name"]), kit.Text(req["page"]), kit.Text(req["hash"]), kit.Text(req["only"])
	refuse := func(r svc.Refusal) {
		r.Why = err.Error()
		svc.Refuse(r)
		a.stir()
	}
	switch {
	case req["spec"] != nil:
		if err = reach.DecodeInto(req["spec"], &spec); err != nil {
			return
		}
		spec.Source = "asked for"
	case page != "":
		installed, said := svc.PageServices()
		var ok bool
		if spec, ok = installed[page]; !ok {
			why := fmt.Sprintf("%s is not authorized to pages on %s: errand serve authorize --to pages %s [-- COMMAND]", page, queue.Host(), page)
			for _, s := range said {
				why += "\n" + s
			}
			err = errors.New(why)
			r := svc.Refusal{Who: "pages", What: page, From: kit.Text(req["from"])}
			r.Command, _, _, _ = svc.ComposedHere(page)
			refuse(r)
			return
		}
		if bad := svc.WrongVersion(page, spec.Provider); bad != "" {
			return spec, fmt.Errorf("%s: %s", page, bad)
		}
		if auth := svc.PageAuthorized()[page]; auth != nil {
			svc.UsedNow(auth.Hash)
		}
	case hash != "":
		var auth *svc.Authorization
		spec, auth, err = svc.AuthorizedTo(hash, only)
		if err != nil {
			if r, ok := a.leaseCalled(hash); ok && only == "" { // what this user asked for in full
				return r.Spec, nil
			}
			who := only
			if who == "" {
				who = "connect"
			}
			refuse(svc.Refusal{Who: who, What: hash})
			return
		}
		svc.UsedNow(auth.Hash)
	default:
		r, ok := a.leaseCalled(name) // one that was asked for in full
		if !ok {
			return spec, fmt.Errorf("no lease %q on %s: a server is asked for in full ( errand serve [options] -- COMMAND ), or by an authorization's hash", name, queue.Host())
		}
		spec = r.Spec
	}
	if spec.Idle == 0 {
		spec.Idle = 600
	}
	if spec.Grace == 0 {
		spec.Grace = 10
	}
	return
}

// wantedService: the server a request is for -- by its key, the one this agent holds
func (a *Place) wantedService(req map[string]any, again bool) (*service, otherServer, error) {
	if key := kit.Text(req["key"]); key != "" {
		for _, s := range a.runningServices() {
			if s.key == key {
				var h hold
				if reach.DecodeInto(req["hold"], &h) == nil && h.ID != "" {
					s.mu.Lock()
					s.holds[h.ID] = h
					s.mu.Unlock()
				}
				return s, otherServer{}, nil
			}
		}
		return nil, otherServer{}, errors.New("it has stopped")
	}
	spec, err := a.askedService(req)
	if err != nil {
		return nil, otherServer{}, err
	}
	machine, spec, err := spec.Where()
	if err != nil {
		return nil, otherServer{}, err
	}
	if machine != nil {
		return nil, otherServer{}, errors.New("it runs on " + machine.String("host") + ": that is who is to be asked")
	}
	config := kit.Text(req["config"]) // ( what it was when its lease was taken )
	if config == "" {
		if config, err = spec.Hash(); err != nil {
			return nil, otherServer{}, err
		}
	}
	s, other, err := a.serverFor(spec, config, again)
	var h hold
	if s != nil && reach.DecodeInto(req["hold"], &h) == nil && h.ID != "" {
		s.mu.Lock()
		s.holds[h.ID] = h
		s.mu.Unlock()
	}
	return s, other, err
}

// stopServices: those a word names, whichever agent of this machine holds them -> how many
func (a *Place) stopServices(name, key, config string, alone bool) int {
	n := 0
	called := func(word, label, hash string) bool {
		return hash == config || (config == "" && called(word, label, hash))
	}
	var wg sync.WaitGroup
	for _, s := range a.runningServices() {
		if (key != "" && s.key == key) || (key == "" && called(name, s.spec.Name, s.config)) {
			s := s
			n++
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, grace := s.kept()
				s.stop()
				select {
				case <-s.done:
				case <-time.After(time.Duration((grace + 8) * float64(time.Second))):
				}
			}()
		}
	}
	if !alone {
		for dir, o := range a.othersSay() {
			for _, r := range o.Serving {
				if (key != "" && r.Key == key) || (key == "" && called(name, r.Name, r.Config)) {
					n++
					_, _ = reach.AskAgent(dir, map[string]any{"op": "service_stop", "key": r.Key, "alone": true})
				}
			}
		}
	}
	if !alone && key == "" && config == "" { // those that run elsewhere, for a lease of here
		for _, l := range a.farLeases() {
			if called(name, l.rec.label(), l.rec.Config) {
				if got, err := a.askFar(l.rec.Host, l.environ, map[string]any{"op": "service_stop", "config": l.rec.Config}); err == nil {
					n += int(kit.Num(got["stopped"], 0))
				}
			}
		}
	}
	wg.Wait()
	return n
}

func portsText(ports map[string]int) string {
	var out []string
	for n, p := range ports {
		out = append(out, fmt.Sprintf("%s=%d", n, p))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// keptInput: what the near side of a channel sends, kept until it is read, then closed at its end. What is not read yet does
// not hold the link, which carries other channels, and the word that this one is over.
func keptInput(f *reach.FarChan) <-chan []byte {
	out := make(chan []byte)
	go func() {
		var kept [][]byte
		over := false
		for {
			var head []byte
			var to chan []byte
			switch {
			case len(kept) > 0:
				head, to = kept[0], out
			case over:
				close(out)
				return
			}
			select {
			case m := <-f.Inbox:
				switch m.Kind {
				case kit.IN:
					kept = append(kept, m.Payload)
				case kit.EOF:
					over = true
				}
			case to <- head:
				kept = kept[1:]
			case <-f.Gone:
				return
			}
		}
	}()
	return out
}

// serveService: what is asked of the services of this machine
func (a *Place) serveService(req map[string]any, f *reach.FarChan) {
	name := kit.Text(req["name"])
	switch req["op"] {
	case "lease": // ports, and until when
		spec, err := a.askedService(req)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		r, said, err := a.takeLease(spec, kit.Strs(req["environ"]))
		if err != nil {
			f.Fail("%v", err)
			return
		}
		f.Reply(map[string]any{"ok": true, "lease": a.leaseSaid(r, said)})
	case "service_look": // what a machine says of a server it is to run, for the lease another machine gives
		spec, err := a.askedService(req)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		said, err := a.lookAtServer(spec)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		var h hold
		if reach.DecodeInto(req["hold"], &h) == nil && h.ID != "" {
			for _, s := range a.runningServices() {
				if s.config == said.Config {
					s.mu.Lock()
					s.holds[h.ID] = h
					s.mu.Unlock()
				}
			}
		}
		f.Reply(map[string]any{"ok": true, "look": said})
	case "lease_connect":
		a.leaseConnect(req, f)
	case "lease_yield":
		a.yieldLeases(kit.Text(req["id"]))
		f.Reply(map[string]any{"ok": true})
	case "lease_release": // given back, whichever agent has it
		n := 0
		match := func(r reach.LeaseRow) bool {
			if id := kit.Text(req["id"]); id != "" {
				return r.ID == id
			}
			return called(name, r.Name, r.Config)
		}
		for _, r := range a.leaseRows() {
			if match(r) && a.releaseLease(r.ID) {
				n++
			}
		}
		if req["alone"] != true {
			for dir, o := range a.othersSay() {
				for _, r := range o.Leases {
					if match(r) {
						n++
						_, _ = reach.AskAgent(dir, map[string]any{"op": "lease_release", "id": r.ID, "alone": true})
					}
				}
			}
		}
		f.Reply(map[string]any{"ok": true, "released": n})
	case "service": // started if need be, and said until it answers
		s, other, err := a.wantedService(req, true)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		if s == nil {
			a.pass(other.dir, map[string]any{"op": "service", "key": other.key, "stay": req["stay"]}, f)
			return
		}
		s.mu.Lock()
		s.wants++
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.wants--
			s.wantedAt = time.Now()
			s.mu.Unlock()
		}()
		f.Reply(map[string]any{"ok": true, "status": s.status()})
		last := ""
		for {
			s.mu.Lock()
			wait := s.changed
			s.mu.Unlock()
			st := s.status()
			b, _ := json.Marshal(map[string]any{"status": st})
			if string(b) != last {
				last = string(b)
				f.Out(kit.NOTE, b)
			}
			stay, _ := req["stay"].(bool)
			select {
			case <-s.ready:
				if !stay {
					f.Out(kit.EXIT, []byte(`{"code":0}`))
					return
				}
			default:
			}
			select {
			case <-s.done:
				b, _ := json.Marshal(map[string]any{"status": s.status()})
				f.Out(kit.NOTE, b)
				f.Out(kit.EXIT, []byte(`{"code":1}`))
				return
			case <-wait:
			case <-s.ready:
				if stay {
					select {
					case <-wait:
					case <-s.done:
					case <-f.Gone:
						return
					case <-time.After(time.Second):
					}
				}
			case <-f.Said:
				return
			case <-f.Gone:
				return
			}
		}
	case "service_stop":
		f.Reply(map[string]any{"ok": true, "stopped": a.stopServices(name, kit.Text(req["key"]), kit.Text(req["config"]), req["alone"] == true)})
	case "service_list": // what runs, the ports that wait for what does not, and what is declared
		out := []map[string]any{}
		seen, named := map[string]bool{}, map[string]bool{}
		doc := overview(a)
		for _, r := range doc.Serving {
			named[r.Name] = true
			one := map[string]any{"name": r.Name, "config": r.Config, "state": r.State, "detail": r.More, "ports": []any{}}
			for _, l := range doc.Leases {
				if l.Config == r.Config {
					seen[l.ID] = true
					one["ports"], one["until"] = l.Ports, l.Until
				}
			}
			out = append(out, one)
		}
		for _, l := range doc.Leases {
			named[l.Name] = true
			if !seen[l.ID] {
				state, detail := "rests", ""
				if l.On != "" {
					state, detail = "on "+l.On, ""
				}
				if l.Failed != "" {
					state, detail = "failed", l.Failed
				}
				out = append(out, map[string]any{"name": l.Name, "config": l.Config, "state": state, "detail": detail, "ports": l.Ports, "until": l.Until})
			}
		}
		said := []string{}
		if was := svc.NotRead(); was != "" {
			said = append(said, was)
		}
		all, err := svc.Authorized()
		if err != nil {
			said = append(said, err.Error())
		}
		hashes := make([]string, 0, len(all))
		for h := range all {
			hashes = append(hashes, h)
		}
		sort.Strings(hashes)
		for _, h := range hashes {
			if au := all[h]; !named[au.Label] {
				out = append(out, map[string]any{"name": au.Label, "config": h, "state": "authorized", "detail": "to " + strings.Join(au.To, ", ")})
			}
		}
		f.Reply(map[string]any{"ok": true, "services": out, "said": said})
	case "service_connect": // a connection to one of its ports, on this channel
		s, other, err := a.wantedService(req, false)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		if s == nil {
			a.pass(other.dir, map[string]any{"op": "service_connect", "key": other.key, "port": req["port"]}, f)
			return
		}
		port := kit.Text(req["port"])
		if port == "" {
			port = s.spec.Ports[0].Name
		}
		known := false
		for _, p := range s.spec.Ports {
			known = known || p.Name == port
		}
		if !known {
			f.Fail("%s: no port %q", name, port)
			return
		}
		f.Reply(map[string]any{"ok": true})
		sent := keptInput(f) // from now on: what comes while the server starts is for it, its end included
		c, release, err := s.open(port, f.Gone)
		if err != nil {
			f.Out(kit.EXIT, []byte(`{"code":1}`))
			return
		}
		defer release()
		defer c.Close()
		over := make(chan struct{})
		defer close(over)
		go func() { // ( beside what is sent: a server that reads slowly does not hide that its client left )
			select {
			case <-f.Gone:
				c.Close()
			case <-over:
			}
		}()
		go func() {
			for {
				select {
				case data, more := <-sent:
					if !more { // the client sends no more, and is still to be answered
						if t, ok := c.(*net.TCPConn); ok {
							_ = t.CloseWrite()
						}
						return
					}
					_, _ = c.Write(data) // ( refused, what follows is read all the same: the link it comes by carries other things )
				case <-over:
					return
				}
			}
		}()
		buf := make([]byte, 64<<10)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				f.Out(kit.OUT, append([]byte{}, buf[:n]...))
			}
			if err != nil {
				f.Out(kit.EXIT, []byte(`{"code":0}`))
				return
			}
		}
	}
}
