// Leases ( ADR 0040 ): a request for a server is given ports, and until when. Until then they are listened on, by this
// agent, and lead to that server or to nothing. A connection that finds it stopped starts it, and waits.
//
// A lease is on disk: the next agent of this machine opens the same ports, be it of another version -- the one before closes
// its listeners, and keeps what it accepted. A number stays its definition's a while after the lease is over.

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/svc"
	"github.com/errand-run/errand/internal/units"
)

const (
	leaseKeep    = 86400.0      // seconds a port stays once it was last used, if nothing is said
	sharedLease  = 2 * 3600.0   // and at most, on a machine shared with others
	numberKept   = 14 * 86400.0 // a number stays its definition's that long after its last use
	firstNumber  = 20000        // outside what a system gives to whoever asks for any port: 32768 and up on Linux, 49152 elsewhere
	numbersCount = 10000
	leaseSaved   = 30 * time.Second // what a use changes of it is written that often
	failedFor    = time.Minute      // a server that does not start is tried again after two seconds, then twice as long, up to this
)

// leaseRecord is a lease as it is written.
type leaseRecord struct {
	ID       string          `json:"id"`                // of the port: the definition, and what concerns its connections
	Config   string          `json:"config"`            // of the definition
	Text     string          `json:"text"`              // ... of what is written of it alone
	Host     *reach.HostSpec `json:"host,omitempty"`    // the machine it runs on; nil: this one
	Machine  string          `json:"machine,omitempty"` // ... as it was called
	Spec     svc.ServiceSpec `json:"spec"`
	Ports    map[string]int  `json:"ports"`
	Until    float64         `json:"until"`
	Last     float64         `json:"last"` // asked for, or used
	Taken    float64         `json:"taken"`
	Released bool            `json:"released,omitempty"`
}

func (r leaseRecord) over(now float64) bool { return r.Released || now > r.Until }

func (r leaseRecord) label() string { return r.Spec.Name }

// lease is one this agent listens for. What it says is under its agent's `leaseMu`.
type lease struct {
	a         *Place
	rec       leaseRecord
	listeners []net.Listener
	conns     int
	refused   int
	failed    string   // why its last connection led to nothing
	environ   []string // what the machine it runs on is reached with
	savedAt   time.Time
}

type failure struct {
	why string
	n   int
	at  time.Time
}

func (f failure) left() time.Duration {
	return min(time.Duration(1<<min(f.n, 10))*time.Second, failedFor) - time.Since(f.at)
}

func leasesDir() string { return filepath.Join(kit.LocalCache(), "leases", queue.Host()) }

func leaseFile(id string) string { return filepath.Join(leasesDir(), id+".json") }

// leasesWritten: every lease of this machine, those that are over too -- their numbers are kept
func leasesWritten() []leaseRecord {
	var out []leaseRecord
	files, _ := filepath.Glob(filepath.Join(leasesDir(), "*.json"))
	now := kit.Seconds(time.Now())
	for _, f := range files {
		var r leaseRecord
		data, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(data, &r) != nil || r.ID == "" {
			continue
		}
		if r.over(now) && now-max(r.Last, r.Until) > numberKept {
			_ = os.Remove(f)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Taken < out[k].Taken })
	return out
}

func (l *lease) save() error {
	if err := os.MkdirAll(leasesDir(), 0o700); err != nil {
		return err
	}
	l.savedAt = time.Now()
	b, _ := json.MarshalIndent(l.rec, "", " ")
	tmp := leaseFile(l.rec.ID) + fmt.Sprintf(".%d.tmp", os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil { // ( its variables may hold a secret )
		return err
	}
	return os.Rename(tmp, leaseFile(l.rec.ID))
}

// lookedAt is what a machine says of a server it is to run.
type lookedAt struct {
	Config string `json:"config"`
	State  string `json:"state"` // of the one that runs; "rests"
	Why    string `json:"why,omitempty"`
	Host   string `json:"host"`
}

// lookAtServer: a server of this machine's, before it is started: what it is, whether it can be, how it is
func (a *Place) lookAtServer(spec svc.ServiceSpec) (lookedAt, error) {
	out := lookedAt{State: "rests", Host: queue.Host()}
	if err := spec.Check(); err != nil {
		return out, err
	}
	var err error
	if out.Config, err = spec.Hash(); err != nil {
		return out, err
	}
	_, stamp, err := spec.Program(a.serviceEnviron())
	if err != nil {
		return out, err
	}
	if why := spec.TooMuch(queue.Machine()); why != "" {
		return out, errors.New(why)
	}
	for _, s := range overview(a).Serving {
		if s.Config == out.Config {
			out.State = s.State
			return out, nil
		}
	}
	if f, ok := a.failureOf(out.Config + "." + stamp); ok {
		out.State, out.Why = "failed", f.why
	}
	return out, nil
}

// askFar: a question to another machine's agent, by the link that is kept to it
func (a *Place) askFar(h *reach.HostSpec, environ []string, req map[string]any) (map[string]any, error) {
	l, err := farLink(h, environ)
	if err != nil {
		return nil, err
	}
	return l.Ask(req)
}

func farLink(h *reach.HostSpec, environ []string) (*reach.Link, error) {
	if len(environ) == 0 {
		environ = os.Environ()
	}
	return reach.LinkTo(&reach.Term{Environ: environ, Emit: func(byte, []byte) {}}, h)
}

// farLeases: those of servers that run elsewhere
func (a *Place) farLeases() []*lease {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	var out []*lease
	for _, l := range a.leases {
		if l.rec.Host != nil {
			out = append(out, l)
		}
	}
	return out
}

// takeLease: the lease of what is asked for -- the one that runs, for longer, or a new one -- and how its server is. `environ`: what
// the machine it runs on is reached with.
func (a *Place) takeLease(spec svc.ServiceSpec, environ []string) (leaseRecord, lookedAt, error) {
	var none leaseRecord
	if err := spec.Check(); err != nil {
		return none, lookedAt{}, err
	}
	if spec.Keep <= 0 {
		spec.Keep = leaseKeep
	}
	if sharedHere() {
		spec.Keep = min(spec.Keep, sharedLease)
		if spec.Listen != "" {
			return none, lookedAt{}, errors.New("listen: this machine is shared with others: its ports are listened on at 127.0.0.1 only")
		}
	}
	if spec.Always {
		if why := units.Refused(); why != "" {
			return none, lookedAt{}, errors.New("always: " + why)
		}
	}
	machine, there, err := spec.Where()
	if err != nil {
		return none, lookedAt{}, err
	}
	var host *reach.HostSpec
	var said lookedAt
	name, text := "", spec.Text()
	if machine == nil {
		if said, err = a.lookAtServer(spec); err != nil {
			return none, said, err
		}
	} else {
		h := reach.HostOf(machine)
		host, name = &h, h.Host
		now := kit.Seconds(time.Now())
		var got map[string]any
		if got, err = a.askFar(host, environ, map[string]any{"op": "service_look", "spec": there,
			"hold": hold{ID: farHold(text), Idle: spec.Idle, Grace: spec.Grace, Until: now + spec.Keep}}); err == nil {
			err = reach.DecodeInto(got["look"], &said)
		}
		if err != nil {
			a.leaseMu.Lock()
			for _, l := range a.leases { // the port stays what it is: the machine may come back
				if l.rec.Text == text {
					said = lookedAt{Config: l.rec.Config, State: "away", Why: err.Error()}
				}
			}
			a.leaseMu.Unlock()
			if said.Config == "" {
				return none, said, err // ( which says which machine )
			}
		}
	}
	def := said.Config
	id := spec.PortHash(def)
	if host != nil {
		id = svc.Short(host.Key() + " " + id)
	}
	a.leaseMu.Lock()
	held := a.leases[id] != nil

	a.leaseMu.Unlock()
	if !held {
		a.askYield(id)
	}
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	now := kit.Seconds(time.Now())
	l := a.leases[id]
	if l == nil {
		rec := leaseRecord{ID: id, Config: def, Text: text, Host: host, Machine: name, Spec: spec, Taken: now}
		for _, r := range leasesWritten() {
			if r.ID != id {
				continue
			}
			rec.Ports = r.Ports
			if !r.over(now) { // ( another agent's, that went )
				rec = r
			}
		}
		if l, err = a.openLease(rec); err != nil {
			return none, said, err
		}
	}
	was := &l.rec.Spec // while it runs, the longest of what was asked
	was.Idle, was.Grace, was.Keep = max(was.Idle, spec.Idle), max(was.Grace, spec.Grace), max(was.Keep, spec.Keep)
	l.rec.Last, l.rec.Until = now, max(l.rec.Until, now+spec.Keep)
	if len(environ) > 0 {
		l.environ = environ
	}
	if err := l.save(); err != nil {
		return none, said, err
	}
	a.touched()
	return l.rec, said, nil
}

// farHold: how a lease of here is known to the machine its server runs on
func farHold(text string) string { return queue.Host() + ":" + text }

// askYield: the other agents of this machine close what they listen on for a lease ( "": for any ), which this one takes
func (a *Place) askYield(id string) {
	for _, dir := range reach.Machines(a.Root, a.Dir) {
		_, _ = reach.AskAgent(dir, map[string]any{"op": "lease_yield", "id": id})
	}
}

// openLease: its ports listened on -- the numbers it has, else some that nobody has; by the system, for one that is always
// listened on. Under `leaseMu`.
func (a *Place) openLease(rec leaseRecord) (*lease, error) {
	l := &lease{a: a, rec: rec}
	taken := map[int]string{}
	for _, r := range leasesWritten() {
		if r.ID != rec.ID {
			for _, n := range r.Ports {
				taken[n] = r.label()
			}
		}
	}
	ports := map[string]int{}
	var names []string
	for _, p := range rec.Spec.Ports {
		var n int
		if rec.Spec.Always {
			var err error
			if n, err = keepFor(rec, p, taken); err != nil {
				l.close()
				l.rec.unkeep()
				return nil, err
			}
		} else {
			lis, err := listenFor(rec.Spec.Listen, rec.ID, p, rec.Ports[p.Name], taken)
			if err != nil {
				l.close()
				return nil, err
			}
			n = lis.Addr().(*net.TCPAddr).Port
			l.listeners, names = append(l.listeners, lis), append(names, p.Name)
		}
		ports[p.Name], taken[n] = n, rec.label()
	}
	l.rec.Ports = ports
	if a.leases == nil {
		a.leases = map[string]*lease{}
	}
	a.leases[rec.ID] = l
	a.leaseOnce.Do(func() { go a.leaseRounds() })
	for i, lis := range l.listeners {
		go l.accept(lis, names[i])
	}
	return l, nil
}

func listenFor(addr, id string, p svc.ServicePort, had int, taken map[int]string) (net.Listener, error) {
	if addr == "" {
		addr = "127.0.0.1"
	}
	at := func(n int) (net.Listener, error) { return net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(n))) }
	if n := p.Announced; n > 0 { // that one, and no other
		lis, err := at(n)
		if err == nil {
			return lis, nil
		}
		who := "another program"
		if taken[n] != "" {
			who = "the service " + taken[n]
		}
		return nil, fmt.Errorf("port %s: %d is taken by %s ( %v )", p.Name, n, who, err)
	}
	if had > 0 && taken[had] == "" {
		if lis, err := at(had); err == nil {
			return lis, nil
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id + p.Name))
	page := runinfo.PortOfUser()
	for i, from := 0, int(h.Sum32()%numbersCount); i < numbersCount; i++ {
		n := firstNumber + (from+i)%numbersCount
		if taken[n] != "" || (n >= page && n < page+4) { // ( the page's own )
			continue
		}
		if lis, err := at(n); err == nil {
			return lis, nil
		}
	}
	return nil, fmt.Errorf("port %s: none is free between %d and %d", p.Name, firstNumber, firstNumber+numbersCount-1)
}

// ── always: the system listens in errand's stead ( ADR 0028 §6 ) ─────────────

func keptLabel(id, port string) string { return "dev.errand.lease." + id + "." + port }

// keepFor: a port of a lease the system listens on, and starts `errand serve kept-lease` for at a connection -- the number it
// has, else one found as any other
func keepFor(rec leaseRecord, p svc.ServicePort, taken map[int]string) (int, error) {
	label := keptLabel(rec.ID, p.Name)
	if n := units.KeptPort(label); n > 0 {
		return n, nil
	}
	lis, err := listenFor(rec.Spec.Listen, rec.ID, p, rec.Ports[p.Name], taken)
	if err != nil {
		return 0, err
	}
	n := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	self := reach.TypedPath(os.Getenv) // ( the program as it was installed: it outlives an update )
	if self == "" {
		self = kit.SelfPath()
	}
	if err := units.KeepPort(label, []string{self, "serve", "kept-lease", rec.ID, p.Name}, rec.Spec.Listen, n); err != nil {
		return 0, fmt.Errorf("always: %v", err)
	}
	return n, nil
}

// unkeep: the system listens for it no more
func (r leaseRecord) unkeep() {
	if !r.Spec.Always {
		return
	}
	for _, p := range r.Spec.Ports {
		if units.KeptPort(keptLabel(r.ID, p.Name)) > 0 {
			units.UnkeepPort(keptLabel(r.ID, p.Name))
		}
	}
}

// KeptLease: what the system starts at a connection to a port it keeps for a lease -- each connection relayed to this machine's
// agent, started if there is none, until none came for a while. A lease that is over is let go of, by the system too.
func KeptLease(id, port string) int {
	lis, err := units.InheritedListener()
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand serve kept-lease: started by hand? the system hands this its socket ( errand serve --always )")
		return 2
	}
	t := &reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}
	var mu sync.Mutex
	units.ServeKept(lis, 5*time.Minute, func(c net.Conn) {
		defer c.Close()
		rec, ok := leaseWritten(id)
		if !ok || rec.over(kit.Seconds(time.Now())) {
			c.Close()
			if ok {
				rec.unkeep() // ( this program with it )
			}
			return
		}
		if !rec.Spec.Others && !OwnConnection(c) {
			return
		}
		mu.Lock()
		link, err := reach.LinkTo(t, nil)
		mu.Unlock()
		if err != nil {
			fmt.Fprintln(os.Stderr, "errand serve kept-lease:", err)
			return
		}
		if err := relayOver(link, map[string]any{"op": "lease_connect", "id": id, "port": port}, c, 0); err != nil {
			fmt.Fprintln(os.Stderr, "errand serve kept-lease:", err)
		}
	})
	return 0
}

// leaseWritten: a lease as it is on disk
func leaseWritten(id string) (leaseRecord, bool) {
	for _, r := range leasesWritten() {
		if r.ID == id {
			return r, true
		}
	}
	return leaseRecord{}, false
}

// leaseConnect: a connection the system accepted for a lease ( KeptLease ), on this channel
func (a *Place) leaseConnect(req map[string]any, f *reach.FarChan) {
	id, port := kit.Text(req["id"]), kit.Text(req["port"])
	a.leaseMu.Lock()
	held := a.leases[id] != nil
	a.leaseMu.Unlock()
	if !held {
		a.askYield(id)
	}
	a.leaseMu.Lock()
	l := a.leases[id]
	if l == nil {
		if r, ok := leaseWritten(id); ok && r.Spec.Always && !r.over(kit.Seconds(time.Now())) {
			l, _ = a.openLease(r)
		}
	}
	a.leaseMu.Unlock()
	if l == nil {
		f.Fail("no lease %s here", id)
		return
	}
	known := false
	for _, p := range l.rec.Spec.Ports {
		known = known || p.Name == port
	}
	if !known {
		f.Fail("lease %s: no port %q", id, port)
		return
	}
	a.touched()
	f.Reply(map[string]any{"ok": true})
	l.relay(newChanConn(f), port)
}

// chanConn: a link's channel, as a connection
type chanConn struct {
	f    *reach.FarChan
	in   <-chan []byte
	rest []byte
	once sync.Once
}

func newChanConn(f *reach.FarChan) *chanConn { return &chanConn{f: f, in: keptInput(f)} }

func (c *chanConn) Read(b []byte) (int, error) {
	for len(c.rest) == 0 {
		select {
		case data, more := <-c.in:
			if !more {
				return 0, io.EOF
			}
			c.rest = data
		case <-c.f.Gone:
			return 0, io.EOF
		}
	}
	n := copy(b, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *chanConn) Write(b []byte) (int, error) {
	select {
	case <-c.f.Gone:
		return 0, io.ErrClosedPipe
	default:
	}
	c.f.Out(kit.OUT, append([]byte{}, b...))
	return len(b), nil
}

func (c *chanConn) CloseWrite() error {
	c.once.Do(func() { c.f.Out(kit.EXIT, []byte(`{"code":0}`)) })
	return nil
}

func (c *chanConn) Close() error                     { return c.CloseWrite() }
func (c *chanConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *chanConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *chanConn) SetDeadline(time.Time) error      { return nil }
func (c *chanConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chanConn) SetWriteDeadline(time.Time) error { return nil }

// close: nothing listens for it any more; what was accepted stays
func (l *lease) close() {
	for _, lis := range l.listeners {
		_ = lis.Close()
	}
}

// yieldLeases: another agent of this machine listens for it from here on
func (a *Place) yieldLeases(id string) {
	a.leaseMu.Lock()
	for k, l := range a.leases {
		if id == "" || id == k {
			l.close()
			_ = l.save()
			delete(a.leases, k)
		}
	}
	a.leaseMu.Unlock()
	a.touched()
}

// restoreLeases: those that run, taken over by this agent, which is the one asked from here on
func (a *Place) restoreLeases() {
	a.askYield("")
	a.leaseMu.Lock()
	now := kit.Seconds(time.Now())
	var over []leaseRecord
	for _, r := range leasesWritten() {
		if r.over(now) {
			over = append(over, r)
		}
		if r.over(now) || a.leases[r.ID] != nil {
			continue
		}
		if l, err := a.openLease(r); err == nil { // ( a number taken meanwhile: said to whoever asks again )
			_ = l.save()
		}
	}
	a.leaseMu.Unlock()
	for _, r := range over { // ( kept by the system while no agent was there to let them go )
		r.unkeep()
	}
	a.touched()
}

// leaseRounds: a lease that is over is let go of
func (a *Place) leaseRounds() {
	for {
		time.Sleep(time.Second)
		now := kit.Seconds(time.Now())
		var gone []leaseRecord
		a.leaseMu.Lock()
		for id, l := range a.leases {
			if l.conns == 0 && now > l.rec.Until {
				l.close()
				_ = l.save()
				delete(a.leases, id)
				gone = append(gone, l.rec)
			}
		}
		a.leaseMu.Unlock()
		for _, r := range gone {
			r.unkeep()
		}
		if len(gone) > 0 {
			a.touched()
		}
	}
}

// releaseLease: given back -> whether this agent had it
func (a *Place) releaseLease(id string) bool {
	a.leaseMu.Lock()
	l := a.leases[id]
	if l != nil {
		l.close()
		l.rec.Released, l.rec.Last = true, kit.Seconds(time.Now())
		_ = l.save()
		delete(a.leases, id)
	}
	a.leaseMu.Unlock()
	if l != nil {
		l.rec.unkeep()
		a.touched()
	}
	return l != nil
}

func (l *lease) accept(lis net.Listener, name string) {
	for {
		c, err := lis.Accept()
		if err != nil {
			return
		}
		if !l.rec.Spec.Others && !OwnConnection(c) {
			c.Close()
			l.a.leaseMu.Lock()
			l.refused++
			l.a.leaseMu.Unlock()
			continue
		}
		go l.relay(c, name)
	}
}

// used: a connection more, or less: what it has left starts again
func (l *lease) used(by int) {
	a := l.a
	a.leaseMu.Lock()
	l.conns += by
	now := kit.Seconds(time.Now())
	l.rec.Last, l.rec.Until = now, max(l.rec.Until, now+l.rec.Spec.Keep)
	if a.leases[l.rec.ID] == l && time.Since(l.savedAt) > leaseSaved { // ( else another agent's to write )
		_ = l.save()
	}
	a.leaseMu.Unlock()
	a.touched()
}

func (l *lease) relay(c net.Conn, name string) {
	defer c.Close()
	l.used(1)
	defer l.used(-1)
	l.a.leaseMu.Lock()
	rec, environ := l.rec, l.environ
	l.a.leaseMu.Unlock()
	if rec.Host != nil {
		_, there, _ := rec.Spec.Where()
		link, err := farLink(rec.Host, environ)
		if err == nil {
			h := rec.hold()
			h.ID = farHold(rec.Text)
			err = relayOver(link, map[string]any{"op": "service_connect", "spec": there, "config": rec.Config, "port": name, "hold": h}, c, rec.holdEnd())
		}
		l.led(err)
		return
	}
	s, other, err := l.a.serverFor(rec.Spec, rec.Config, false)
	l.led(err)
	if err != nil {
		return
	}
	if s == nil {
		if link, err := reach.LinkToAgent(other.dir); err == nil {
			_ = relayOver(link, map[string]any{"op": "service_connect", "key": other.key, "port": name, "hold": rec.hold()}, c, rec.holdEnd())
			link.Close()
		}
		return
	}
	up, release, err := s.open(name, nil)
	if err != nil {
		return
	}
	defer release()
	defer up.Close()
	joined(c, up, rec.holdEnd())
}

func (r leaseRecord) holdEnd() time.Duration {
	return time.Duration(r.Spec.HoldEnd * float64(time.Second))
}

// hold is what a request asks of how a server is kept, whichever agent has it.
type hold struct {
	ID    string  `json:"id"`
	Idle  float64 `json:"idle"`
	Grace float64 `json:"grace"`
	Until float64 `json:"until"`
}

func (r leaseRecord) hold() hold {
	return hold{ID: r.ID, Idle: r.Spec.Idle, Grace: r.Spec.Grace, Until: r.Until}
}

// holdsOf: what the leases of this agent ask of a server
func (a *Place) holdsOf(config string) []hold {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	var out []hold
	for _, l := range a.leases {
		if l.rec.Config == config {
			out = append(out, l.rec.hold())
		}
	}
	return out
}

// led: whether its last connection led to its server
func (l *lease) led(err error) {
	why := ""
	if err != nil {
		why = kit.FirstLine(err.Error())
	}
	l.a.leaseMu.Lock()
	differs := l.failed != why
	l.failed = why
	l.a.leaseMu.Unlock()
	if differs {
		l.a.touched()
	}
}

// relayOver: a connection, carried to a server another agent holds; the end of its input held back `hold` ( hold_end )
func relayOver(l *reach.Link, req map[string]any, conn net.Conn, hold time.Duration) error {
	c := l.Open(req)
	defer c.Close()
	if _, err := c.Reply(); err != nil {
		return err
	}
	ended, answers := make(chan struct{}), &kit.Exchange{}
	defer close(ended)
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				answers.Asked()
				if c.Send(kit.IN, append([]byte{}, buf[:n]...)) != nil {
					return
				}
			}
			if err != nil {
				if hold > 0 && !answers.Over(hold, ended) {
					return
				}
				_ = c.Send(kit.EOF, nil)
				return
			}
		}
	}()
	for {
		kind, payload, err := c.Recv()
		if err != nil || kind == kit.EXIT {
			return nil
		}
		if kind == kit.OUT {
			answers.Answered()
			if _, err := conn.Write(payload); err != nil {
				return nil
			}
		}
	}
}

// joined: a client and its server, until both sides are through; the end of the client's input held back `hold` ( hold_end )
func joined(client, server net.Conn, hold time.Duration) {
	if hold <= 0 {
		bothWays(client, server)
		return
	}
	answers, ended := &kit.Exchange{}, make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 64<<10)
		for {
			n, err := client.Read(buf)
			if n > 0 {
				answers.Asked()
				if _, werr := server.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
		if answers.Over(hold, ended) {
			closeWrite(server)
		}
	}()
	go func() {
		defer wg.Done()
		defer close(ended)
		buf := make([]byte, 64<<10)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				answers.Answered()
				if _, werr := client.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		closeWrite(client)
	}()
	wg.Wait()
}

// ── what is said of them ─────────────────────────────────────────────────────

func (a *Place) leaseRows() []reach.LeaseRow {
	a.leaseMu.Lock()
	defer a.leaseMu.Unlock()
	out := []reach.LeaseRow{}
	for _, l := range a.leases {
		r := l.rec
		row := reach.LeaseRow{ID: r.ID, Name: r.label(), Config: r.Config, Until: r.Until, Keep: r.Spec.Keep, Taken: r.Taken, Users: l.conns, Refused: l.refused,
			Command: r.Spec.Command, Vars: r.Spec.ShownVars(), Needs: r.Spec.NeedsText(), Idle: r.Spec.Idle, Cwd: r.Spec.Dir(), Others: r.Spec.Others, Source: r.Spec.Source, On: r.Machine, Layers: layersOf(r.Spec), Failed: l.failed,
			Listen: r.Spec.Listen, Always: r.Spec.Always, HoldEnd: r.Spec.HoldEnd}
		for _, p := range r.Spec.Ports {
			row.Ports = append(row.Ports, reach.ServingPort{Name: p.Name, Port: r.Ports[p.Name]})
		}
		if r.Host == nil && row.Failed == "" {
			if _, stamp, err := r.Spec.Program(a.serviceEnviron()); err == nil {
				if f, ok := a.failureOf(r.Config + "." + stamp); ok {
					row.Failed = f.why
				}
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Name != out[k].Name {
			return out[i].Name < out[k].Name
		}
		return out[i].ID < out[k].ID
	})
	return out
}

// called: whether a word names a server, or a lease: its label, its hash, or both ( `ollama~3f2a` )
func called(word, label, config string) bool {
	if name, hash, ok := strings.Cut(word, "~"); ok {
		return name == label && hash != "" && strings.HasPrefix(config, hash)
	}
	return word == label || (len(word) >= 6 && strings.HasPrefix(config, word))
}

// leaseCalled: the one lease a word names, whichever agent of this machine listens for it
func (a *Place) leaseCalled(word string) (leaseRecord, bool) {
	var got leaseRecord
	n, now := 0, kit.Seconds(time.Now())
	for _, r := range leasesWritten() {
		if !r.over(now) && called(word, r.label(), r.Config) {
			if n == 0 || r.Config != got.Config {
				n++
			}
			got = r
		}
	}
	return got, n == 1
}

func layersOf(s svc.ServiceSpec) string {
	_, rest, err := s.Where()
	if err != nil || len(rest.On) == 0 {
		return ""
	}
	return layer.Describe(rest.On)
}

// leaseSaid: a lease, as it is answered to whoever asked for it
func (a *Place) leaseSaid(r leaseRecord, said lookedAt) map[string]any {
	ports := []map[string]any{}
	for _, p := range r.Spec.Ports {
		ports = append(ports, map[string]any{"name": p.Name, "port": r.Ports[p.Name]})
	}
	out := map[string]any{"id": r.ID, "label": r.label(), "config": r.Config, "ports": ports, "until": r.Until, "again_in": (r.Until - kit.Seconds(time.Now())) / 2,
		"state": said.State, "host": said.Host, "address": reach.ReachedAt(r.Spec.Listen)}
	if r.Host != nil {
		out["on"] = r.Machine
	}
	if said.Why != "" {
		out["why"] = said.Why
	}
	return out
}
