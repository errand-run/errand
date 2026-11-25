// What the screen and the page show ( the guide's "Every run, live" ): one model, one way to build rows from it, for both.
//
// It speaks to one agent, this machine's; what another machine's says is asked through it ( `at` ). Each machine watched
// says what it holds when that changes ( `watch` ); the runs done are the history's ( runinfo/history.go ). A row has a key that
// stays its own from one build to the next.

package page

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

type UiRow struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Detail  string   `json:"detail"`
	Kind    string   `json:"kind"`
	Mark    string   `json:"mark"`
	Depth   int      `json:"depth"`
	Open    *bool    `json:"open"`
	Fold    *string  `json:"fold"`
	Machine *string  `json:"machine"`
	Meters  [][2]any `json:"meters"`
	Hits    []int    `json:"hits"`
	Level   string   `json:"level,omitempty"`
	Tabled  bool     `json:"tabled,omitempty"`
	Tree    string   `json:"tree,omitempty"`
	Cols    [][3]any `json:"cols,omitempty"`

	What any `json:"-"` // what it stands for: *tableRow, *pastRun, *uiNode, vmRow, map
	on   *UiMachine
}

type UiMachine struct {
	M        reach.Machine
	doc      *reach.OverviewDoc
	res      map[string]any
	err      string
	readAt   time.Time
	history  []runinfo.PastRun // what it holds, when it was asked
	storage  map[string]any
	storedAt time.Time
	asking   bool
	watching bool
	stop     chan struct{}
}

func (m *UiMachine) Name() string { return m.M.Name }

func (m *UiMachine) Here() bool { return m.M.Here() }

// now: the time, by its clock
func (m *UiMachine) now() float64 {
	if m.doc == nil {
		return kit.Seconds(time.Now())
	}
	return m.doc.Now + time.Since(m.readAt).Seconds()
}

type Ui struct {
	Mu       sync.Mutex
	l        *reach.Link
	machines []*UiMachine
	ReadAt   time.Time
	past     []runinfo.PastRun
	PastAt   time.Time
	pastSize int64
	found    []runinfo.PastRun // on other machines only, as a check said
	going    map[string]bool
	bringing map[string]*bringing // the views on their way here ( views.go )
	gauges   time.Time            // when the machines were last shown: their gauges are read meanwhile
	Changed  func(lists bool, details []string)
	Said     func(text string)
	closed   bool
}

func NewUI() (*Ui, error) {
	conn, err := reach.ReachAgent()
	if err != nil {
		return nil, err
	}
	if err := reach.AwaitHello(conn); err != nil {
		return nil, err
	}
	u := &Ui{l: reach.NewLink(conn, conn, "this machine's agent"), going: map[string]bool{}, bringing: map[string]*bringing{}, Changed: func(bool, []string) {}, Said: func(string) {}}
	u.refresh()
	go u.measuring()
	return u, nil
}

// again: the agent went ( it was unused, or is of another version ): the one that is there now
func (u *Ui) link() (*reach.Link, error) {
	u.Mu.Lock()
	l := u.l
	u.Mu.Unlock()
	select {
	case <-l.Dead:
	default:
		return l, nil
	}
	conn, err := reach.ReachAgent()
	if err != nil {
		return nil, err
	}
	if err := reach.AwaitHello(conn); err != nil {
		return nil, err
	}
	l = reach.NewLink(conn, conn, "this machine's agent")
	u.Mu.Lock()
	u.l = l
	u.Mu.Unlock()
	return l, nil
}

// Ask: a question to a machine's agent
func (u *Ui) Ask(m *UiMachine, req map[string]any) (map[string]any, error) {
	l, err := u.link()
	if err != nil {
		return nil, err
	}
	if m == nil || m.Here() {
		return l.Ask(req)
	}
	return l.Ask(map[string]any{"op": "at", "host": m.M.Spec(), "req": req, "environ": os.Environ()})
}

// refresh: the machines, as the ledger says them
func (u *Ui) refresh() {
	u.Mu.Lock()
	defer u.Mu.Unlock()
	if time.Since(u.ReadAt) < 2*time.Second || u.closed {
		return
	}
	u.ReadAt = time.Now()
	was := map[string]*UiMachine{}
	for _, m := range u.machines {
		was[m.M.Name] = m
	}
	var all []*UiMachine
	for _, m := range reach.Ledger(reach.HereName()) {
		x := was[m.Name]
		if x == nil {
			x = &UiMachine{stop: make(chan struct{})}
		}
		delete(was, m.Name)
		x.M = m
		switch {
		case m.Dormant() && x.watching:
			close(x.stop)
			x.stop, x.watching, x.doc = make(chan struct{}), false, nil
		case !m.Dormant() && !x.watching:
			x.watching = true
			go u.watch(x, x.stop)
		}
		all = append(all, x)
	}
	for _, gone := range was {
		close(gone.stop)
	}
	u.machines = all
}

func (u *Ui) Close() {
	u.Mu.Lock()
	defer u.Mu.Unlock()
	u.closed = true
	for _, m := range u.machines {
		if m.watching {
			close(m.stop)
			m.watching = false
		}
	}
	u.l.Close()
}

func (u *Ui) watch(m *UiMachine, stop chan struct{}) {
	for {
		l, err := u.link()
		if err == nil {
			req := map[string]any{"op": "watch"}
			if !m.Here() {
				req = map[string]any{"op": "at", "host": m.M.Spec(), "req": req, "environ": os.Environ()}
			}
			c := l.Open(req)
			if _, err = c.Reply(); err == nil {
				got := make(chan reach.LinkMsg)
				go func() {
					defer close(got)
					for {
						kind, payload, err := c.Recv()
						if err != nil || kind == kit.EXIT {
							return
						}
						select {
						case got <- reach.LinkMsg{Kind: kind, Payload: payload}:
						case <-stop:
							return
						}
					}
				}()
				for on := true; on; {
					select {
					case <-stop:
						c.Close()
						return
					case msg, more := <-got:
						if !more {
							on = false
							break
						}
						var said struct {
							Overview *reach.OverviewDoc `json:"overview"`
						}
						if msg.Kind == kit.NOTE && json.Unmarshal(msg.Payload, &said) == nil && said.Overview != nil {
							u.heard(m, said.Overview)
						}
					}
				}
				err = errors.New("the connection was lost")
			}
			c.Close()
		}
		u.Mu.Lock()
		m.err = kit.FirstLine(err.Error())
		u.Mu.Unlock()
		u.Changed(true, nil)
		select {
		case <-stop:
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (u *Ui) heard(m *UiMachine, doc *reach.OverviewDoc) {
	u.Mu.Lock()
	was := map[string]reach.TableRow{}
	if m.doc != nil {
		for _, r := range m.doc.Rows {
			was[r.ID] = r
		}
	}
	var details []string
	ended := false
	lists := m.doc == nil || m.err != "" || len(doc.Rows) != len(was)
	for _, r := range doc.Rows {
		w, ok := was[r.ID]
		if !ok || !sameSaid(w, r) {
			details = append(details, workKey(r.Leaf))
		}
		w.Out, w.Marks = r.Out, r.Marks // ( what a run writes is in its detail: the lists do not say it )
		lists = lists || !ok || !sameSaid(w, r)
		delete(was, r.ID)
		ended = ended || (r.State == "ended" && w.State != r.State)
	}
	for _, r := range was { // let go of by its agent: it is among those that are done
		details = append(details, workKey(r.Leaf))
	}
	if len(was) > 0 || ended {
		u.PastAt, lists = time.Time{}, true
	}
	if m.doc == nil || !sameSaid(aside(m.doc), aside(doc)) { // the machine itself, and what it keeps
		lists, details = true, append(details, "*")
	}
	m.doc, m.err, m.readAt = doc, "", time.Now()
	u.Mu.Unlock()
	staled(details)
	if lists || len(details) > 0 {
		u.Changed(lists, details)
	}
}

func sameSaid(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// aside: what a machine says of itself beside its runs, less what is only the time that passes
func aside(doc *reach.OverviewDoc) reach.OverviewDoc {
	out := *doc
	out.Now, out.Rows = 0, nil
	out.Agents = append([]reach.AgentRow{}, doc.Agents...)
	for i := range out.Agents {
		out.Agents[i].Idle, out.Agents[i].RSS = 0, 0
	}
	return out
}

// measuring: the machines' gauges, while they are looked at
func (u *Ui) measuring() {
	for {
		time.Sleep(gaugesEvery)
		u.Mu.Lock()
		if u.closed {
			u.Mu.Unlock()
			return
		}
		look := time.Since(u.gauges) < 30*time.Second
		var those []*UiMachine
		for _, m := range u.machines {
			if look && m.watching && m.doc != nil && m.M.Gauges() {
				those = append(those, m)
			}
		}
		u.Mu.Unlock()
		var wg sync.WaitGroup
		var moved atomic.Bool
		for _, m := range those {
			wg.Add(1)
			go func(m *UiMachine) {
				defer wg.Done()
				if got, err := u.Ask(m, map[string]any{"op": "resources"}); err == nil {
					res, _ := got["resources"].(map[string]any)
					u.Mu.Lock()
					if gaugesSaid(m.res) != gaugesSaid(res) {
						moved.Store(true)
					}
					m.res = res
					u.Mu.Unlock()
				}
			}(m)
		}
		wg.Wait()
		if moved.Load() {
			u.Changed(true, []string{"*"})
		}
	}
}

const gaugesEvery = 5 * time.Second // ( while somebody looks at the machines )

// gaugesSaid: what is drawn of a machine's resources, as finely as it is
func gaugesSaid(res map[string]any) string {
	said := fmt.Sprintf("%.1f", kit.Num(res["load"], -1))
	part := func(x any) {
		if g, ok := x.(map[string]any); ok && kit.Num(g["total"], 0) > 0 {
			said += fmt.Sprintf(" %.2f %.0f", kit.Num(g["used"], 0)/kit.Num(g["total"], 1), kit.Num(g["util"], 0))
		}
	}
	part(res["ram"])
	for _, g := range anyList(res["gpus"]) {
		part(g)
	}
	held, _ := json.Marshal(res["held"])
	return said + string(held) + fmt.Sprint(res["cpu"])
}

// runs: the runs done that this machine holds ( read again when its history grew )
func (u *Ui) runs() []runinfo.PastRun {
	size := int64(0)
	if st, err := os.Stat(runinfo.HistoryPath()); err == nil {
		size = st.Size()
	}
	u.Mu.Lock()
	fresh := size == u.pastSize && time.Since(u.PastAt) < 30*time.Second
	out := u.past
	found := u.found
	u.Mu.Unlock()
	if !fresh {
		out = runinfo.PastRuns()
		u.Mu.Lock()
		u.past, u.PastAt, u.pastSize = out, time.Now(), size
		u.Mu.Unlock()
	}
	if len(found) == 0 {
		return out
	}
	have := map[string]bool{}
	for _, r := range out {
		have[r.Rel] = true
	}
	all := append([]runinfo.PastRun{}, out...)
	for _, r := range found {
		if !have[r.Rel] {
			all = append(all, r)
		}
	}
	return all
}

// ── rows ─────────────────────────────────────────────────────────────────────

type UiAsked struct {
	Tab     string
	Query   string
	Folded  map[string]bool
	Opened  map[string]bool
	Shown   int
	Days    float64
	All     bool
	Sel     string // the row looked at: what is above it is open
	Project string // "": every project
	Unfold  bool
}

func yes(b bool) *bool { return &b }

func ptr(s string) *string { return &s }

func rowKey(kind, machine, what string) string {
	b, _ := json.Marshal([]any{kind, machine, what})
	return string(b)
}

// relOf: a run's directory, from the tree of runs it is in: the same here and where it ran
func relOf(leaf string) string {
	leaf = filepath.ToSlash(leaf)
	if i := strings.LastIndex(leaf, "/errand-outputs/runs/"); i >= 0 {
		return leaf[i+len("/errand-outputs/runs/"):]
	}
	if i := strings.Index(leaf, "/runs/"); i >= 0 {
		return leaf[i+6:]
	}
	return leaf
}

func workKey(leaf string) string {
	b, _ := json.Marshal([]any{"work", nil, relOf(leaf)})
	return string(b)
}

func matches(query string, texts ...string) bool {
	hay := strings.ToLower(strings.Join(texts, " "))
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func rowMark(r reach.TableRow) (string, string) {
	if r.State == "waiting" {
		return "wait", "waiting: " + r.More
	}
	return "run", "running"
}

func statusMark(status string) string {
	switch status {
	case "PASS":
		return "ok"
	case "FAIL":
		return "FAIL"
	case "SKIP":
		return "skip"
	}
	return "lost"
}

// Build: the rows of a tab
func (u *Ui) Build(q UiAsked) []UiRow {
	rows := u.rowsOf(q)
	for i := range rows { // lists, for whoever reads them, and never nothing
		if rows[i].Meters == nil {
			rows[i].Meters = [][2]any{}
		}
		if rows[i].Hits == nil {
			rows[i].Hits = []int{}
		}
	}
	return rows
}

func (u *Ui) rowsOf(q UiAsked) []UiRow {
	u.refresh()
	if q.Tab == "machines" {
		u.Mu.Lock()
		u.gauges = time.Now()
		u.Mu.Unlock()
	}
	switch q.Tab {
	case "machines":
		return u.machineRows(q)
	case "caches":
		return u.storageRows(q)
	}
	return u.runRows(q)
}

func shortName(s string) string {
	if r := []rune(s); len(r) > 12 {
		return string(r[:11]) + "…"
	}
	return s
}

func (u *Ui) snapshot() []*UiMachine {
	u.Mu.Lock()
	defer u.Mu.Unlock()
	out := make([]*UiMachine, len(u.machines))
	for i, m := range u.machines {
		copied := *m
		out[i] = &copied
	}
	return out
}

func (u *Ui) real(m *UiMachine) *UiMachine {
	if m == nil {
		return nil
	}
	u.Mu.Lock()
	defer u.Mu.Unlock()
	for _, x := range u.machines {
		if x.M.Name == m.M.Name {
			return x
		}
	}
	return m
}

func (u *Ui) runRows(q UiAsked) []UiRow {
	type work struct {
		m *UiMachine
		r reach.TableRow
	}
	var all []work
	busy := map[string]bool{}
	asking := false
	for _, m := range u.snapshot() {
		if m.M.Dormant() {
			continue
		}
		if m.doc == nil {
			asking = asking || m.err == ""
			continue
		}
		for _, r := range m.doc.Rows {
			if r.State == "ended" || r.Aside {
				continue
			}
			busy[relOf(r.Leaf)] = true
			if (q.Project == "" || reach.SameProject(q.Project, r.Project)) && matches(q.Query, m.Name(), path.Base(filepath.ToSlash(r.Project)), r.What, r.Env, reach.StateWords(r, m.now())) {
				all = append(all, work{m, r})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].r.Started < all[j].r.Started })
	var rows []UiRow
	for _, title := range []string{"running", "waiting"} {
		var those []work
		for _, w := range all {
			if (w.r.State == "waiting") == (title == "waiting") {
				those = append(those, w)
			}
		}
		if len(those) == 0 {
			continue
		}
		open := !q.Folded["day:"+title]
		rows = append(rows, UiRow{Key: rowKey("day", "", title), Label: fmt.Sprintf("%s ( %d )", title, len(those)), Kind: "day", Open: yes(open), Fold: ptr("day:" + title)})
		for _, w := range those {
			if !open {
				break
			}
			w := w
			mark, words := rowMark(w.r)
			if mark == "run" {
				words = reach.StepWords(w.r.Step, 8)
				if words == "" {
					words = w.r.More
				}
			}
			since := w.r.Started
			if mark == "wait" && w.r.Since > 0 {
				since = w.r.Since
			}
			env := ""
			if w.r.Env != "" {
				env = "  " + w.r.Env
			}
			rows = append(rows, UiRow{Key: workKey(w.r.Leaf), Label: fmt.Sprintf("%-12s %s%s", shortName(w.m.Name()), w.r.What, env),
				Detail: strings.TrimSpace(words + "  " + reach.Span(w.m.now()-since)), Kind: "proc", Mark: mark, Machine: ptr(w.m.Name()), What: &w.r, on: w.m})
		}
	}
	tree := runTree(u.filtered(q), busy)
	if q.Sel != "" { // the row looked at stays shown: what is above it kept open, a run that ends folded away included
		folded, opened := map[string]bool{}, map[string]bool{}
		for k := range q.Folded {
			folded[k] = true
		}
		for k := range q.Opened {
			opened[k] = true
		}
		for _, id := range tree.above(q.Sel) {
			opened["t:"+id] = true
			delete(folded, "t:"+id)
		}
		q.Folded, q.Opened = folded, opened
	}
	rows = append(rows, u.treeRows(tree, q)...)
	if len(rows) == 0 {
		text := "nothing runs, nothing was run lately"
		if asking {
			text = "asking the machines…"
		}
		rows = []UiRow{{Key: rowKey("note", "", "none"), Label: text, Kind: "note"}}
	}
	return rows
}

func (u *Ui) filtered(q UiAsked) []runinfo.PastRun {
	var out []runinfo.PastRun
	for _, r := range u.runs() {
		if (q.Project == "" || reach.SameProject(q.Project, r.Project)) && matches(q.Query, r.Host, path.Base(filepath.ToSlash(r.Project)), r.What, r.Env, r.Status, r.File) {
			out = append(out, r)
		}
	}
	return out
}

func gb(mb float64) string {
	if mb >= 10*1024 {
		return fmt.Sprintf("%.0f G", mb/1024)
	}
	return fmt.Sprintf("%.1f G", mb/1024)
}

// meters: a machine's gauges for its row -- the cores, the memory, each card ( their mean beyond two )
func meters(res map[string]any) [][2]any {
	out := [][2]any{}
	if res == nil {
		return out
	}
	round := func(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }
	if res["busy"] != nil {
		out = append(out, [2]any{"cpu", round(kit.Num(res["busy"], 0))})
	}
	ram, _ := res["ram"].(map[string]any)
	if total := kit.Num(ram["total"], 0); total > 0 {
		out = append(out, [2]any{"ram", round(kit.Num(ram["used"], 0) / total)})
	}
	var utils []float64
	var names []string
	for _, x := range anyList(res["gpus"]) {
		g, _ := x.(map[string]any)
		if g["util"] != nil {
			utils = append(utils, kit.Num(g["util"], 0)/100)
			names = append(names, fmt.Sprintf("gpu%v", g["index"]))
		}
	}
	if len(utils) <= 2 {
		for i, v := range utils {
			out = append(out, [2]any{names[i], round(v)})
		}
		return out
	}
	sum := 0.0
	for _, v := range utils {
		sum += v
	}
	return append(out, [2]any{fmt.Sprintf("gpu×%d", len(utils)), round(sum / float64(len(utils)))})
}

func anyList(v any) []any { out, _ := v.([]any); return out }

func (u *Ui) machineRows(q UiAsked) []UiRow {
	var rows []UiRow
	for _, m := range u.snapshot() {
		m := m
		name := m.Name()
		if m.Here() {
			name += " (here)"
		}
		if m.M.Dormant() {
			rows = append(rows, UiRow{Key: rowKey("dormant", m.Name(), ""), Label: name, Detail: reach.Span(kit.Seconds(time.Now()) - m.M.Used()), Kind: "dormant", Machine: ptr(m.Name()), on: m})
			continue
		}
		running, waiting := 0, 0
		if m.doc != nil {
			for _, r := range m.doc.Rows {
				switch {
				case r.Aside || r.State == "ended":
				case r.State == "waiting":
					waiting++
				default:
					running++
				}
			}
		}
		said, kind := fmt.Sprintf("%d/%d", running, waiting), "machine"
		switch {
		case m.err != "":
			said, kind = "✗", "broken"
		case m.doc == nil:
			said = "…"
		}
		open := !q.Folded[m.Name()]
		rows = append(rows, UiRow{Key: rowKey("machine", m.Name(), ""), Label: name, Detail: said, Kind: kind, Open: yes(open), Fold: ptr(m.Name()),
			Machine: ptr(m.Name()), Meters: meters(m.res), on: m})
		if !open || m.doc == nil {
			continue
		}
		for _, v := range m.doc.VMs {
			v := v
			if matches(q.Query, m.Name(), v.Text, v.State) {
				rows = append(rows, UiRow{Key: rowKey("vm", m.Name(), v.ID), Label: v.Text, Detail: v.State, Kind: "vm", Depth: 1, Machine: ptr(m.Name()), What: &v, on: m})
			}
		}
		for _, s := range m.doc.Serving {
			s := s
			if matches(q.Query, m.Name(), s.Name, s.State) {
				rows = append(rows, UiRow{Key: rowKey("service", m.Name(), s.Name), Label: "service " + s.Name, Detail: strings.TrimSpace(s.State + "  " + s.More), Kind: "service", Depth: 1,
					Machine: ptr(m.Name()), What: &s, on: m})
			}
		}
		for _, k := range m.doc.Kept {
			k := k
			if matches(q.Query, m.Name(), k.Text) {
				kind, mark := "note", ""
				if k.Kind == "unowned" {
					kind, mark = "unowned", "lost"
				}
				rows = append(rows, UiRow{Key: rowKey(kind, m.Name(), k.Kind+k.ID+fmt.Sprint(k.Pid)), Label: k.Text, Kind: kind, Mark: mark, Depth: 1, Machine: ptr(m.Name()), What: &k, on: m})
			}
		}
	}
	return rows
}

// find: the row a key names, and its machine -- rows built again with everything shown
func (u *Ui) find(key string) (*UiRow, error) {
	var parts []any
	_ = json.Unmarshal([]byte(key), &parts)
	kind := ""
	if len(parts) == 3 {
		kind, _ = parts[0].(string)
	}
	tabs := []string{"runs", "machines"}
	switch {
	case strings.HasPrefix(kind, "c") || kind == "mount" || kind == "sdir" || kind == "svcs" || kind == "svcver":
		tabs = []string{"caches"}
	case kind == "machine" || kind == "dormant" || kind == "vm" || kind == "service" || kind == "unowned" || kind == "note":
		tabs = []string{"machines", "runs"}
	}
	for _, tab := range tabs {
		rows := u.Build(UiAsked{Tab: tab, Shown: 1 << 30, Sel: key, Days: 30, Unfold: tab == "caches"})
		for i := range rows {
			if rows[i].Key == key {
				return &rows[i], nil
			}
		}
	}
	return nil, errors.New("not there any more")
}
