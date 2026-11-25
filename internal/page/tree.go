// The runs as a tree ( ADR 0023 ): the directories of the file's path, the file, what of it was run, its parameter sets as
// a table ( one line a set, a column a parameter, then its results, the status, date and duration of its last run, how
// many ), and their runs. A node is folded until opened, but one that holds a run going on is open until folded.

package page

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

type uiNode struct {
	name, kind, id string // kind: dir, file, entry, set
	children       map[string]*uiNode
	runs           []*runinfo.PastRun
	latest         float64
	busy           bool
	count          int
	busyRuns       map[string]bool
}

func treeID(logical string) string {
	sum := sha1.Sum([]byte(logical))
	return hex.EncodeToString(sum[:])[:12]
}

func (n *uiNode) child(kind, name, logical string) *uiNode {
	k := kind + "\x00" + name
	if n.children[k] == nil {
		n.children[k] = &uiNode{name: name, kind: kind, id: treeID(logical), children: map[string]*uiNode{}}
	}
	return n.children[k]
}

// runTree: the runs under their files, whichever root they were started from: each project's path from the roots' common
// directory is put before
func runTree(runs []runinfo.PastRun, busy map[string]bool) *uiNode {
	root := &uiNode{kind: "root", children: map[string]*uiNode{}}
	top := ""
	for i, r := range runs {
		p := filepath.ToSlash(r.Project)
		if i == 0 {
			top = p
			continue
		}
		for top != "" && p != top && !strings.HasPrefix(p, strings.TrimSuffix(top, "/")+"/") {
			top = strings.TrimSuffix(filepath.ToSlash(filepath.Dir(top)), "/")
			if top == "." {
				top = ""
			}
		}
	}
	home, _ := os.UserHomeDir()
	for i := range runs {
		r := &runs[i]
		file := r.File
		if file == "" {
			file = "?"
		}
		head := strings.Trim(strings.TrimPrefix(filepath.ToSlash(r.Project), top), "/")
		if strings.HasPrefix(file, "/") {
			file = strings.Replace(file, home+"/", "~/", 1)
		} else if head != "" {
			file = head + "/" + file
		}
		node, sofar := root, ""
		parts := strings.Split(strings.Trim(file, "/"), "/")
		for k, part := range parts {
			if sofar != "" {
				sofar += "/"
			}
			sofar += part
			kind := "dir"
			if k == len(parts)-1 {
				kind = "file"
			}
			node = node.child(kind, part, sofar)
		}
		if r.Name != "" {
			node = node.child("entry", r.Name, file+"::"+r.Name)
		}
		if len(r.Params) > 0 {
			b, _ := json.Marshal(shownParams(r.Params))
			node = node.child("set", string(b), file+"::"+r.Name+"::"+string(b))
		}
		node.runs = append(node.runs, r)
	}
	root.settle(busy)
	root.merge()
	return root
}

func shownParams(p map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range p {
		out[k] = shownValue(v)
	}
	return out
}

func shownValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'g', 4, 64)
	case string:
		return x
	case bool:
		return fmt.Sprint(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (n *uiNode) settle(busy map[string]bool) {
	n.busyRuns = busy
	n.count = len(n.runs)
	for _, r := range n.runs {
		n.latest = max(n.latest, r.Started)
		n.busy = n.busy || busy[r.Rel]
	}
	for _, c := range n.children {
		c.settle(busy)
		n.count += c.count
		n.latest = max(n.latest, c.latest)
		n.busy = n.busy || c.busy
	}
}

// merge: a directory with one child and no runs of its own is one row with it
func (n *uiNode) merge() {
	kids := map[string]*uiNode{}
	for k, c := range n.children {
		c.merge()
		for c.kind == "dir" && len(c.children) == 1 && len(c.runs) == 0 {
			for _, only := range c.children {
				only.name = c.name + "/" + only.name
				c = only
			}
		}
		kids[k] = c
	}
	n.children = kids
}

func (n *uiNode) all() []*runinfo.PastRun {
	out := append([]*runinfo.PastRun{}, n.runs...)
	for _, c := range n.children {
		out = append(out, c.all()...)
	}
	return out
}

// above: the nodes to open for the row `key` names to be shown
func (n *uiNode) above(key string) []string {
	var parts []any
	if json.Unmarshal([]byte(key), &parts) != nil || len(parts) != 3 {
		return nil
	}
	kind, _ := parts[0].(string)
	what, _ := parts[2].(string)
	var walk func(n *uiNode, above []string) []string
	walk = func(n *uiNode, above []string) []string {
		hit := false
		switch kind {
		case "tnode", "tset", "thead":
			hit = n.id == what
		case "work":
			for _, r := range n.runs {
				hit = hit || r.Rel == what || r.Leaf == what
			}
		}
		if hit {
			if kind == "work" || kind == "thead" {
				return append(above, n.id)
			}
			return above
		}
		for _, c := range n.children {
			next := above
			if n.id != "" {
				next = append(append([]string{}, above...), n.id)
			}
			if got := walk(c, next); got != nil {
				return got
			}
		}
		return nil
	}
	if got := walk(n, []string{}); got != nil {
		return got
	}
	return nil
}

func at(t, now float64) string {
	when, today := time.Unix(int64(t), 0), time.Unix(int64(now), 0)
	if when.Format("2006-01-02") == today.Format("2006-01-02") {
		return when.Format("15:04")
	}
	if today.Sub(when) < 300*24*time.Hour {
		return when.Format("2 Jan 15:04")
	}
	return when.Format("2 Jan 2006")
}

func newest(runs []*runinfo.PastRun) []*runinfo.PastRun {
	out := append([]*runinfo.PastRun{}, runs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out
}

func (u *Ui) isGoing(r *runinfo.PastRun) bool {
	u.Mu.Lock()
	defer u.Mu.Unlock()
	return u.going[r.Leaf]
}

func (u *Ui) pastRow(r *runinfo.PastRun, busy bool, depth int, now float64) UiRow {
	status, mark := r.Status, statusMark(r.Status)
	if busy {
		status, mark = "running", "run"
	}
	if r.There != "" {
		status, mark = "on "+r.There+" only", "there"
	}
	if u.isGoing(r) {
		status, mark = "removing", "going"
	}
	if status == "" {
		status = "lost"
	}
	took := ""
	if r.Duration != nil {
		took = fmt.Sprintf("%.1f s", *r.Duration)
	}
	host := r.Host
	if r.There != "" {
		host = r.There
	}
	label := strings.TrimRight(fmt.Sprintf("%s  %-12s %-12s %-7s %s", at(r.Started, now), shortName(host), r.Env, status, took), " ")
	if r.Counts != [3]int{} {
		label += fmt.Sprintf("  %d passed", r.Counts[0])
		if r.Counts[1] > 0 {
			label += fmt.Sprintf(", %d failed", r.Counts[1])
		}
	}
	return UiRow{Key: workKey(r.Leaf), Label: label, Kind: "run", Mark: mark, Depth: depth, Machine: ptr(host), What: r}
}

func (u *Ui) treeRows(root *uiNode, q UiAsked) []UiRow {
	var rows []UiRow
	now := kit.Seconds(time.Now())
	open := func(n *uiNode) bool {
		k := "t:" + n.id
		return q.Unfold || q.Query != "" || q.Opened[k] || (n.busy && !q.Folded[k])
	}
	gone := func(runs []*runinfo.PastRun) bool {
		for _, r := range runs {
			if !u.isGoing(r) {
				return false
			}
		}
		return len(runs) > 0
	}
	var walk func(n *uiNode, depth int)
	list := func(runs []*runinfo.PastRun, depth int) {
		for _, r := range newest(runs) {
			rows = append(rows, u.pastRow(r, root.busyRuns[r.Rel], depth, now))
		}
	}
	walk = func(n *uiNode, depth int) {
		var kids []*uiNode
		sets := map[string]*uiNode{}
		for _, c := range n.children {
			if c.kind == "set" {
				sets[c.id] = c
				continue
			}
			kids = append(kids, c)
		}
		sort.Slice(kids, func(i, j int) bool {
			if (kids[i].kind != "dir") != (kids[j].kind != "dir") {
				return kids[i].kind == "dir" // folders first
			}
			if kids[i].latest != kids[j].latest {
				return kids[i].latest > kids[j].latest
			}
			return kids[i].name < kids[j].name
		})
		for _, c := range kids {
			label := c.name
			if c.kind == "dir" && !strings.HasSuffix(label, "/") {
				label += "/"
			}
			o := open(c)
			detail, mark := fmt.Sprintf("%d run%s  %s", c.count, reach.Plural(c.count), reach.Span(now-c.latest)), ""
			if c.busy {
				mark = "run"
			}
			if gone(c.all()) {
				detail, mark = "removing…", "going"
			}
			rows = append(rows, UiRow{Key: rowKey("tnode", "", c.id), Label: label, Detail: detail, Kind: "tnode", Mark: mark, Depth: depth, Open: yes(o), Fold: ptr("t:" + c.id), Level: c.kind, What: c})
			if o {
				walk(c, depth+1)
			}
		}
		if len(sets) > 0 {
			rows = append(rows, u.setRows(n, sets, depth, q, now, open, list, root.busyRuns)...)
		}
		list(n.runs, depth)
	}
	walk(root, 0)
	return rows
}

// setRows: the parameter sets of an entry, as a table
func (u *Ui) setRows(n *uiNode, sets map[string]*uiNode, depth int, q UiAsked, now float64, open func(*uiNode) bool, list func([]*runinfo.PastRun, int), busy map[string]bool) []UiRow {
	type line struct {
		n     *uiNode
		last  *runinfo.PastRun
		cells []string
	}
	var lines []line
	var pnames, rnames []string
	add := func(names *[]string, name string) {
		for _, x := range *names {
			if x == name {
				return
			}
		}
		*names = append(*names, name)
	}
	ids := make([]string, 0, len(sets))
	for id := range sets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return sets[ids[i]].latest > sets[ids[j]].latest })
	results := map[string]map[string]any{}
	for _, id := range ids {
		s := sets[id]
		runs := newest(s.runs)
		lines = append(lines, line{n: s, last: runs[0]})
		names := make([]string, 0)
		for k := range runs[0].Params {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			add(&pnames, k)
		}
		for _, r := range runs {
			if len(r.Results) > 0 {
				results[id] = r.Results
				names = names[:0]
				for k := range r.Results {
					names = append(names, k)
				}
				sort.Strings(names)
				for _, k := range names {
					add(&rnames, k)
				}
				break
			}
		}
	}
	cols := append([]string{}, pnames...)
	for _, r := range rnames {
		cols = append(cols, "→"+r)
	}
	cols = append(cols, "status", "last", "took", "env", "runs")
	for i := range lines {
		l := &lines[i]
		for _, k := range pnames {
			l.cells = append(l.cells, shownValue(l.last.Params[k]))
		}
		for _, k := range rnames {
			l.cells = append(l.cells, shownValue(results[l.n.id][k]))
		}
		status := l.last.Status
		if busy[l.last.Rel] {
			status = "running"
		}
		took := ""
		if l.last.Duration != nil {
			took = fmt.Sprintf("%.1f s", *l.last.Duration)
		}
		l.cells = append(l.cells, status, at(l.last.Started, now), took, l.last.Env+"@"+l.last.Host, strconv.Itoa(len(l.n.runs)))
	}
	spec := ""
	for k := range q.Opened {
		if rest, ok := strings.CutPrefix(k, "s:"+n.id+"="); ok {
			spec = rest
		}
	}
	for c, name := range cols {
		if name == strings.TrimPrefix(spec, "-") && spec != "" {
			down := strings.HasPrefix(spec, "-")
			sort.SliceStable(lines, func(i, j int) bool {
				a, b := lines[i].cells[c], lines[j].cells[c]
				x, ex := strconv.ParseFloat(a, 64)
				y, ey := strconv.ParseFloat(b, 64)
				less := a < b
				if ex == nil && ey == nil {
					less = x < y
				}
				if down {
					return !less && a != b
				}
				return less
			})
		}
	}
	shown := make([]string, len(cols))
	widths := make([]int, len(cols))
	for c, name := range cols {
		shown[c] = name
		switch spec {
		case name:
			shown[c] += " ▲"
		case "-" + name:
			shown[c] += " ▼"
		}
		widths[c] = len([]rune(shown[c]))
		for _, l := range lines {
			widths[c] = max(widths[c], len([]rune(l.cells[c])))
		}
	}
	text := func(cells []string) string {
		var parts []string
		for c, v := range cells {
			parts = append(parts, v+strings.Repeat(" ", widths[c]-len([]rune(v))))
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	var spans [][3]any
	from := 0
	for c, name := range cols {
		spans = append(spans, [3]any{name, from, from + widths[c]})
		from += widths[c] + 2
	}
	rows := []UiRow{{Key: rowKey("thead", "", n.id), Label: text(shown), Kind: "thead", Depth: depth, Tree: n.id, Cols: spans, What: n}}
	for _, l := range lines {
		if len(l.n.runs) == 1 { // its only run: the line is the run, nothing to unfold
			row := u.pastRow(l.n.runs[0], busy[l.last.Rel], depth, now)
			row.Label, row.Tabled = text(l.cells), true
			rows = append(rows, row)
			continue
		}
		o := open(l.n)
		mark := statusMark(l.last.Status)
		if busy[l.last.Rel] {
			mark = "run"
		}
		rows = append(rows, UiRow{Key: rowKey("tset", "", l.n.id), Label: text(l.cells), Kind: "tset", Mark: mark, Depth: depth, Open: yes(o), Fold: ptr("t:" + l.n.id), What: l.n})
		if o {
			for _, r := range newest(l.n.runs) {
				rows = append(rows, u.pastRow(r, busy[r.Rel], depth+1, now))
			}
		}
	}
	return rows
}
