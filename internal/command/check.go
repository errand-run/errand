package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

// `errand check`: what errand believes, against what is there -- the copies of projects and whose their files are, what the
// next push would do, the runs that are in one place only, what was removed by hand. It reads; `--fix` puts right what
// loses nothing ( a record of what is gone, a lock nobody holds ), `--bring` brings back the runs that are there only. A file errand did
// not write, a run: never removed.

// checked is one thing said of a machine.
type checked struct {
	Kind  string   `json:"kind"`
	Level string   `json:"level"` // ok | info | wrong
	Text  string   `json:"text"`
	Path  string   `json:"path,omitempty"`
	Lines []string `json:"lines,omitempty"`
	Fix   string   `json:"fix,omitempty"`  // what `--fix` would do; "": nothing is done of it
	Done  string   `json:"done,omitempty"` // what was done
	Count int      `json:"count,omitempty"`

	fix func() (string, error)
}

type checkedMachine struct {
	Name  string    `json:"name"`
	Error string    `json:"error,omitempty"`
	Found []checked `json:"found"`
}

const checkedLines = 10

func some(all []string) []string {
	if len(all) <= checkedLines {
		return all
	}
	return append(append([]string{}, all[:checkedLines]...), fmt.Sprintf("...and %d more", len(all)-checkedLines))
}

func checkMain(t *reach.Term, args []string) int {
	fix, bring, asJSON, all := false, false, false, false
	var names []string
	for _, a := range args {
		switch {
		case a == "--fix":
			fix = true
		case a == "--bring":
			bring = true
		case a == "--json":
			asJSON = true
		case a == "--all":
			all = true
		case a == "-v" || a == "--verbose":
		case strings.HasPrefix(a, "-"):
			t.Sayf("errand check: %s: not an option of it ( --fix, --bring, --all, --json )", a)
			return 2
		default:
			names = append(names, a)
		}
	}
	ms := reach.Ledger(reach.HereName())
	var wanted []reach.Machine
	for _, name := range names {
		found := false
		for _, m := range ms {
			if m.Named(name) {
				wanted, found = append(wanted, m), true
				break
			}
		}
		if !found {
			t.Sayf("errand check: %s: no such machine ( `errand machines` lists them; `here` is this one )", name)
			return 2
		}
	}
	var dormant []string
	if len(names) == 0 {
		for _, m := range ms {
			if m.Dormant() && !all {
				dormant = append(dormant, m.Name)
				continue
			}
			wanted = append(wanted, m)
		}
	}
	docs := map[string]*project.Document{} // the projects read, by their root
	var out []checkedMachine
	code := 0
	for _, m := range wanted {
		done := t.Working("work", "checking "+m.Name)
		got := checkMachine(t, m, docs)
		for i := range got.Found {
			c := &got.Found[i]
			if c.fix == nil || (c.Kind == "there" && !bring) || (c.Kind != "there" && !fix) {
				continue
			}
			said, err := c.fix()
			if err != nil {
				c.Text += " -- not put right: " + kit.FirstLine(err.Error())
				c.Level = "wrong"
				continue
			}
			c.Done = said
		}
		done()
		if got.Error != "" {
			code = 1
		}
		for _, c := range got.Found {
			if c.Level == "wrong" && c.Done == "" {
				code = 1
			}
		}
		out = append(out, got)
		if !asJSON {
			showChecked(t, got)
		}
	}
	if asJSON {
		if out == nil {
			out = []checkedMachine{}
		}
		b, _ := json.MarshalIndent(map[string]any{"v": 1, "machines": out, "dormant": dormant}, "", " ")
		t.Println(string(b))
		return code
	}
	if len(dormant) > 0 {
		t.Printf("  not asked, no work was sent there for a day: %s  ( --all, or by name )\n", strings.Join(dormant, ", "))
	}
	fixable, there := 0, 0
	for _, m := range out {
		for _, c := range m.Found {
			switch {
			case c.Done != "" || c.Fix == "":
			case c.Kind == "there":
				there += c.Count
			default:
				fixable++
			}
		}
	}
	if fixable > 0 {
		t.Println("  errand check --fix puts right what is marked so; it removes no file of yours, and no run")
	}
	if there > 0 {
		t.Printf("  errand check --bring MACHINE brings back the %d run%s\n", there, reach.Plural(there))
	}
	return code
}

func showChecked(t *reach.Term, m checkedMachine) {
	t.Println(m.Name)
	if m.Error != "" {
		t.Println("  " + t.Red("✗ "+m.Error))
		return
	}
	for _, c := range m.Found {
		mark := map[string]string{"ok": "✓ ", "info": "· ", "wrong": "✗ "}[c.Level]
		line := mark + c.Text
		if c.Level == "wrong" && c.Done == "" {
			line = t.Red(line)
		}
		switch {
		case c.Done != "":
			line += "  -- " + c.Done
		case c.Fix != "":
			line += "  ( " + map[bool]string{true: "--bring", false: "--fix"}[c.Kind == "there"] + ": " + c.Fix + " )"
		}
		t.Println("  " + line)
		for _, l := range c.Lines {
			t.Println("      " + l)
		}
	}
}

// checkMachine: what there is to say of one machine, and of what is here of it
func checkMachine(t *reach.Term, m reach.Machine, docs map[string]*project.Document) checkedMachine {
	out := checkedMachine{Name: m.Name, Found: []checked{}}
	add := func(c checked) { out.Found = append(out.Found, c) }
	l, err := reach.LinkTo(t, m.Spec())
	var seen agent.SeenHere
	if err == nil {
		var got map[string]any
		if got, err = l.Ask(map[string]any{"op": "check"}); err == nil {
			err = reach.DecodeInto(got["seen"], &seen)
		}
	}
	if err != nil {
		out.Error = kit.FirstLine(err.Error())
		return out
	}
	fixThere := func(what, path, done string) func() (string, error) {
		return func() (string, error) {
			got, err := l.Ask(map[string]any{"op": "check_fix", "what": what, "path": path})
			if err != nil {
				return "", err
			}
			if got["done"] != true {
				return "", fmt.Errorf("it is not as it was seen any more")
			}
			return done, nil
		}
	}
	// ── the copies of projects it holds
	copies := map[string]agent.SeenCopy{}
	for _, c := range seen.Copies {
		copies[c.Path] = c
		switch {
		case c.Gone:
			add(checked{Kind: "copy", Level: "wrong", Path: c.Path, Text: c.Path + ": a copy that is there no more, removed by hand",
				Fix: "its record goes; the next run makes the copy again", fix: fixThere("record", c.Record, "its record removed")})
			continue
		case c.Bare:
			add(checked{Kind: "copy", Level: "info", Path: c.Path, Text: c.Path + ": no push went to its end there: nothing in it is known to be errand's"})
		case c.Old:
			add(checked{Kind: "copy", Level: "wrong", Path: c.Path, Text: c.Path + ": its manifest is an older errand's, and does not say whose its files are: none is taken to be errand's",
				Lines: []string{"a push stops at the first one it would replace or remove; moving the copy aside ( mv ) starts anew"}})
		case c.Foreign > 0:
			add(checked{Kind: "copy", Level: "info", Path: c.Path, Text: fmt.Sprintf("%s: %d file%s there that errand did not write, and leaves", c.Path, c.Foreign, reach.Plural(c.Foreign))})
		}
		if c.Lock != "" {
			add(checked{Kind: "lock", Level: "wrong", Path: c.Path, Text: c.Path + ": a push's lock that is nobody's ( " + c.Lock + " )", Fix: "the lock goes", fix: fixThere("lock", c.Path, "removed")})
		}
		if c.Cut {
			add(checked{Kind: "cut", Level: "info", Path: c.Path, Text: c.Path + ": a push was cut; the next one goes on from there"})
		}
		if c.Aside > 0 {
			add(checked{Kind: "aside", Level: "info", Path: c.Path, Text: fmt.Sprintf("%s: what %d push%s moved aside is in .errand-copy-kept/, and goes after 30 days", c.Path, c.Aside, map[bool]string{true: "es", false: ""}[c.Aside > 1])})
		}
	}
	for _, b := range seen.Builds {
		text := fmt.Sprintf("%s: %d build director%s, %s", b.Path, b.Count, map[bool]string{true: "ies", false: "y"}[b.Count > 1], runinfo.Sized(b.Size))
		if b.Unused > 0 {
			text += fmt.Sprintf(", %d of them ( %s ) not used for 30 days: errand caches --unused 30", b.Unused, runinfo.Sized(b.Held))
		}
		add(checked{Kind: "builds", Level: "info", Path: b.Path, Text: text, Count: b.Count})
	}
	// ── what is here of it
	past := runinfo.PastRuns()
	checkFar(m, l, past, add)
	checkViews(m, l, add)
	nothing := func() checkedMachine {
		if len(out.Found) == 0 {
			add(checked{Kind: "ok", Level: "ok", Text: "nothing to say"})
		}
		return out
	}
	if m.Here() {
		return nothing()
	}
	// ── the projects here whose environments lead there
	for _, doc := range projectsOf(t, seen, docs, add) {
		done := map[string]bool{}
		names := make([]string, 0, len(doc.Envs))
		for name := range doc.Envs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			stack := doc.Envs[name].Stack
			if len(stack) == 0 || stack[0].Type() != "Ssh" {
				continue
			}
			h := reach.HostOf(stack[0])
			root := stack[0].String("root")
			if root == "" {
				root = doc.Root
			}
			if h.VM != nil || !leadsTo(m, h) || done[root] {
				continue
			}
			done[root] = true
			checkPush(t, m, h, l, doc, root, stack[0].Strings("also"), copies, add)
		}
	}
	return nothing()
}

func leadsTo(m reach.Machine, h reach.HostSpec) bool {
	for _, n := range m.Notes {
		if n.Spec().Key() == h.Key() {
			return true
		}
	}
	return false
}

// projectsOf: the projects of here that machine's copies come from, and the one the command is typed in
func projectsOf(t *reach.Term, seen agent.SeenHere, docs map[string]*project.Document, add func(checked)) []*project.Document {
	roots := []string{}
	if root, declared := project.FindRoot(t.Cwd); declared {
		roots = append(roots, root)
	}
	for _, c := range seen.Copies {
		if at := kit.Text(c.Origin["path"]); at != "" {
			roots = append(roots, at)
		}
	}
	var out []*project.Document
	had := map[string]bool{}
	for _, root := range roots {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
		if found, _ := filepath.Glob(filepath.Join(root, "errand-*.py")); had[root] || len(found) == 0 {
			continue
		}
		had[root] = true
		doc, known := docs[root]
		if !known {
			var err error
			if doc, err = declared(t, root); err != nil {
				add(checked{Kind: "project", Level: "wrong", Path: root, Text: root + ": its declarations cannot be read: " + kit.FirstLine(err.Error())})
				doc = nil
			}
			docs[root] = doc
		}
		if doc != nil {
			out = append(out, doc)
		}
	}
	return out
}

// checkPush: what the next push of a project would do there, and the runs of it that are there only
func checkPush(t *reach.Term, m reach.Machine, h reach.HostSpec, l *reach.Link, doc *project.Document, root string, also []string, copies map[string]agent.SeenCopy, add func(checked)) {
	sel, err := projectSelection(doc)
	if err == nil {
		var p *filesync.PushSeen
		if p, err = filesync.SeePush(l, doc.Root, root, sel, filepath.Join(doc.OutputsDir(), "state", "sync", h.Key()+".json"), h.Host); err == nil {
			add(pushSaid(doc.Root, root, p))
			if c, held := copies[p.Root]; held && !c.Gone {
				checkRuns(m, h, doc, c, add)
			}
		}
	}
	if err != nil {
		add(checked{Kind: "push", Level: "wrong", Path: root, Text: fmt.Sprintf("%s → %s: what a push would do cannot be said: %s", doc.Root, root, kit.FirstLine(err.Error()))})
	}
	for _, other := range also {
		plain, _ := filesync.NewSelection(runinfo.NotCopied, false, []filesync.Zone{{Pattern: "errand-outputs", Kind: "errand"}})
		to := path.Join(root, filepath.ToSlash(other))
		if p, err := filesync.SeePush(l, filepath.Join(doc.Root, other), to, plain, "", h.Host); err == nil {
			add(pushSaid(filepath.Join(doc.Root, other), to, p))
		}
	}
}

func pushSaid(from, to string, p *filesync.PushSeen) checked {
	c := checked{Kind: "push", Level: "ok", Path: p.Root, Count: len(p.Foreign)}
	what := []string{}
	if p.Send > 0 {
		what = append(what, fmt.Sprintf("sends %d file%s ( %s )", p.Send, reach.Plural(p.Send), runinfo.Sized(p.Bytes)))
	}
	if p.Remove > 0 {
		what = append(what, fmt.Sprintf("removes %d that errand wrote", p.Remove))
	}
	if p.RemovedThere > 0 {
		what = append(what, fmt.Sprintf("%d of those were removed there", p.RemovedThere))
	}
	if len(what) == 0 {
		what = append(what, "has nothing to do")
	}
	c.Text = fmt.Sprintf("%s → %s: the next push %s", from, to, strings.Join(what, ", "))
	if len(p.Foreign) > 0 {
		c.Level = "wrong"
		c.Text = fmt.Sprintf("%s → %s: the next push stops, %d file%s there would be lost", from, to, len(p.Foreign), reach.Plural(len(p.Foreign)))
		var all []string
		for _, f := range p.Foreign {
			how := "not written by errand"
			if f.Changed {
				how = "changed there since errand wrote it"
			}
			all = append(all, fmt.Sprintf("%s: %s ( %s )", f.Key, how, f.When))
		}
		c.Lines = some(all)
	}
	return c
}

// checkRuns: the runs in a copy that are not here, and that no command is to bring back
func checkRuns(m reach.Machine, h reach.HostSpec, doc *project.Document, c agent.SeenCopy, add func(checked)) {
	here := filepath.Join(doc.OutputsDir(), "runs")
	waiting := map[string]bool{} // left to be brought back by `errand attach`
	files, _ := filepath.Glob(filepath.Join(runinfo.NotesDir(), "*.json"))
	for _, f := range files {
		var n runinfo.RunNote
		if data, err := os.ReadFile(f); err == nil && kit.JsonUnmarshal(data, &n) == nil && n.There != nil {
			waiting[n.Leaf] = true
		}
	}
	var only []string
	for _, rel := range c.Runs {
		at := filepath.Join(here, filepath.FromSlash(rel))
		if !kit.Lexists(at) && !waiting[at] {
			only = append(only, rel)
		}
	}
	if len(only) == 0 {
		return
	}
	from := runinfo.RunsOf(c.Path)
	add(checked{Kind: "there", Level: "info", Path: from, Count: len(only), Lines: some(only),
		Text: fmt.Sprintf("%s: %d run%s there, not here ( removed here, or never brought back )", c.Path, len(only), reach.Plural(len(only))),
		Fix:  "brought back to " + here,
		fix: func() (string, error) {
			t := &reach.Term{Environ: os.Environ(), Emit: func(byte, []byte) {}}
			defer t.Close()
			l, err := reach.LinkTo(t, nil)
			if err != nil {
				return "", err
			}
			defer l.Close()
			got, err := l.Ask(map[string]any{"op": "bring_runs", "host": h, "from": from, "to": here, "runs": only})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%.0f brought back", kit.Num(got["brought"], 0)), nil
		}})
}

const askedAtOnce = 2000

// missingThere: which of these paths are not on a machine
func missingThere(l *reach.Link, paths []string) (map[string]bool, error) {
	out := map[string]bool{}
	for len(paths) > 0 {
		n := min(askedAtOnce, len(paths))
		got, err := l.Ask(map[string]any{"op": "check_there", "paths": paths[:n]})
		if err != nil {
			return nil, err
		}
		for _, p := range kit.Strs(got["missing"]) {
			out[p] = true
		}
		paths = paths[n:]
	}
	return out, nil
}

// checkFar: the runs here that say where they ran, when that directory is there no more
func checkFar(m reach.Machine, l *reach.Link, past []runinfo.PastRun, add func(checked)) {
	if m.Here() {
		return
	}
	var paths []string
	var those []runinfo.PastRun
	for _, r := range past {
		if r.Far != nil && r.FarLeaf != "" && leadsTo(m, *r.Far) {
			paths, those = append(paths, r.FarLeaf), append(those, r)
		}
	}
	missing, err := missingThere(l, paths)
	if err != nil {
		add(checked{Kind: "ran", Level: "wrong", Text: "where the runs ran cannot be looked at: " + kit.FirstLine(err.Error())})
		return
	}
	var gone []runinfo.PastRun
	var lines []string
	for _, r := range those {
		if missing[r.FarLeaf] {
			gone, lines = append(gone, r), append(lines, r.Rel)
		}
	}
	if len(gone) == 0 {
		return
	}
	add(checked{Kind: "ran", Level: "wrong", Count: len(gone), Lines: some(lines), Fix: "they are said to be here only",
		Text: fmt.Sprintf("%d run%s here said to be there as well, and not there any more", len(gone), reach.Plural(len(gone))),
		fix: func() (string, error) {
			for _, r := range gone {
				if err := r.LetGo(); err != nil {
					return "", err
				}
			}
			return "said to be here only", nil
		}})
}

// checkViews: the copies of views noted here, when they are there no more
func checkViews(m reach.Machine, l *reach.Link, add func(checked)) {
	if m.Here() {
		return
	}
	var paths []string
	var those []filesync.CopyNote
	for _, c := range filesync.CopiesKnown() {
		if c.There != "" && leadsTo(m, c.Host) {
			paths, those = append(paths, c.There), append(those, c)
		}
	}
	missing, err := missingThere(l, paths)
	if err != nil {
		return // ( said already, of the runs )
	}
	for _, c := range those {
		if c := c; missing[c.There] {
			add(checked{Kind: "view", Level: "wrong", Path: c.Path, Text: c.Path + ": its copy for services is there no more", Fix: "its note goes; it is copied again when it is asked for",
				fix: func() (string, error) { return "its note removed", os.Remove(c.File()) }})
		}
	}
}
