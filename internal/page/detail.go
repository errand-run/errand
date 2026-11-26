// The detail of a row, what can be done on it, and the storage's rows -- for the screen and the page alike.

package page

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/svc"
)

type UiDetail struct {
	Kind    string  `json:"kind"`
	Label   string  `json:"label"`
	Detail  string  `json:"detail"`
	K       string  `json:"k,omitempty"`
	V       string  `json:"v,omitempty"`
	Machine string  `json:"machine,omitempty"`
	Path    string  `json:"path,omitempty"`
	URL     string  `json:"url,omitempty"`
	Look    string  `json:"look,omitempty"`
	Ready   bool    `json:"ready,omitempty"`
	Dir     bool    `json:"dir,omitempty"`
	T       float64 `json:"t,omitempty"`
	Status  string  `json:"status,omitempty"`
}

type UiRead struct {
	at     time.Time
	stale  bool // its machine said it changed
	files  []map[string]any
	dirs   []map[string]any
	tail   []byte   // the end of its output, as read so far
	outAt  any      // and where that ended
	said   *journal // what it says of itself, read so far
	out    string
	Path   string
	source string
	views  []uiView
	step   *stepFull
	err    string
}

type uiView struct {
	View, Title string
	Static      string // served as it is: its index
	Last        []string
	T           float64 // when it was named first
	At          float64 // and last
}

// stepFull: a run's journal, whole -- its last word, its curves
type stepFull struct {
	Text   string
	Done   *float64
	Eta    *float64
	T      float64
	Curves map[string][]float64
	Order  []string
}

func spark(values []float64, width int) string {
	if len(values) > width { // the most recent, as many as there is room for
		step := float64(len(values)) / float64(width)
		var out []float64
		for i := 0; i < width; i++ {
			out = append(out, values[int(float64(i+1)*step)-1])
		}
		values = out
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo, hi = min(lo, v), max(hi, v)
	}
	var b strings.Builder
	for _, v := range values {
		i := 0
		if hi > lo {
			i = int((v - lo) / (hi - lo) * 7.999)
		}
		b.WriteRune([]rune("▁▂▃▄▅▆▇█")[i])
	}
	return b.String()
}

// journal is what a run says of itself, read as it grows.
type journal struct {
	end               float64 // where what was read of it ends
	part              string  // a line not whole yet
	step              stepFull
	views             []uiView
	at                map[string]int
	firstT, firstDone float64
	have, spoke       bool
}

// read: what was added
func (j *journal) read(text string) {
	s := &j.step
	if j.at == nil {
		j.at, s.Curves = map[string]int{}, map[string][]float64{}
	}
	text = j.part + text
	whole := strings.LastIndexByte(text, '\n') + 1
	text, j.part = text[:whole], text[whole:]
	for _, line := range strings.Split(text, "\n") {
		var e map[string]any
		if strings.TrimSpace(line) == "" || kit.JsonUnmarshal([]byte(line), &e) != nil {
			continue
		}
		if _, transfer := e["transfer"]; transfer {
			continue
		}
		t := kit.Num(e["t"], s.T)
		if v, ok := e["view"].(string); ok {
			i, known := j.at[v]
			if !known {
				i = len(j.views)
				j.at[v] = i
				j.views = append(j.views, uiView{View: v, T: t})
			}
			view := &j.views[i] // named again, without saying again how it is opened: opened as it was
			view.At = t
			if title, ok := e["title"].(string); ok {
				view.Title = title
			}
			for _, x := range anyList(e["open"]) {
				if o, ok := x.(map[string]any); ok {
					if index, ok := o["static"].(string); ok {
						view.Static = index
					}
				}
			}
			if last := kit.Strs(e["last"]); len(last) > 0 {
				view.Last = last
			}
		}
		for k, v := range e {
			switch k {
			case "t", "view", "title", "last", "open", "dir":
			case "text":
				s.Text, _ = v.(string)
				j.spoke = true
			case "done":
				d := kit.Num(v, 0)
				s.Done, j.spoke = &d, true
				if !j.have {
					j.firstT, j.firstDone, j.have = t, d, true
				} else if rate := (d - j.firstDone) / (t - j.firstT); t > j.firstT && rate > 0 {
					eta := (1 - d) / rate
					s.Eta = &eta
				}
			default:
				if x, ok := v.(float64); ok {
					if s.Curves[k] == nil {
						s.Order = append(s.Order, k)
					}
					s.Curves[k] = append(s.Curves[k], x)
					j.spoke = true
				}
			}
		}
		s.T = t
	}
	sort.Strings(s.Order)
}

// state: where the run is, and its views
func (j *journal) state() (*stepFull, []uiView) {
	views := append([]uiView{}, j.views...)
	if !j.spoke {
		return nil, views
	}
	s := j.step
	s.Order = append([]string{}, s.Order...)
	s.Curves = map[string][]float64{}
	for k, v := range j.step.Curves {
		s.Curves[k] = v
	}
	return &s, views
}

const readTail = 1 << 16 // how much of the end of an output is kept

// read: the files of a run's directory, the end of its output, what it said of where it is -- through its machine's agent,
// in one answer; what was read before is not read again
func (u *Ui) read(m *UiMachine, leaf string, was UiRead) UiRead {
	got := UiRead{at: time.Now(), tail: was.tail, outAt: was.outAt, said: was.said}
	if got.said == nil {
		got.said = &journal{}
	}
	for {
		a, err := u.Ask(m, map[string]any{"op": "run_look", "path": leaf, "out": got.outAt, "steps": got.said.end})
		if err != nil {
			got.err = kit.FirstLine(err.Error())
			break
		}
		got.files, got.dirs = nil, nil
		for _, x := range anyList(a["files"]) {
			f, _ := x.(map[string]any)
			f["path"] = leaf + "/" + kit.Text(f["name"])
			got.files = append(got.files, f)
			if kit.Text(f["name"]) == "output.txt" {
				got.Path, got.source = kit.Text(f["path"]), "output.txt"
			}
		}
		for _, x := range anyList(a["dirs"]) {
			if d, ok := x.(map[string]any); ok {
				got.dirs = append(got.dirs, d)
			}
		}
		if t, ok := a["out"].(map[string]any); ok {
			b, _ := base64.StdEncoding.DecodeString(kit.Text(t["b64"]))
			switch {
			case got.outAt == nil || kit.Num(t["offset"], 0) != kit.Num(got.outAt, -1): // its end, anew
				got.tail = b
				if kit.Num(t["offset"], 0) > 0 { // a line cut in two is not shown
					if _, rest, cut := bytes.Cut(b, []byte("\n")); cut {
						got.tail = rest
					}
				}
			default:
				all := append(append([]byte{}, got.tail...), b...)
				if len(all) > 2*readTail {
					all = all[len(all)-readTail:]
					if _, rest, cut := bytes.Cut(all, []byte("\n")); cut {
						all = rest
					}
				}
				got.tail = all
			}
			got.outAt = kit.Num(t["end"], 0)
		}
		t, ok := a["steps"].(map[string]any)
		if !ok {
			break
		}
		b, _ := base64.StdEncoding.DecodeString(kit.Text(t["b64"]))
		if kit.Num(t["size"], 0) < got.said.end { // another journal
			got.said = &journal{}
			continue
		}
		next := *got.said // ( what was read before may be read by another page meanwhile )
		next.at = map[string]int{}
		for k, v := range got.said.at {
			next.at[k] = v
		}
		next.views = append([]uiView{}, got.said.views...)
		next.step.Order = append([]string{}, got.said.step.Order...)
		next.step.Curves = map[string][]float64{}
		for k, v := range got.said.step.Curves {
			next.step.Curves[k] = append([]float64{}, v...)
		}
		got.said = &next
		got.said.read(string(b))
		end := kit.Num(t["end"], 0)
		stuck := end == got.said.end
		got.said.end = end
		if stuck || end >= kit.Num(t["size"], 0) {
			break
		}
	}
	got.out = strings.ToValidUTF8(string(got.tail), "\uFFFD")
	got.step, got.views = got.said.state()
	return got
}

var ownFiles = map[string]bool{"stdout.txt": true, "stderr.txt": true, "steps.jsonl": true, "result.json": true, "launch.json": true, "summary.json": true, "asks.jsonl": true, "results.jsonl": true}

func Rich(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".svg", ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".mp4", ".webm":
		return true
	}
	return false
}

func sizeText(n float64) string {
	for _, unit := range []string{"B", "kB", "MB", "GB"} {
		if n < 1000 || unit == "GB" {
			if unit == "B" {
				return fmt.Sprintf("%.0f B", n)
			}
			return fmt.Sprintf("%.1f %s", n, unit)
		}
		n /= 1000
	}
	return ""
}

// machineOf: the machine whose files a row's are -- a run brought back here is read here
func (u *Ui) machineOf(row *UiRow) *UiMachine {
	if row.on != nil {
		return u.real(row.on)
	}
	if r, ok := row.What.(*runinfo.PastRun); ok {
		for _, m := range u.snapshot() {
			m := m
			if (r.There == "" && m.Here()) || (r.There != "" && m.Name() == r.There) {
				return u.real(m)
			}
		}
	}
	return nil
}

func RowLeaf(row *UiRow) string {
	switch w := row.What.(type) {
	case *runinfo.PastRun:
		return w.Leaf
	case *reach.TableRow:
		return w.Leaf
	}
	return ""
}

// facts: what the detail says of a row first
func (u *Ui) facts(row *UiRow, m *UiMachine, step *stepFull) [][2]string {
	var out [][2]string
	line := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	definition := func(command []string, vars map[string]string, cwd, needs, config string) {
		line("command", strings.Join(command, " "))
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			line("$"+k, vars[k])
		}
		line("cwd", cwd)
		line("needs", needs)
		line("hash", config)
	}
	steps := func(now float64) {
		if step == nil {
			return
		}
		line("step", step.Text)
		if step.Done != nil {
			eta := ""
			if step.Eta != nil && *step.Done < 1 {
				eta = "  " + draw.Lasting(*step.Eta) + " left"
			}
			line("progress", fmt.Sprintf("%s %.0f %%%s", reach.Bar(*step.Done, 20), 100**step.Done, eta))
		}
		if step.T > 0 {
			line("said", reach.Span(now-step.T)+" ago")
		}
		for _, k := range step.Order {
			v := step.Curves[k]
			line(k, fmt.Sprintf("%s  %.4g", spark(v, 30), v[len(v)-1]))
		}
	}
	switch w := row.What.(type) {
	case *reach.TableRow:
		line("machine", m.Name())
		line("what", w.What)
		line("env", w.Env)
		line("state", reach.StateWords(*w, m.now()))
		steps(m.now())
		line("started", reach.Span(m.now()-w.Started)+" ago")
		if w.Pid > 0 {
			line("pid", fmt.Sprint(w.Pid))
		}
		line("slurm", w.Slurm)
		line("id", w.ID)
		line("leaf", w.Leaf)
	case *runinfo.PastRun:
		line("machine", w.Host)
		line("what", w.What)
		line("env", w.Env)
		status := w.Status
		if status == "" {
			status = "no result yet"
		}
		line("status", status)
		line("error", w.Error)
		line("started", reach.Span(kit.Seconds(time.Now())-w.Started)+" ago")
		if w.Duration != nil {
			line("took", reach.Span(*w.Duration))
		}
		if w.Counts != [3]int{} {
			line("entries", fmt.Sprintf("%d passed, %d failed, %d skipped", w.Counts[0], w.Counts[1], w.Counts[2]))
		}
		names := make([]string, 0)
		for k := range w.Params {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			line(k, shownValue(w.Params[k]))
		}
		names = names[:0]
		for k := range w.Results {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			line("→"+k, shownValue(w.Results[k]))
		}
		steps(kit.Seconds(time.Now()))
		line("leaf", w.Leaf)
	case *reach.VmRow:
		line("machine", m.Name())
		line("vm", w.Text)
		line("state", w.State)
		if w.Pid > 0 {
			line("pid", fmt.Sprint(w.Pid))
		}
	case *reach.ServingRow:
		line("machine", m.Name())
		line("service", w.Name)
		line("state", w.State)
		line("says", w.More)
		for _, l := range leasesOf(m, w.Config) {
			for _, p := range l.Ports {
				k := "address"
				if len(l.Ports) > 1 {
					k = p.Name
				}
				line(k, fmt.Sprintf("127.0.0.1:%d  ( kept %s )", p.Port, reach.Span(l.Until-m.now())))
			}
		}
		for _, p := range w.Ports {
			if p.Server > 0 {
				line("server's", fmt.Sprintf("%s %d", p.Name, p.Server))
			}
		}
		if w.Ended > 0 {
			line("stopped", reach.Span(m.now()-w.Ended)+" ago")
		} else {
			line("since", reach.Span(m.now()-w.Started))
			line("clients", fmt.Sprint(w.Users))
			if w.Waiting > 0 {
				line("waiting", fmt.Sprintf("%d connection%s, until it answers", w.Waiting, reach.Plural(w.Waiting)))
			}
		}
		line("in", w.On)
		definition(w.Command, w.Vars, w.Cwd, w.Needs, w.Config)
		if w.Idle > 0 {
			line("idle", "stops once unused for "+reach.Span(w.Idle))
		}
		line("source", w.Source)
		if w.Pid > 0 {
			line("pid", fmt.Sprint(w.Pid))
		}
		if w.Agent > 0 {
			line("held by", fmt.Sprintf("the agent %d", w.Agent))
		}
		line("journal", w.Log)
	case *reach.LeaseRow:
		line("machine", m.Name())
		line("leads to", w.On)
		line("service", w.Name)
		for _, p := range w.Ports {
			line(map[bool]string{true: "address", false: p.Name}[len(w.Ports) == 1], fmt.Sprintf("%s:%d", w.Address(), p.Port))
		}
		if w.Listen != "" {
			line("listened on", w.Listen+": other machines may connect")
		}
		if w.Always {
			line("always", "the system listens in errand's stead, and starts it at a connection")
		}
		if w.HoldEnd > 0 {
			line("hold end", "a client's end is passed on once its server was silent "+reach.Span(w.HoldEnd))
		}
		line("until", fmt.Sprintf("%s from now; %s once it was last used", reach.Span(w.Until-m.now()), reach.Span(w.Keep)))
		line("clients", fmt.Sprint(w.Users))
		state := "at rest: the next connection starts its server"
		if w.On != "" {
			state = "on " + w.On + ": the next connection starts it, if it rests"
		}
		for _, s := range m.doc.Serving {
			if s.Config == w.Config {
				state = s.State
			}
		}
		line("server", state)
		line("failed", w.Failed)
		line("in", w.Layers)
		definition(w.Command, w.Vars, w.Cwd, w.Needs, w.Config)
		line("idle", "its server stops once unused for "+reach.Span(w.Idle))
		line("asked", reach.Span(m.now()-w.Taken)+" ago")
		line("source", w.Source)
		if w.Others {
			line("others", "other accounts may connect")
		}
		if w.Refused > 0 {
			line("refused", fmt.Sprintf("%d connection%s of other accounts", w.Refused, reach.Plural(w.Refused)))
		}
	case *reach.AuthRow:
		line("machine", m.Name())
		line("authorized", w.Label)
		line("to", strings.Join(w.To, ", "))
		line("asked as", w.Page)
		line("failed", w.Error)
		definition(w.Command, w.Vars, w.Cwd, w.Needs, w.Hash)
		if w.Idle > 0 {
			line("idle", "its server stops once unused for "+reach.Span(w.Idle))
		}
		if w.Keep > 0 {
			line("keep", "its port stays "+reach.Span(w.Keep)+" after its last use")
		}
		line("from", w.Dev)
		if w.Unverified {
			line("unverified", "no package there gives this version: as it was typed")
		}
		line("since", w.Since)
		line("used", map[bool]string{true: "never", false: w.Used}[w.Used == ""])
		for _, who := range w.To {
			if name, ok := strings.CutPrefix(who, "key:"); ok {
				line("key "+name, `restrict,command="~/.cache/errand/bin/errand there --only `+who+`" …, or command="… errand serve connect `+w.Hash+`"`)
			}
		}
	case *reach.RefusedRow:
		line("machine", m.Name())
		line("asked by", w.Who)
		line("asked for", w.What)
		line("from", w.From)
		line("why", w.Why)
		line("would run", svc.Typed(w.Command))
		line("when", reach.Span(m.now()-w.At)+" ago")
		if w.Times > 1 {
			line("times", fmt.Sprint(w.Times))
		}
	case *reach.KeptRow:
		line("machine", m.Name())
		line(w.Kind, w.Text)
		if w.Pid > 0 {
			line("pid", fmt.Sprint(w.Pid))
		}
	case map[string]any:
		names := make([]string, 0, len(w))
		for k := range w {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			switch v := w[k].(type) {
			case string:
				line(k, v)
			case float64:
				if strings.Contains(k, "size") {
					line(k, sizeText(v))
				} else {
					line(k, shownValue(v))
				}
			case bool:
				line(k, fmt.Sprint(v))
			}
		}
	default:
		if m == nil {
			return out
		}
		if m.M.Dormant() {
			line("machine", m.Name())
			line("dormant", "no work sent there for "+reach.Span(kit.Seconds(time.Now())-m.M.Used())+": not contacted, so that no agent starts there for nothing")
			line("a", "contacts it, now and from now on ( saved: errand status too )")
			return out
		}
		here := ""
		if m.Here() {
			here = "  ( this machine )"
		}
		line("machine", m.Name()+here)
		if h := m.M.Spec(); h != nil {
			line("reached", "ssh "+h.Host+", through its agent")
		}
		line("error", m.err)
		if m.doc != nil {
			line("host", m.doc.Host)
			running, waiting := 0, 0
			for _, r := range m.doc.Rows {
				if r.State == "waiting" {
					waiting++
				} else if r.State == "running" && !r.Aside {
					running++
				}
			}
			line("work", fmt.Sprintf("%d running, %d waiting", running, waiting))
			for _, g := range m.doc.Agents {
				kind := g.Kind
				if kind == "" {
					kind = "machine"
				}
				line("agent", fmt.Sprintf("%s, pid %d, up %s, release %s", kind, g.Pid, reach.Span(m.now()-g.Started), g.Release))
			}
		}
		if m.res != nil {
			cpu := fmt.Sprintf("%.0f cores", kit.Num(m.res["cpus"], 0))
			if m.res["busy"] != nil {
				cpu = fmt.Sprintf("%.0f %% of %s", 100*kit.Num(m.res["busy"], 0), cpu)
			}
			if m.res["load"] != nil {
				cpu += fmt.Sprintf(" · load %.1f", kit.Num(m.res["load"], 0))
			}
			line("cpu", cpu)
			if ram, ok := m.res["ram"].(map[string]any); ok && kit.Num(ram["total"], 0) > 0 {
				line("ram", gb(kit.Num(ram["used"], 0))+" of "+gb(kit.Num(ram["total"], 0))+" used")
			}
			for _, x := range anyList(m.res["gpus"]) {
				g, _ := x.(map[string]any)
				line(fmt.Sprintf("gpu %v", g["index"]), fmt.Sprintf("%s · %.0f %% · %s of %s", kit.Text(g["name"]), kit.Num(g["util"], 0), gb(kit.Num(g["used"], 0)), gb(kit.Num(g["total"], 0))))
			}
			if held, ok := m.res["held"].(map[string]any); ok && kit.Num(held["claims"], 0) > 0 {
				line("held", fmt.Sprintf("by errand: %.0f claim%s", kit.Num(held["claims"], 0), reach.Plural(int(kit.Num(held["claims"], 0)))))
			}
		}
	}
	return out
}

var ReadsMu sync.Mutex

var Reads = map[string]UiRead{}

// readAgain: after how long a run is read again, its machine having said nothing of it -- which it does when the run moves
func readAgain(row *UiRow) time.Duration {
	if r, ok := row.What.(*reach.TableRow); ok && r.State != "ended" {
		return time.Minute
	}
	return 10 * time.Second
}

// staled: those runs are to be read again, when somebody looks at them
func staled(keys []string) {
	ReadsMu.Lock()
	defer ReadsMu.Unlock()
	for _, k := range keys {
		if got, ok := Reads[k]; ok {
			got.stale = true
			Reads[k] = got
		}
	}
}

// DetailOf: what the detail of a row says
func (u *Ui) DetailOf(row *UiRow) ([]UiDetail, *UiMachine, UiRead) {
	m := u.machineOf(row)
	leaf := RowLeaf(row)
	var got UiRead
	if leaf != "" && m != nil {
		ReadsMu.Lock()
		got = Reads[row.Key]
		ReadsMu.Unlock()
		if got.at.IsZero() || got.stale || got.err != "" || time.Since(got.at) > readAgain(row) {
			got = u.read(m, leaf, got)
			ReadsMu.Lock()
			Reads[row.Key] = got
			if len(Reads) > 200 {
				Reads = map[string]UiRead{row.Key: got}
			}
			ReadsMu.Unlock()
		}
	}
	out := []UiDetail{}
	for _, f := range u.facts(row, m, got.step) {
		out = append(out, UiDetail{Kind: "plain", Label: fmt.Sprintf("%-9s %s", f[0], f[1]), K: f[0], V: f[1]})
	}
	if leaf == "" || m == nil {
		return out, m, got
	}
	if got.err != "" {
		out = append(out, UiDetail{Kind: "broken", Label: "cannot read: " + got.err})
	}
	out = append(out, UiDetail{Kind: "note"}, UiDetail{Kind: "head", Label: "outputs"})
	out = append(out, UiDetail{Kind: "output", Label: "console", Detail: got.source, Machine: m.Name()})
	now := m.now()
	byPath := map[string]map[string]any{}
	for _, f := range got.files {
		byPath[kit.Text(f["path"])] = f
	}
	type shown struct {
		t float64
		d UiDetail
	}
	var front []shown
	seen := map[string]bool{}
	for _, v := range got.views {
		p := v.View
		if !strings.HasPrefix(p, "/") {
			p = leaf + "/" + p
		} else if mark := "/" + path.Base(leaf) + "/"; strings.Contains(p, mark) && !strings.HasPrefix(p, leaf+"/") {
			p = leaf + "/" + strings.SplitN(p, mark, 2)[1]
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		d := UiDetail{Kind: "output", Label: v.Title, Machine: m.Name(), Path: p, T: v.T}
		if d.Label == "" {
			d.Label = path.Base(p)
		}
		f := byPath[p]
		if f != nil {
			d.Detail = sizeText(kit.Num(f["size"], 0)) + "  " + reach.Span(now-kit.Num(f["mtime"], now))
		}
		switch {
		case v.Static != "":
			d.Dir = true
			if url, ready := u.seenAt(row, m, leaf, p, v); url != "" {
				d.URL, d.Look, d.Ready = url, "static", ready
			} else {
				d.Look = "static" // ( on its way here )
			}
		case Rich(p):
			d.Look, d.Ready = "file", f != nil
		}
		front = append(front, shown{v.T, d})
	}
	var plain, own []UiDetail
	for _, e := range got.dirs { // what is in one is listed when it is opened
		name := kit.Text(e["name"])
		p := leaf + "/" + name
		if seen[p] || strings.HasPrefix(name, ".errand-") {
			continue
		}
		seen[p] = true
		d := UiDetail{Kind: "output", Label: name, Detail: "  " + reach.Span(now-kit.Num(e["mtime"], now)), Machine: m.Name(), Path: p, Dir: true}
		if strings.Contains(strings.Trim(name, "."), ".") { // a document that is a directory
			front = append(front, shown{kit.Num(e["mtime"], 0), d})
		} else {
			plain = append(plain, d)
		}
	}
	for _, f := range got.files {
		name, p := kit.Text(f["name"]), kit.Text(f["path"])
		if name == "output.txt" || seen[p] || strings.Contains(name, "/") {
			continue
		}
		d := UiDetail{Kind: "output", Label: name, Detail: sizeText(kit.Num(f["size"], 0)) + "  " + reach.Span(now-kit.Num(f["mtime"], now)), Machine: m.Name(), Path: p}
		switch {
		case Rich(name):
			front = append(front, shown{kit.Num(f["mtime"], 0), d})
		case ownFiles[name]:
			own = append(own, d)
		default:
			plain = append(plain, d)
		}
	}
	sort.SliceStable(front, func(i, j int) bool { return front[i].t < front[j].t })
	for _, s := range front {
		out = append(out, s.d)
	}
	out = append(out, plain...)
	if len(own) > 0 {
		out = append(out, UiDetail{Kind: "note"}, UiDetail{Kind: "head", Label: "errand's files"})
		out = append(out, own...)
	}
	out = append(out, UiDetail{Kind: "note"})
	out = append(out, console(row, got)...)
	return out, m, got
}

// console: a run's output as `errand run` drew it -- the end of it when it is long
func console(row *UiRow, got UiRead) []UiDetail {
	what, env, status, took := "", "", "", ""
	switch w := row.What.(type) {
	case *reach.TableRow:
		what, env = w.What, w.Env
	case *runinfo.PastRun:
		what, env, status = w.What, w.Env, w.Status
		if w.Duration != nil {
			took = "  " + reach.Span(*w.Duration)
		}
	}
	head := "▸ " + what
	if env != "" {
		head += "  " + env
	}
	out := []UiDetail{{Kind: "chead", Label: head}}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(got.out, "\t", "    "), "\n"), "\n")
	if len(lines) > 400 {
		out = append(out, UiDetail{Kind: "note", Label: fmt.Sprintf("  ┆ … %d lines before ( o: all of it )", len(lines)-400)})
		lines = lines[len(lines)-400:]
	}
	if got.out == "" {
		lines = []string{"( nothing yet )"}
		if status != "" {
			lines = []string{"( no output )"}
		}
	}
	for _, l := range lines {
		if i := strings.LastIndexByte(l, '\r'); i >= 0 && i < len(l)-1 { // what a progress bar last drew
			l = l[i+1:]
		}
		out = append(out, UiDetail{Kind: "out", Label: "  │ " + strings.TrimRight(l, "\r")})
	}
	if status != "" {
		out = append(out, UiDetail{Kind: "cend", Label: "  ╰ " + status + " " + what + took, Status: status})
	}
	return out
}

// EntryFile: the file of what a run ran, where it is on its machine
func EntryFile(row *UiRow) string {
	project, file := "", ""
	switch w := row.What.(type) {
	case *reach.TableRow:
		project, file = w.Project, w.File
		if file == "" {
			for _, word := range strings.Fields(w.What) {
				if strings.HasPrefix(word, "/") {
					return word
				}
			}
			if word := strings.Fields(w.What + " "); len(word) > 0 && strings.Contains(word[0], ".") {
				file = strings.SplitN(word[0], "::", 2)[0]
			}
		}
	case *runinfo.PastRun:
		project, file = w.Project, w.File
		if w.Far != nil { // it was brought back: the project is here
			if i := strings.Index(filepath.ToSlash(w.Leaf), "/errand-outputs/"); i >= 0 {
				project = w.Leaf[:i]
			}
		}
	}
	if file == "" || project == "" {
		return ""
	}
	if strings.HasPrefix(file, "/") {
		return file
	}
	return project + "/" + file
}

// can: what can be done on a row
func (u *Ui) can(row *UiRow, m *UiMachine) map[string]any {
	out := map[string]any{}
	if len(u.KillList(row)) > 0 {
		out["kill"] = true
	}
	switch row.Kind {
	case "authorized":
		out["withdraw"] = true
	case "refused":
		if r, ok := row.What.(*reach.RefusedRow); ok && r.Who == "pages" && len(r.Command) > 0 {
			out["authorize"] = true
		}
	case "tnode", "tset", "thead", "run":
		out["runs"] = true
	case "cache", "cachefs":
		out["caches"] = []string{"dead", "unused"}
		if row.Kind == "cachefs" {
			out["hold"] = true
		}
	case "centry":
		out["caches"] = []string{"flagged", "everywhere"}
	case "machine", "broken", "dormant":
		if m != nil {
			out["check"] = true
			out["rename"] = true
			if !m.Here() {
				out["forget"] = true
				switch {
				case m.M.Dormant():
					out["watch"] = "watch"
				case m.M.Gauges():
					out["watch"] = "gauges off"
				default:
					out["watch"] = "gauges on"
				}
			}
		}
	}
	return out
}

// ── doing ────────────────────────────────────────────────────────────────────

type killing struct {
	M    *UiMachine
	What string
	req  map[string]any
}

// KillList: what stopping a row stops
func (u *Ui) KillList(row *UiRow) []killing {
	m := u.machineOf(row)
	var out []killing
	switch w := row.What.(type) {
	case *reach.TableRow:
		out = append(out, killing{m, w.What, map[string]any{"op": "stop", "id": w.ID}})
	case *reach.ServingRow:
		if w.Ended > 0 {
			break
		}
		out = append(out, killing{m, "service " + w.Name, map[string]any{"op": "service_stop", "name": w.Name, "key": w.Key}})
	case *reach.LeaseRow:
		out = append(out, killing{m, "the port of " + w.Name + " ( given back )", map[string]any{"op": "lease_release", "id": w.ID}})
	case *reach.VmRow:
		out = append(out, killing{m, w.Text, map[string]any{"op": "vm_stop", "id": w.ID}})
	case *reach.KeptRow:
		if w.Kind == "unowned" {
			out = append(out, killing{m, fmt.Sprintf("%s ( pid %d )", w.Text, w.Pid), map[string]any{"op": "kill_group", "pid": w.Pid}})
		}
	default:
		if row.Kind == "machine" && m != nil && m.doc != nil {
			for _, r := range m.doc.Rows {
				if r.State != "ended" && !r.Aside {
					out = append(out, killing{m, r.What, map[string]any{"op": "stop", "id": r.ID}})
				}
			}
		}
	}
	return out
}

func (u *Ui) Kill(list []killing) string {
	for _, k := range list {
		k := k
		go func() {
			if _, err := u.Ask(k.M, k.req); err != nil {
				u.Said("not stopped: " + k.What + ": " + kit.FirstLine(err.Error()))
			}
		}()
	}
	if len(list) == 1 {
		return "stopping " + list[0].What
	}
	return fmt.Sprintf("stopping %d", len(list))
}

// EditNotes: what is kept of a machine, changed
func (u *Ui) EditNotes(m *UiMachine, how func(n *reach.MachineNote)) {
	for _, n := range m.M.Notes {
		how(&n)
		n.Write()
	}
	u.Mu.Lock()
	u.ReadAt = time.Time{}
	u.Mu.Unlock()
	u.refresh()
	u.Changed(true, nil)
}

func (u *Ui) Forget(m *UiMachine) string {
	for _, n := range m.M.Notes {
		_ = os.Remove(n.Path())
	}
	u.Mu.Lock()
	u.ReadAt = time.Time{}
	u.Mu.Unlock()
	u.refresh()
	u.Changed(true, nil)
	return m.Name() + " forgotten: not asked any more, until work is sent there again"
}

func (u *Ui) ToggleWatch(m *UiMachine) string {
	switch {
	case m.M.Dormant():
		u.EditNotes(m, func(n *reach.MachineNote) { n.Watch = "on" })
		return m.Name() + ": watched from now on"
	case m.M.Gauges():
		u.EditNotes(m, func(n *reach.MachineNote) { n.NoGauges = true })
		return m.Name() + ": its gauges are not measured any more"
	}
	u.EditNotes(m, func(n *reach.MachineNote) { n.NoGauges = false })
	return m.Name() + ": its gauges are measured"
}

// removal is runs that are to be removed ( ADR 0023 ): here, and where they ran.
type removal struct {
	Title string
	Lines []string
	runs  []*runinfo.PastRun
}

// runsUnder: the runs a row of the tree stands for
func runsUnder(row *UiRow) []*runinfo.PastRun {
	switch w := row.What.(type) {
	case *uiNode:
		return w.all()
	case *runinfo.PastRun:
		return []*runinfo.PastRun{w}
	}
	return nil
}

// Plan: what removing the rows `keys` name removes; `how`: "all", or "latest" to keep the most recent run of each
// parameter set, environment and machine, and the most recent that passed
func (u *Ui) Plan(keys []string, how string) (*removal, error) {
	var runs []*runinfo.PastRun
	seen := map[string]bool{}
	for _, k := range keys {
		row, err := u.find(k)
		if err != nil {
			return nil, err
		}
		for _, r := range runsUnder(row) {
			if !seen[r.Leaf] {
				seen[r.Leaf] = true
				runs = append(runs, r)
			}
		}
	}
	busy := map[string]bool{}
	for _, m := range u.snapshot() {
		if m.doc != nil {
			for _, r := range m.doc.Rows {
				if r.State != "ended" {
					busy[relOf(r.Leaf)] = true
				}
			}
		}
	}
	var gone []*runinfo.PastRun
	if how == "latest" || how == "D" {
		groups := map[string][]*runinfo.PastRun{}
		for _, r := range runs {
			k := fmt.Sprint(r.Host, r.File, r.Name, shownParams(r.Params), r.Env)
			groups[k] = append(groups[k], r)
		}
		for _, g := range groups {
			g = newest(g)
			passed := false
			for i, r := range g {
				if i == 0 || (!passed && r.Status == "PASS") {
					passed = passed || r.Status == "PASS"
					continue
				}
				gone = append(gone, r)
			}
		}
	} else {
		gone = runs
	}
	var kept []*runinfo.PastRun
	far := map[string]int{}
	for _, r := range gone {
		if busy[r.Rel] {
			continue
		}
		kept = append(kept, r)
		if r.Far != nil {
			far[r.Host]++
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("nothing to remove here")
	}
	p := &removal{Title: fmt.Sprintf("remove %d run%s?", len(kept), reach.Plural(len(kept))), runs: kept}
	for i, r := range kept {
		if i == 8 {
			p.Lines = append(p.Lines, fmt.Sprintf("... and %d more", len(kept)-8))
			break
		}
		p.Lines = append(p.Lines, r.Rel)
	}
	for host, n := range far {
		p.Lines = append(p.Lines, fmt.Sprintf("their directories on %s too, where %d ran", host, n))
	}
	return p, nil
}

// Measure: how much the runs of a removal take
func (u *Ui) Measure(p *removal) []string {
	var leaves []string
	for _, r := range p.runs {
		leaves = append(leaves, r.Leaf)
	}
	got, err := u.Ask(nil, map[string]any{"op": "runs_size", "leaves": leaves})
	if err != nil {
		return p.Lines
	}
	sizes, _ := got["sizes"].(map[string]any)
	total := 0.0
	for _, v := range sizes {
		total += kit.Num(v, 0)
	}
	return append(append([]string{}, p.Lines...), "", sizeText(total)+" here")
}

// Remove: they go -- shown greyed from here on, until they are gone
func (u *Ui) Remove(p *removal) {
	u.Mu.Lock()
	for _, r := range p.runs {
		u.going[r.Leaf] = true
	}
	u.Mu.Unlock()
	u.Changed(true, nil)
	var here []string
	far := map[string][]string{}
	hosts := map[string]*reach.HostSpec{}
	for _, r := range p.runs {
		if r.There == "" {
			here = append(here, r.Leaf)
		}
		if r.Far != nil && r.FarLeaf != "" {
			far[r.Far.Key()] = append(far[r.Far.Key()], r.FarLeaf)
			hosts[r.Far.Key()] = r.Far
		}
	}
	said := []string{}
	removed := 0
	if got, err := u.Ask(nil, map[string]any{"op": "runs_remove", "leaves": here}); err != nil {
		said = append(said, kit.FirstLine(err.Error()))
	} else {
		removed = len(kit.Strs(got["removed"]))
		if k := len(kit.Strs(got["kept"])); k > 0 {
			said = append(said, fmt.Sprintf("%d kept ( they run, or are not a run's )", k))
		}
	}
	for k, leaves := range far {
		l, err := u.link()
		if err == nil {
			_, err = l.Ask(map[string]any{"op": "at", "host": hosts[k], "req": map[string]any{"op": "runs_remove", "leaves": leaves}, "environ": os.Environ()})
		}
		if err != nil {
			said = append(said, hosts[k].Host+": "+kit.FirstLine(err.Error()))
		}
	}
	u.Mu.Lock()
	for _, r := range p.runs {
		delete(u.going, r.Leaf)
	}
	u.PastAt = time.Time{}
	u.pastSize = -1
	u.Mu.Unlock()
	u.Said(strings.TrimSpace(fmt.Sprintf("%d removed  %s", removed, strings.Join(said, "  "))))
	u.Changed(true, nil)
}

// Check: the runs other machines hold that are not here ( ADR 0025 )
func (u *Ui) Check() {
	var found []runinfo.PastRun
	have := map[string]bool{}
	for _, r := range u.runs() {
		if r.There == "" {
			have[r.Rel] = true
		}
	}
	n := 0
	for _, m := range u.snapshot() {
		if m.Here() || m.M.Dormant() {
			continue
		}
		got, err := u.Ask(u.real(m), map[string]any{"op": "history"})
		if err != nil {
			u.Said(m.Name() + ": " + kit.FirstLine(err.Error()))
			continue
		}
		b, _ := reach.JsonMarshal(got["runs"])
		var runs []runinfo.PastRun
		_ = kit.JsonUnmarshal(b, &runs)
		for _, r := range runs {
			if !have[r.Rel] && r.Far == nil {
				r.There, r.Far, r.FarLeaf = m.Name(), m.M.Spec(), r.Leaf
				found = append(found, r)
				n++
			}
		}
	}
	u.Mu.Lock()
	u.found = found
	u.Mu.Unlock()
	u.Said(fmt.Sprintf("%d run%s on other machines that are not here", n, reach.Plural(n)))
	u.Changed(true, nil)
}

// BringBack: a run another machine holds, brought here -- beside where this project's runs are, else under errand's cache
func (u *Ui) BringBack(r *runinfo.PastRun) {
	var m *UiMachine
	for _, x := range u.snapshot() {
		if x.Name() == r.There {
			m = u.real(x)
		}
	}
	if m == nil {
		u.Said("no machine " + r.There)
		return
	}
	to := filepath.Join(u.runsHere(r.Project), filepath.FromSlash(r.Rel))
	got, err := u.Ask(nil, map[string]any{"op": "bring", "host": m.M.Spec(), "from": r.FarLeaf, "to": to})
	if err != nil {
		u.Said("not brought back: " + kit.FirstLine(err.Error()))
		return
	}
	_ = got
	u.Mu.Lock()
	var rest []runinfo.PastRun
	for _, f := range u.found {
		if f.Rel != r.Rel {
			rest = append(rest, f)
		}
	}
	u.found, u.PastAt = rest, time.Time{}
	u.Mu.Unlock()
	u.Said(path.Base(r.Rel) + " brought back")
	u.Changed(true, nil)
}

// ── the storage ──────────────────────────────────────────────────────────────

// storageRows: what errand holds, by disk: its directories, the library caches, their entries
func (u *Ui) storageRows(q UiAsked) []UiRow {
	var rows []UiRow
	for _, m := range u.snapshot() {
		if m.M.Dormant() || (m.doc == nil && !m.Here()) {
			continue
		}
		real := u.real(m)
		u.Mu.Lock()
		stale := time.Since(real.storedAt) > 30*time.Second && !real.asking
		if stale {
			real.asking = true
		}
		stored := real.storage
		u.Mu.Unlock()
		if stale {
			var entries []string
			for k := range q.Opened {
				if e, ok := strings.CutPrefix(k, "entries:"); ok {
					entries = append(entries, filepath.Base(e))
				}
			}
			go func() {
				got, err := u.Ask(real, map[string]any{"op": "storage", "days": q.Days, "entries": entries})
				u.Mu.Lock()
				real.asking, real.storedAt = false, time.Now()
				if err == nil {
					real.storage, _ = got["storage"].(map[string]any)
				}
				u.Mu.Unlock()
				u.Changed(true, nil)
			}()
		}
		if stored == nil {
			rows = append(rows, UiRow{Key: rowKey("cnote", m.Name(), ""), Label: m.Name(), Detail: "asking…", Kind: "note", Machine: ptr(m.Name()), on: m})
			continue
		}
		look, _ := stored["storage"].(map[string]any)
		mounts, _ := look["mounts"].(map[string]any)
		var keys []string
		for k := range mounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			mt, _ := mounts[k].(map[string]any)
			fold := "mnt:" + m.Name() + " " + k
			open := !q.Folded[fold]
			label := fmt.Sprintf("%s  %s  %s", m.Name(), kit.Text(mt["point"]), kit.Text(mt["fstype"]))
			detail := ""
			if total := kit.Num(mt["total"], 0); total > 0 {
				detail = sizeText(kit.Num(mt["free"], 0)) + " free of " + sizeText(total)
			}
			rows = append(rows, UiRow{Key: rowKey("mount", m.Name(), k), Label: label, Detail: detail, Kind: "mount", Open: yes(open), Fold: ptr(fold), Machine: ptr(m.Name()), What: mt, on: m})
			if !open {
				continue
			}
			for _, x := range anyList(look["dirs"]) {
				d, _ := x.(map[string]any)
				if kit.Text(d["mount"]) != k || !matches(q.Query, m.Name(), kit.Text(d["path"]), kit.Text(d["label"])) {
					continue
				}
				detail := ""
				if d["size"] != nil {
					detail = sizeText(kit.Num(d["size"], 0))
				}
				rows = append(rows, UiRow{Key: rowKey("sdir", m.Name(), kit.Text(d["path"])), Label: kit.Text(d["label"]) + "  " + kit.Text(d["path"]), Detail: detail, Kind: "sdir", Depth: 1, Machine: ptr(m.Name()), What: d, on: m})
			}
			for _, x := range anyList(stored["dirs"]) {
				d, _ := x.(map[string]any)
				if kit.Text(d["mount"]) != k {
					continue
				}
				for _, y := range anyList(d["caches"]) {
					c, _ := y.(map[string]any)
					if !matches(q.Query, m.Name(), kit.Text(c["name"]), kit.Text(d["path"]), kit.Text(d["project"])) {
						continue
					}
					p := kit.Text(d["path"]) + "/" + kit.Text(c["name"])
					what := map[string]any{"path": p, "machine": m.Name(), "id": d["id"], "cache": c["name"], "project": d["project"]}
					for k, v := range c {
						if k != "list" {
							what[k] = v
						}
					}
					fold := "entries:" + p
					isOpen := q.Opened[fold]
					line := fmt.Sprintf("%.0f entries  %s", kit.Num(c["entries"], 0), sizeText(kit.Num(c["size"], 0)))
					mark := ""
					if n := kit.Num(c["dead"], 0); n > 0 {
						line += fmt.Sprintf(" · %.0f dead ( %s )", n, sizeText(kit.Num(c["dead_size"], 0)))
						mark = "lost"
					}
					if n := kit.Num(c["unused"], 0); n > 0 {
						line += fmt.Sprintf(" · %.0f unused ( %s )", n, sizeText(kit.Num(c["unused_size"], 0)))
					}
					rows = append(rows, UiRow{Key: rowKey("cache", m.Name(), p), Label: kit.Text(c["name"]) + "  " + path.Base(kit.Text(d["project"])), Detail: line, Kind: "cache", Mark: mark, Depth: 1,
						Open: yes(isOpen), Fold: ptr(fold), Machine: ptr(m.Name()), What: what, on: m})
					list, _ := c["list"].(map[string]any)
					if !isOpen {
						continue
					}
					var names []string
					for n, v := range list {
						e, _ := v.(map[string]any)
						if q.All || e["dead"] != nil || e["unused"] == true {
							names = append(names, n)
						}
					}
					sort.Strings(names)
					for _, n := range names {
						e, _ := list[n].(map[string]any)
						e["key"], e["id"], e["cache"], e["machine"] = n, d["id"], c["name"], m.Name()
						flag := ""
						if e["dead"] != nil {
							flag = "dead"
						} else if e["unused"] == true {
							flag = "unused"
						}
						rows = append(rows, UiRow{Key: rowKey("centry", m.Name(), p+" "+n), Label: n, Detail: strings.TrimSpace(sizeText(kit.Num(e["size"], 0)) + "  " + flag), Kind: "centry", Depth: 2, Machine: ptr(m.Name()), What: e, on: m})
					}
					if len(names) == 0 {
						rows = append(rows, UiRow{Key: rowKey("cnote", m.Name(), p+" none"), Label: "nothing dead or unused ( all: every entry )", Kind: "note", Depth: 2, Machine: ptr(m.Name()), on: m})
					}
				}
			}
		}
	}
	if len(rows) == 0 {
		rows = []UiRow{{Key: rowKey("cnote", "", "none"), Label: "nothing is held", Kind: "note"}}
	}
	return rows
}

// CacheRemoval: what of a cache a row removes
func (u *Ui) CacheRemoval(row *UiRow, how string, days float64) (string, []string, func() string, error) {
	w, _ := row.What.(map[string]any)
	m := u.machineOf(row)
	if w == nil || m == nil {
		return "", nil, nil, errors.New("on a cache, or an entry of one")
	}
	req := map[string]any{"op": "caches_remove", "id": w["id"], "cache": w["cache"], "days": days}
	what := kit.Text(w["cache"]) + " on " + m.Name()
	switch {
	case row.Kind == "centry":
		req["keys"] = []string{kit.Text(w["key"])}
		what = kit.Text(w["key"]) + " of " + what
	case how == "dead" || how == "unused":
		req["rule"] = how
		what = "what is " + how + " in " + what
	default:
		return "", nil, nil, errors.New("dead, or unused")
	}
	dry := map[string]any{}
	for k, v := range req {
		dry[k] = v
	}
	dry["dry_run"] = true
	got, err := u.Ask(m, dry)
	if err != nil {
		return "", nil, nil, err
	}
	lines := []string{fmt.Sprintf("%d entries, %s", len(anyList(got["removed"])), sizeText(kit.Num(got["freed"], 0)))}
	return "remove " + what + "?", lines, func() string {
		got, err := u.Ask(m, req)
		if err != nil {
			return kit.FirstLine(err.Error())
		}
		u.Mu.Lock()
		m.storedAt = time.Time{}
		u.Mu.Unlock()
		return fmt.Sprintf("%d entries removed, %s freed", len(anyList(got["removed"])), sizeText(kit.Num(got["freed"], 0)))
	}, nil
}
