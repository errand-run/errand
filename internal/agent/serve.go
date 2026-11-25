// Servers kept while used ( the guide's "Servers kept while used"; ADR 0018 ), by the agent of the machine they run on.
//
// A service is started on demand, never twice, and stopped when nobody uses it. The agent holds everything of one: its
// place in the queue -- firm while it is used, kept warm, giving way, otherwise --, its announced ports, where it listens
// itself and relays each connection to the server's side, and the server, a process group of its own. One per machine,
// user and name, whichever version of errand answers: an agent that was retired keeps what it serves, and is asked.
//
// What a service is comes from its declaration, read on the machine it runs on: what was installed, and, by errand's Python,
// the user's `services.py` and the project's files, as data; or from a command line, ad hoc. A probe that is a
// function of the declaration is asked of a Python process that stays as long as the server ( `errand.probes` ).

package agent

import (
	"bufio"
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
	a    *Place
	spec svc.ServiceSpec
	decl svc.Declarations
	dir  string

	mu        sync.Mutex
	state     string // waiting, starting, in use, busy, warm, stopped, failed
	detail    string
	announced map[string]int
	server    map[string]int
	conns     int
	waiting   int
	refused   int
	wants     int // commands that wait for it
	wantedAt  time.Time
	started   time.Time
	pid       int
	ready     chan struct{} // closed once the server answers
	quit      chan struct{} // closed when it is asked to stop
	done      chan struct{}
	changed   chan struct{}
	ended     bool
	listeners []net.Listener
	log       *os.File
	ask       *prober
}

func (a *Place) serviceDir(name string) string {
	return filepath.Join(kit.LocalCache(), "serving", name)
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
		ports = append(ports, map[string]any{"name": p.Name, "port": s.announced[p.Name], "server": s.server[p.Name]})
	}
	detail := s.detail
	switch {
	case s.conns > 0:
		detail = strings.TrimSpace(fmt.Sprintf("%d connection%s %s", s.conns, reach.Plural(s.conns), detail))
	}
	return map[string]any{"name": s.spec.Name, "state": s.state, "detail": detail, "ports": ports, "conns": s.conns, "waiting": s.waiting,
		"refused": s.refused, "source": s.spec.Source, "started": kit.Seconds(s.started), "pid": s.pid, "log": filepath.Join(s.dir, "keeper.log"), "ended": s.ended}
}

func (s *service) row() reach.ServingRow {
	st := s.status()
	row := reach.ServingRow{Name: s.spec.Name, State: kit.Text(st["state"]), More: kit.Text(st["detail"]), Started: kit.Num(st["started"], 0), Project: s.spec.From}
	row.Command, row.Source, row.Idle, row.Log, row.Agent = s.spec.Command, s.spec.Source, s.spec.Idle, filepath.Join(s.dir, "keeper.log"), os.Getpid()
	if s.spec.Claims() {
		row.Needs = s.spec.NeedsText()
	}
	s.mu.Lock()
	row.Users, row.Waiting, row.Pid = s.conns, s.waiting, s.pid
	if len(s.spec.Ports) > 0 {
		row.Port = s.announced[s.spec.Ports[0].Name]
	}
	for _, p := range s.spec.Ports {
		row.Ports = append(row.Ports, reach.ServingPort{Name: p.Name, Port: s.announced[p.Name], Server: s.server[p.Name]})
	}
	s.mu.Unlock()
	if row.Port > 0 {
		row.More = strings.TrimRight(fmt.Sprintf("127.0.0.1:%d, %s", row.Port, row.More), ", ")
	}
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
// says: what errand last wrote in their journal, and the ports kept for them
func rested(held []reach.ServingRow) []reach.ServingRow {
	on := map[string]bool{}
	for _, r := range held {
		on[r.Name] = true
	}
	var out []reach.ServingRow
	logs, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "serving", "*", "keeper.log"))
	for _, path := range logs {
		dir := filepath.Dir(path)
		name := filepath.Base(dir)
		st, err := os.Stat(path)
		if on[name] || err != nil || time.Since(st.ModTime()) > restedShown || kit.Exists(filepath.Join(dir, "server.json")) {
			continue
		}
		row := reach.ServingRow{Name: name, State: "stopped", Log: path, Ended: kit.Seconds(st.ModTime())}
		if said := lastSaid(path, st.Size()); said != "" {
			row.More = strings.TrimPrefix(said, "stopped: ")
			if strings.HasPrefix(said, "failed") {
				row.State, row.More = "failed", strings.TrimPrefix(said, "failed: ")
			}
			if row.More == row.State {
				row.More = ""
			}
		}
		kept := map[string]int{}
		if data, err := os.ReadFile(filepath.Join(dir, "ports.json")); err == nil && json.Unmarshal(data, &kept) == nil {
			for n, p := range kept {
				row.Ports = append(row.Ports, reach.ServingPort{Name: n, Port: p})
			}
			sort.Slice(row.Ports, func(i, k int) bool { return row.Ports[i].Name < row.Ports[k].Name })
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
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

// keeping: how many services, and machines errand boots, are held
func (a *Place) keeping() int { return len(a.serving()) + len(a.vms()) }

// serve: the service `spec` names, this agent's: the one that runs, or one that starts
func (a *Place) keepService(spec svc.ServiceSpec, decl svc.Declarations) (*service, error) {
	if err := spec.Check(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.services == nil {
		a.services = map[string]*service{}
	}
	if s := a.services[spec.Name]; s != nil {
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		if !ended {
			return s, nil
		}
	}
	if sharedHere() {
		spec.Idle = min(spec.Idle, sharedIdle)
	}
	dir := a.serviceDir(spec.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(dir, "keeper.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &service{a: a, spec: spec, decl: decl, dir: dir, state: "starting", announced: map[string]int{}, server: map[string]int{},
		ready: make(chan struct{}), quit: make(chan struct{}), done: make(chan struct{}), changed: make(chan struct{}), started: time.Now(), log: log, wantedAt: time.Now()}
	a.services[spec.Name] = s
	a.idleSince = time.Now()
	go s.run()
	return s, nil
}

// listen: the announced ports -- declared, or chosen once for the name and kept
func (s *service) listen() error {
	kept := map[string]int{}
	path := filepath.Join(s.dir, "ports.json")
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &kept)
	}
	for _, p := range s.spec.Ports {
		want := p.Announced
		if want == 0 {
			want = kept[p.Name]
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", want))
		if err != nil && p.Announced == 0 { // the one that was kept is taken: another, kept from here on
			l, err = net.Listen("tcp", "127.0.0.1:0")
		}
		if err != nil {
			return fmt.Errorf("port %s: %d cannot be listened on: %v", p.Name, want, err)
		}
		s.mu.Lock()
		s.announced[p.Name] = l.Addr().(*net.TCPAddr).Port
		s.listeners = append(s.listeners, l)
		s.mu.Unlock()
		kept[p.Name] = s.announced[p.Name]
		go s.accept(l, p.Name)
	}
	return kit.WriteWholeJSON(path, kept)
}

func (s *service) accept(l net.Listener, name string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		if !s.spec.Others && !OwnConnection(c) {
			c.Close()
			s.mu.Lock()
			s.refused++
			s.mu.Unlock()
			s.note("a connection from another account was closed")
			continue
		}
		go s.relay(c, name)
	}
}

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

func (s *service) relay(c net.Conn, name string) {
	defer c.Close()
	up, release, err := s.open(name, nil)
	if err != nil {
		return
	}
	defer release()
	defer up.Close()
	bothWays(c, up)
}

// bothWays: two streams joined, until both sides are through
func bothWays(a, b net.Conn) {
	var wg sync.WaitGroup
	pump := func(from, to net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(to, from)
		if t, ok := to.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		} else {
			_ = to.Close()
		}
	}
	wg.Add(2)
	go pump(a, b)
	go pump(b, a)
	wg.Wait()
}

// ── its probes ───────────────────────────────────────────────────────────────

// prober is the Python that answers a service's probes that are functions.
type prober struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	mu  sync.Mutex
}

func (s *service) function(what string) (bool, bool) {
	if s.ask == nil {
		if len(s.decl.Python) == 0 {
			return false, false
		}
		cmd := exec.Command(s.decl.Python[0], "-m", "errand.probes", s.spec.Name, s.spec.From)
		cmd.Env, cmd.Stderr = s.decl.Environ, s.log
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		if cmd.Start() != nil {
			return false, false
		}
		s.ask = &prober{cmd: cmd, in: in, out: bufio.NewReader(out)}
	}
	p := s.ask
	p.mu.Lock()
	defer p.mu.Unlock()
	s.mu.Lock()
	b, _ := json.Marshal(map[string]any{"what": what, "ports": s.server})
	s.mu.Unlock()
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		s.ask = nil
		return false, false
	}
	line, err := p.out.ReadString('\n')
	if err != nil {
		s.ask = nil
		return false, false
	}
	switch strings.TrimSpace(line) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// probe: a question asked of the server -> its answer; false when it could not be asked
func (s *service) probe(what string, how any) (bool, bool) {
	type got struct{ yes, ok bool }
	answer := make(chan got, 1)
	go func() {
		var g got
		switch h := how.(type) {
		case string:
			s.mu.Lock()
			line, err := svc.FillPorts(h, s.server)
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
		case map[string]any:
			if path, ok := h["http_json"].(string); ok {
				s.mu.Lock()
				port := 0
				if name := kit.Text(h["port"]); name != "" {
					port = s.server[name]
				} else if len(s.spec.Ports) > 0 {
					port = s.server[s.spec.Ports[0].Name]
				}
				s.mu.Unlock()
				client := http.Client{Timeout: probeTimeout}
				if r, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path)); err == nil {
					var v any
					err = json.NewDecoder(r.Body).Decode(&v)
					r.Body.Close()
					g = got{caches.Truthy(v), err == nil || errors.Is(err, io.EOF)}
				}
			} else {
				g.yes, g.ok = s.function(what)
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

func (a *Place) serviceEnviron() []string { return os.Environ() }

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
		for _, l := range s.listeners {
			l.Close()
		}
		s.ended, s.pid = true, 0
		s.mu.Unlock()
		if s.ask != nil {
			_ = s.ask.in.Close()
			_ = s.ask.cmd.Process.Kill()
			_ = s.ask.cmd.Wait()
		}
		state := "stopped"
		if strings.HasPrefix(why, "failed") {
			state = "failed"
		}
		s.note("%s", why)
		_ = os.Remove(filepath.Join(s.dir, "server.json")) // ( before it is said: it rests from then on )
		close(s.done)
		s.say(state, why)
		s.log.Close()
	}()
	if err := s.listen(); err != nil {
		why = "failed: " + err.Error()
		return
	}
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
			} else if time.Since(idle).Seconds() > s.spec.Idle {
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
	taken := map[int]bool{}
	for _, p := range s.announced {
		taken[p] = true
	}
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
	for k, v := range s.spec.Env {
		v, _ = svc.FillPorts(v, s.server)
		env = append(env, k+"="+v)
	}
	var argv []string
	for _, w := range s.spec.Command {
		w, _ = svc.FillPorts(w, s.server)
		argv = append(argv, w)
	}
	full := append(s.a.serviceEnviron(), env...)
	home, _ := os.UserHomeDir()
	cwd := s.spec.Cwd
	if cwd == "" {
		cwd = home
	} else if strings.HasPrefix(cwd, "~/") {
		cwd = filepath.Join(home, cwd[2:])
	}
	path, err := kit.LookIn(argv[0], full, cwd)
	if err != nil {
		return fmt.Sprintf("failed: %s: %v", argv[0], err)
	}
	s.note("starting %s  ( %v -> %v )", strings.Join(argv, " "), s.announced, s.server)
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
		for _, step := range []struct {
			sig  syscall.Signal
			wait time.Duration
		}{{syscall.SIGINT, 2 * time.Second}, {syscall.SIGTERM, time.Duration(max(s.spec.Grace-2, 1) * float64(time.Second))}, {syscall.SIGKILL, 5 * time.Second}} {
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
		case time.Since(idle).Seconds() > s.spec.Idle:
			return fmt.Sprintf("stopped: unused for %s", reach.Span(s.spec.Idle))
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
	files, _ := filepath.Glob(filepath.Join(kit.LocalCache(), "serving", "*", "server.json"))
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

// holder: the agent of this machine that holds a service -- this one ( nil link ), another, or none
func (a *Place) holder(name string) (*service, string) {
	a.mu.Lock()
	s := a.services[name]
	a.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		if !ended {
			return s, ""
		}
	}
	for _, dir := range reach.Others(a.Root, a.Dir) {
		l, err := reach.LinkToAgent(dir)
		if err != nil {
			continue
		}
		got, err := l.Ask(map[string]any{"op": "own"})
		l.Close()
		var o reach.OverviewDoc
		if err != nil || reach.DecodeInto(got["own"], &o) != nil {
			continue
		}
		for _, r := range o.Serving {
			if r.Name == name {
				return nil, dir
			}
		}
	}
	return nil, ""
}

// wanted: the service asked for -- the one that runs, else started from what is declared, or from what was said
func (a *Place) wantedService(req map[string]any) (*service, string, error) {
	name := kit.Text(req["name"])
	if s, other := a.holder(name); s != nil || other != "" {
		return s, other, nil
	}
	var spec svc.ServiceSpec
	var decl svc.Declarations
	if req["spec"] != nil { // ad hoc
		if err := reach.DecodeInto(req["spec"], &spec); err != nil {
			return nil, "", err
		}
		spec.Source = "ad hoc"
	} else {
		var err error
		if decl, err = svc.DeclaredServices(kit.Text(req["cwd"]), kit.Strs(req["environ"])); err != nil {
			return nil, "", err
		}
		var ok bool
		if spec, ok = decl.Services[name]; !ok {
			names := make([]string, 0, len(decl.Services))
			for n := range decl.Services {
				names = append(names, n)
			}
			sort.Strings(names)
			why := fmt.Sprintf("no service %q is declared on %s ( declared: %s )", name, queue.Host(), strings.Join(names, ", "))
			for _, s := range decl.Said {
				why += "\n" + s
			}
			return nil, "", errors.New(why)
		}
		if bad := svc.WrongVersion(name, spec.Provider); bad != "" {
			return nil, "", fmt.Errorf("%s: %s", name, bad)
		}
	}
	if spec.Idle == 0 {
		spec.Idle = 600
	}
	if spec.Grace == 0 {
		spec.Grace = 10
	}
	s, err := a.keepService(spec, decl)
	return s, "", err
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
	case "service": // started if need be, and said until it answers
		s, other, err := a.wantedService(req)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		if other != "" {
			a.pass(other, req, f)
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
		s, other := a.holder(name)
		switch {
		case other != "":
			a.pass(other, req, f)
		case s == nil:
			f.Reply(map[string]any{"ok": true, "was": "stopped"})
		default:
			s.stop()
			select {
			case <-s.done:
			case <-time.After(time.Duration((s.spec.Grace + 8) * float64(time.Second))):
			}
			f.Reply(map[string]any{"ok": true, "stopped": true})
		}
	case "service_list": // what runs, and what is declared and does not
		out := []map[string]any{}
		seen := map[string]bool{}
		doc := overview(a)
		for _, r := range doc.Serving {
			seen[r.Name] = true
			out = append(out, map[string]any{"name": r.Name, "state": r.State, "detail": r.More})
		}
		decl, err := svc.DeclaredServices(kit.Text(req["cwd"]), kit.Strs(req["environ"]))
		said := decl.Said
		if err != nil {
			said = append(said, kit.FirstLine(err.Error()))
		}
		names := make([]string, 0, len(decl.Services))
		for n := range decl.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if !seen[n] {
				out = append(out, map[string]any{"name": n, "state": "declared", "detail": decl.Services[n].Source})
			}
		}
		f.Reply(map[string]any{"ok": true, "services": out, "said": said})
	case "service_connect": // a connection to one of its ports, on this channel
		s, other, err := a.wantedService(req)
		if err != nil {
			f.Fail("%v", err)
			return
		}
		if other != "" {
			a.pass(other, req, f)
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
