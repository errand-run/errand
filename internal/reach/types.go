// What the programs of errand say to each other of what runs where ( agent/overview.go, ledger.go ): the agent writes it, the
// command line, the screen and the page read it.

package reach

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/system"
)

// TableRow is one run an agent holds.
type TableRow struct {
	ID      string  `json:"id"`
	What    string  `json:"what"`
	Env     string  `json:"env"`
	Project string  `json:"project"`
	State   string  `json:"state"` // running | waiting | ended
	More    string  `json:"more"`  // what it waits for; how it ended
	Started float64 `json:"started"`

	Since float64         `json:"since,omitempty"` // when it began to wait
	Leaf  string          `json:"leaf,omitempty"`
	Step  *draw.StepState `json:"step,omitempty"` // where it says it is
	Batch bool            `json:"batch,omitempty"`
	Slurm string          `json:"slurm,omitempty"`
	Pid   int             `json:"pid,omitempty"`
	File  string          `json:"file,omitempty"`
	Sel   string          `json:"sel,omitempty"`
	Aside bool            `json:"aside,omitempty"` // errand's own: a listing, a shell
	Out   int64           `json:"out,omitempty"`   // how much it wrote
	Marks int             `json:"marks,omitempty"` // how many times its directory changed
}

// AgentRow is one agent of a machine.
type AgentRow struct {
	Dir     string  `json:"dir"`
	Kind    string  `json:"kind"` // "" the machine's, container, allocation
	Release string  `json:"release"`
	Errand  string  `json:"errand"`
	Pid     int     `json:"pid"`
	Started float64 `json:"started"`
	Idle    float64 `json:"idle"` // seconds since it did anything
	Keep    float64 `json:"keep"`
	Running int     `json:"running"`
	Retired bool    `json:"retired,omitempty"`
	RSS     float64 `json:"rss,omitempty"` // MB
}

// KeptRow is something kept between commands, or that errand sees and does not hold.
type KeptRow struct {
	Kind string `json:"kind"` // allocation | vm | warm | unowned
	ID   string `json:"id,omitempty"`
	Text string `json:"text"`
	Pid  int    `json:"pid,omitempty"`
}

type OverviewDoc struct {
	V          int          `json:"v"`
	Host       string       `json:"host"`
	User       string       `json:"user"`
	System     string       `json:"system"`
	Now        float64      `json:"now"`
	Cache      string       `json:"cache"`
	Rows       []TableRow   `json:"rows"`
	Agents     []AgentRow   `json:"agents"`
	Kept       []KeptRow    `json:"kept"`
	Serving    []ServingRow `json:"serving"`
	Leases     []LeaseRow   `json:"leases,omitempty"`
	Rested     []ServingRow `json:"rested,omitempty"`     // the services that ran here and do not now ( apart: a row of Serving is one that is held )
	Authorized []AuthRow    `json:"authorized,omitempty"` // what pages and keys may ask for here ( ADR 0040 )
	Refused    []RefusedRow `json:"refused,omitempty"`    // what they asked for lately, and were refused
	VMs        []VmRow      `json:"vms"`
	Shared     bool         `json:"shared,omitempty"` // errand leaves nothing there
}

// ServingRow is a service an agent keeps ( agent/serve.go ); vmRow, a machine it boots ( vm.go ).
type ServingRow struct {
	Name    string  `json:"name"`
	Env     string  `json:"env"`
	Project string  `json:"project"`
	State   string  `json:"state"` // starting | ready | parked | failed
	More    string  `json:"more,omitempty"`
	Port    int     `json:"port,omitempty"`
	Users   int     `json:"users"`
	Started float64 `json:"started"`
	Leaf    string  `json:"leaf,omitempty"`

	On      string            `json:"on,omitempty"`     // the layers it is started in
	Config  string            `json:"config,omitempty"` // the hash of its definition
	Key     string            `json:"key,omitempty"`    // ... and of the program found for it
	Vars    map[string]string `json:"vars,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Command []string          `json:"command,omitempty"`
	Source  string            `json:"source,omitempty"` // where it is declared
	Needs   string            `json:"needs,omitempty"`
	Ports   []ServingPort     `json:"ports,omitempty"`
	Pid     int               `json:"pid,omitempty"`   // of the server; 0: not started
	Agent   int               `json:"agent,omitempty"` // the pid of the agent that holds it
	Waiting int               `json:"waiting,omitempty"`
	Idle    float64           `json:"idle,omitempty"`
	Log     string            `json:"log,omitempty"`
	Ended   float64           `json:"ended,omitempty"` // one that rests: since when
}

// AuthRow is a server authorized to pages or to a key ( svc/authorized.go ).
type AuthRow struct {
	Hash       string            `json:"hash"`
	Label      string            `json:"label"`
	To         []string          `json:"to"`
	Page       string            `json:"page,omitempty"`
	Since      string            `json:"since"`
	Used       string            `json:"used,omitempty"`
	Command    []string          `json:"command"`
	Vars       map[string]string `json:"vars,omitempty"` // ( those that hold a secret aside )
	Needs      string            `json:"needs,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Idle       float64           `json:"idle,omitempty"`
	Keep       float64           `json:"keep,omitempty"`
	Unverified bool              `json:"unverified,omitempty"`
	Dev        string            `json:"dev,omitempty"`
	Error      string            `json:"error,omitempty"` // what cannot be read of it
}

// RefusedRow is something asked for here that was not authorized.
type RefusedRow struct {
	Who     string   `json:"who"`
	What    string   `json:"what"`
	Why     string   `json:"why"`
	From    string   `json:"from,omitempty"`
	Command []string `json:"command,omitempty"` // what would run, when it is known there
	At      float64  `json:"at"`
	Times   int      `json:"times"`
}

// LeaseRow is a lease an agent listens for ( agent/lease.go ).
type LeaseRow struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Config  string            `json:"config"` // of the server it leads to
	Ports   []ServingPort     `json:"ports"`
	Until   float64           `json:"until"`
	Keep    float64           `json:"keep"`
	Taken   float64           `json:"taken"`
	Users   int               `json:"users"`
	Refused int               `json:"refused,omitempty"` // connections of other accounts
	Failed  string            `json:"failed,omitempty"`  // why its server did not start, the last time
	Command []string          `json:"command"`
	Vars    map[string]string `json:"vars,omitempty"` // ( those that hold a secret aside )
	Needs   string            `json:"needs,omitempty"`
	Idle    float64           `json:"idle"`
	Cwd     string            `json:"cwd"`
	Others  bool              `json:"others,omitempty"`
	Source  string            `json:"source,omitempty"`
	On      string            `json:"on,omitempty"`     // the machine it leads to; "": this one
	Layers  string            `json:"layers,omitempty"` // what its server is started in
	Listen  string            `json:"listen,omitempty"` // the address its ports are listened on; "": 127.0.0.1
	Always  bool              `json:"always,omitempty"` // the system listens on them in errand's stead
	HoldEnd float64           `json:"hold_end,omitempty"`
}

// Address: where its ports are reached, from its machine
func (l LeaseRow) Address() string { return ReachedAt(l.Listen) }

// ReachedAt: where a port listened on at an address is reached from its machine -- 127.0.0.1 for one that is any
func ReachedAt(listen string) string {
	if ip := net.ParseIP(listen); ip == nil || ip.IsUnspecified() {
		return "127.0.0.1"
	}
	if strings.Contains(listen, ":") {
		return "[" + listen + "]"
	}
	return listen
}

// ServingPort is one port of a service: where clients come, and where the server is.
type ServingPort struct {
	Name   string `json:"name"`
	Port   int    `json:"port,omitempty"`
	Server int    `json:"server,omitempty"`
}

type VmRow struct {
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	State string  `json:"state"`
	Pid   int     `json:"pid,omitempty"`
	Since float64 `json:"since,omitempty"`
}

// HostSpec is how a machine is reached: what ssh is given.
type HostSpec struct {
	Host    string   `json:"host"`
	Options []string `json:"options,omitempty"`
	Windows bool     `json:"windows,omitempty"` // what answers is a Windows ( windows.go )
	VM      *VmWant  `json:"vm,omitempty"`      // a machine errand boots: `Host` is how it is called ( vm.go )
	Shared  *bool    `json:"shared,omitempty"`  // a machine errand leaves nothing on ( ADR 0028 ); nil: as it says
	Cache   string   `json:"cache,omitempty"`   // errand's directory there, as declared; "": as the machine says
	Booted  bool     `json:"-"`                 // errand boots it: its name leads to another tomorrow
	NoBoot  bool     `json:"-"`                 // a machine that is not up is left so
	// while it is being reached: the surroundings of whoever typed the command ( its ssh, its keys ), and where to say
	// what is done on the way
	Environ []string        `json:"-"`
	Say     func(string)    `json:"-"`
	Stop    <-chan struct{} `json:"-"` // closed: what is asked of it is left
}

func (h HostSpec) Key() string {
	what := h.Host + "\x00" + strings.Join(h.Options, "\x00")
	if h.Windows {
		what += "\x00windows"
	}
	if h.Cache != "" { // another directory of errand's is another place
		what += "\x00cache\x00" + h.Cache
	}
	if h.VM != nil {
		what += "\x00" + h.VM.Root + "\x00" + h.VM.Image
		if h.VM.Via != nil {
			what += "\x00" + h.VM.Via.Key()
		}
	}
	sum := sha256.Sum256([]byte(what))
	return hex.EncodeToString(sum[:])[:16]
}

// VmWant is a machine errand boots: where its image is, what it is given.
type VmWant struct {
	Root  string    `json:"root"`  // the project, on the machine that boots it
	Image string    `json:"image"` // its directory, from there
	Name  string    `json:"name"`
	Cpus  int       `json:"cpus"`
	Ram   string    `json:"ram"`
	Keep  string    `json:"keep"`
	Data  string    `json:"data,omitempty"`
	Keyed bool      `json:"keyed,omitempty"` // a licence: it does not run out
	Via   *HostSpec `json:"via,omitempty"`   // the machine that boots it; nil: this one
}

const (
	ledgerDays = 14
	dormantDay = 24 * time.Hour
)

// MachineNote is one way to a machine, and what was said of it.
type MachineNote struct {
	V        int      `json:"v"`
	Host     string   `json:"host"`
	Options  []string `json:"options,omitempty"`
	Windows  bool     `json:"windows,omitempty"`
	Cache    string   `json:"cache,omitempty"`    // where errand keeps its files there, when that was declared
	Hostname string   `json:"hostname,omitempty"` // as it names itself
	ID       string   `json:"id,omitempty"`       // what tells it from any other ( system.MachineID ); "": an errand that did not say it
	Label    string   `json:"label,omitempty"`
	First    float64  `json:"first"`
	Used     float64  `json:"used"`            // when work was sent there last
	Watch    string   `json:"watch,omitempty"` // "" while work is sent there; on: always; off: never
	NoGauges bool     `json:"no_gauges,omitempty"`
}

func (n MachineNote) Spec() HostSpec {
	return HostSpec{Host: n.Host, Options: n.Options, Windows: n.Windows, Cache: n.Cache}
}

func ledgerDir() string { return filepath.Join(kit.LocalCache(), "machines") }

func (n MachineNote) Path() string { return filepath.Join(ledgerDir(), n.Spec().Key()+".json") }

func (n MachineNote) Write() {
	if os.MkdirAll(ledgerDir(), 0o755) == nil {
		_ = kit.WriteRecord(n.Path(), n)
	}
}

// Machine is one machine asked, under all the names it is reached by.
type Machine struct {
	Name  string // its label, or the oldest name
	Notes []MachineNote
}

func (m Machine) Here() bool { return len(m.Notes) == 0 }

func (m Machine) Used() float64 {
	at := 0.0
	for _, n := range m.Notes {
		at = max(at, n.Used)
	}
	return at
}

func (m Machine) watch() string {
	for _, n := range m.Notes {
		if n.Watch != "" {
			return n.Watch
		}
	}
	return ""
}

func (m Machine) Gauges() bool {
	for _, n := range m.Notes {
		if n.NoGauges {
			return false
		}
	}
	return true
}

func (m Machine) Dormant() bool {
	switch m.watch() {
	case "on":
		return false
	case "off":
		return true
	}
	return !m.Here() && time.Since(time.Unix(int64(m.Used()), 0)) > dormantDay
}

// Spec: how it is reached -- the way used last
func (m Machine) Spec() *HostSpec {
	if m.Here() {
		return nil
	}
	best := m.Notes[0]
	for _, n := range m.Notes {
		if n.Used > best.Used {
			best = n
		}
	}
	h := best.Spec()
	return &h
}

func (m Machine) Named(word string) bool {
	if word == m.Name || (m.Here() && word == "here") {
		return true
	}
	for _, n := range m.Notes {
		if word == n.Host || word == n.Hostname || word == n.Label {
			return true
		}
	}
	return false
}

// CanBeNamed: whether `label` may name it -- not when another machine is known by that word: the ledger would take
// the two for one ( one label, one machine ), and a command naming it would not know which. "": its oldest name again.
func (m Machine) CanBeNamed(label string) error {
	if label == "" {
		return nil
	}
	for _, o := range Ledger(HereName()) {
		if o.Name != m.Name && o.Named(label) {
			return fmt.Errorf("%s is what %s is called: another name", label, o.Name)
		}
	}
	return nil
}

func afterUser(host string) string {
	if at := strings.LastIndex(host, "@"); at >= 0 {
		return host[at+1:]
	}
	return host
}

func ledgerNotes() []MachineNote {
	var out []MachineNote
	files, _ := filepath.Glob(filepath.Join(ledgerDir(), "*.json"))
	for _, f := range files {
		var n MachineNote
		data, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(data, &n) != nil || n.Host == "" {
			continue
		}
		if n.Watch == "" && time.Since(time.Unix(int64(n.Used), 0)) > ledgerDays*24*time.Hour {
			_ = os.Remove(f)
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First < out[j].First })
	return out
}

// sameMachine: two ways that lead to one machine. Both said what tells it from any other: that, and only that. Else
// ( an errand that did not say it ) the same ssh name but for the user, the same label, or the same hostname answered.
func sameMachine(o, n MachineNote) bool {
	switch {
	case o.Cache != n.Cache:
		return false // ( its agents are others: a place of its own )
	case o.ID != "" && n.ID != "":
		return o.ID == n.ID
	}
	return afterUser(o.Host) == afterUser(n.Host) || (n.Label != "" && n.Label == o.Label) || (n.Hostname != "" && n.Hostname == o.Hostname)
}

// Ledger: the machines known, this one first, then the most recently used. Each has a name of its own: two that
// would have one are told apart by the hostname they answered, else by how they are reached.
func Ledger(here string) []Machine {
	var groups []Machine
	for _, n := range ledgerNotes() {
		at := -1
		for i, m := range groups {
			for _, o := range m.Notes {
				if sameMachine(o, n) {
					at = i
				}
			}
		}
		if at < 0 {
			groups = append(groups, Machine{Name: n.Host})
			at = len(groups) - 1
		}
		groups[at].Notes = append(groups[at].Notes, n)
		if n.Label != "" {
			groups[at].Name = n.Label
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Used() > groups[j].Used() })
	taken := map[string]bool{here: true}
	for i := range groups {
		g := &groups[i]
		if taken[g.Name] {
			n := g.Notes[0]
			for _, aside := range []string{n.Cache, n.Host, strings.Join(n.Options, " "), n.Hostname} {
				if aside != "" && aside != g.Name && !taken[g.Name+" ( "+aside+" )"] {
					g.Name += " ( " + aside + " )"
					break
				}
			}
			for k, base := 2, g.Name; taken[g.Name]; k++ {
				g.Name = fmt.Sprintf("%s ( %d )", base, k)
			}
		}
		taken[g.Name] = true
	}
	return append([]Machine{{Name: here}}, groups...)
}

// UsedMachine: work is sent to a machine, which answered its hostname and what tells it from any other ( "": not said )
func UsedMachine(h HostSpec, hostname, id string) {
	if h.VM != nil {
		return // ( the machine that boots it says what it holds )
	}
	n := MachineNote{V: 1, Host: h.Host, Options: h.Options, Windows: h.Windows, Cache: h.Cache, First: kit.Seconds(time.Now())}
	if data, err := os.ReadFile(n.Path()); err == nil {
		_ = json.Unmarshal(data, &n)
	}
	n.Used = kit.Seconds(time.Now())
	if hostname != "" {
		n.Hostname = hostname
	}
	if id != "" {
		n.ID = id
	}
	n.Write()
}

func HereName() string {
	return system.ShortHost()
}

func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SameProject: a run's project is this one -- here, or its copy on another machine, which has its name
func SameProject(root, other string) bool {
	return other == root || filepath.Base(filepath.ToSlash(other)) == filepath.Base(root)
}

func StateWords(r TableRow, now float64) string {
	switch r.State {
	case "waiting":
		out := "waiting: " + r.More
		if r.Since > 0 {
			out += ", " + Span(now-r.Since)
		}
		return out
	case "ended":
		return "ended: " + r.More
	}
	if r.More != "" {
		return "running  ( " + r.More + " )"
	}
	if words := StepWords(r.Step, 8); words != "" {
		return "running · " + words
	}
	return "running"
}

// StepWords: where a run says it is, in a line
func StepWords(s *draw.StepState, width int) string {
	if s == nil {
		return ""
	}
	var said []string
	if s.Text != "" {
		said = append(said, s.Text)
	}
	if s.Done != nil {
		said = append(said, fmt.Sprintf("%s %.0f %%", Bar(*s.Done, width), 100**s.Done))
	}
	return strings.Join(said, " · ")
}

func Bar(done float64, width int) string {
	done = min(1, max(0, done))
	eighths := int(done * float64(width) * 8)
	out := strings.Repeat("█", eighths/8)
	if eighths%8 > 0 {
		out += string([]rune(" ▏▎▍▌▋▊▉")[eighths%8])
	}
	if n := width - len([]rune(out)); n > 0 {
		out += strings.Repeat(" ", n)
	}
	return "▕" + out + "▏"
}

func Span(s float64) string {
	switch {
	case s < 0:
		s = 0
		fallthrough
	case s < 90:
		return fmt.Sprintf("%.0f s", s)
	case s < 5400:
		return fmt.Sprintf("%.0f min", s/60)
	case s < 172800:
		return fmt.Sprintf("%d h %02d", int(s/3600), int(s/60)%60)
	}
	return fmt.Sprintf("%.0f d", s/86400)
}

func ShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func Ago(since float64) string {
	s := time.Since(time.Unix(int64(since), 0)).Seconds()
	switch {
	case s < 90:
		return fmt.Sprintf("%.0f s", s)
	case s < 5400:
		return fmt.Sprintf("%.0f min", s/60)
	case s < 172800:
		return fmt.Sprintf("%.0f h", s/3600)
	}
	return fmt.Sprintf("%.0f d", s/86400)
}

func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func JsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
