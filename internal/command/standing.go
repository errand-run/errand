// The runs of this machine, from the command line ( the guide's "Let it run" and "What is running" ):
//
//	errand run --batch …     launch it, and give the shell back: prints its id
//	errand status            what runs here, what waits, what ended and was not looked at yet
//	errand attach [ID]       follow a run again, from its beginning, to its end
//	errand stop ID…          stop a run: INT, then TERM, then KILL, by its agent
//
// A run belongs to its place's agent ( ADR 0035 ); the side that started it knows which by a note of each run in
// errand's per-user directory ( `<cache>/runs/<id>.json` ), written when it starts, removed when it was followed to
// its end.

package command

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

func standingMain(t *reach.Term, verb string, args []string) int {
	cwd := t.Cwd
	switch verb {
	case "status":
		return statusMain(t, args)
	case "attach":
		var n runinfo.RunNote
		var err error
		if len(args) == 0 { // alone: the one run there is to follow
			all := runinfo.Notes(t)
			if len(all) != 1 {
				t.Sayf("errand attach: %d runs here: which one? ( `errand status` lists them )\n", len(all))
				return 2
			}
			n = all[0]
		} else if n, err = runinfo.NoteOf(t, args[0]); err != nil {
			t.Sayln("errand attach:", err)
			return 1
		}
		if _, collected := runinfo.ReadRecord(filepath.Join(n.Leaf, runinfo.ResultName)); n.Host != nil && collected {
			return replay(t, n, view{}, cwd)
		}
		return follow(t, n, view{}, n.Env, cwd, false)
	case "stop":
		return stopMain(t, args)
	case "forget":
		return forgetMain(t, args)
	}
	return 2
}

// project: whose runs `--mine` means -- the project one stands in
func projectOf(t *reach.Term) string {
	root, _ := project.FindRoot(t.Cwd)
	return root
}

// statusMain: what runs on every machine work was sent to, what is kept between commands there, and what ended and was
// not looked at
func statusMain(t *reach.Term, args []string) int {
	mine := false
	for _, a := range args {
		switch {
		case a == "--mine":
			mine = true
		case strings.HasPrefix(a, "--root"), a == "--all":
		default:
			t.Sayf("errand status: %s: not an option of it", a)
			return 2
		}
	}
	root := projectOf(t)
	said := reach.Gather(t, false)
	type line struct{ machine, id, what, age, state string }
	by := map[string][]line{}
	var order, names, dormant, after []string
	listed := map[string]bool{}
	for _, s := range said {
		if s.None {
			dormant = append(dormant, fmt.Sprintf("%s ( %s )", s.M.Name, reach.Ago(s.M.Used())))
			continue
		}
		if s.Err != nil {
			after = append(after, fmt.Sprintf("  %s: cannot say ( %s )", s.M.Name, kit.FirstLine(s.Err.Error())))
			continue
		}
		names = append(names, s.M.Name)
		now := s.Doc.Now
		for _, r := range s.Doc.Rows {
			if r.Aside || r.State == "ended" {
				continue
			}
			listed[r.ID] = true
			p := r.Project
			name := filepath.Base(filepath.ToSlash(p))
			if reach.SameProject(root, p) {
				name = filepath.Base(root)
			}
			if by[name] == nil {
				order = append(order, name)
			}
			what := r.What
			if r.Env != "" && r.Env != "local" {
				what += "  --env " + r.Env
			}
			by[name] = append(by[name], line{s.M.Name, reach.ShortID(r.ID), what, reach.Span(now - r.Started), reach.StateWords(r, now)})
		}
		for _, k := range s.Doc.Kept {
			what := k.Text
			if k.Kind == "unowned" {
				what = fmt.Sprintf("unowned: %s ( kill %d )", k.Text, k.Pid)
			}
			after = append(after, "  "+s.M.Name+": "+what)
		}
		served := map[string]bool{}
		for _, v := range s.Doc.Serving {
			served[v.Config] = true
			line := fmt.Sprintf("  %s: service %s %s  %s", s.M.Name, v.Name, v.State, v.More)
			if s.Doc.Shared {
				line = t.Red(line + "  ( a machine shared with others: it goes after two hours )")
			}
			after = append(after, line)
		}
		rests := 0
		for _, l := range s.Doc.Leases {
			if !served[l.Config] {
				rests++
			}
		}
		if rests > 0 {
			after = append(after, fmt.Sprintf("  %s: %d port%s kept, at rest ( errand serve )", s.M.Name, rests, reach.Plural(rests)))
		}
		for _, v := range s.Doc.VMs {
			after = append(after, fmt.Sprintf("  %s: %s %s", s.M.Name, v.Text, v.State))
		}
		n := 0
		for _, g := range s.Doc.Agents {
			if g.Retired {
				n++
			}
		}
		switch {
		case mine && len(s.Doc.Agents) > 0:
			after = append(after, fmt.Sprintf("  %s: %d agent%s", s.M.Name, len(s.Doc.Agents), reach.Plural(len(s.Doc.Agents))))
		case !mine:
			for _, g := range s.Doc.Agents {
				kind := g.Kind
				if kind == "" {
					kind = "machine"
				}
				more := fmt.Sprintf("idle %s", reach.Span(g.Idle))
				if g.Running > 0 {
					more = fmt.Sprintf("%d running", g.Running)
				}
				if g.Retired {
					more += ", retired"
				}
				after = append(after, fmt.Sprintf("  %s: agent ( %s, pid %d ) %s", s.M.Name, kind, g.Pid, more))
			}
		}
	}
	if len(order) > 0 {
		t.Printf("running   ( on %s )\n", strings.Join(names, ", "))
	} else {
		t.Printf("nothing runs   ( on %s )\n", strings.Join(names, ", "))
	}
	sort.Strings(order)
	for _, p := range order {
		if mine && p != filepath.Base(root) {
			ids := []string{}
			for _, l := range by[p] {
				ids = append(ids, l.id)
			}
			t.Printf("  %s: %d running   %s\n", p, len(by[p]), strings.Join(ids, " "))
			continue
		}
		t.Printf("  %s\n", p)
		wm, ww := 0, 0
		for _, l := range by[p] {
			wm, ww = max(wm, len([]rune(l.machine))), max(ww, len([]rune(l.what)))
		}
		for _, l := range by[p] {
			t.Printf("    %-*s  %s  %-*s  %-8s %s\n", wm, l.machine, l.id, min(ww, 60), l.what, l.age, l.state)
		}
	}
	for _, l := range after {
		t.Println(l)
	}
	if len(dormant) > 0 {
		t.Printf("  dormant, not asked: %s\n", strings.Join(dormant, ", "))
	}
	// what ended and was not looked at, what was launched and left
	first := true
	for _, n := range runinfo.Notes(t) {
		if listed[n.ID] {
			continue
		}
		st, more := n.State(t)
		if more != "" {
			st += ": " + more
		}
		where := n.Env
		if n.There != nil {
			where += "@" + n.There.Name
		}
		if first {
			t.Println("launched, or left to be looked at   ( errand attach ID )")
			first = false
		}
		t.Printf("  %s  %-20s  %s  %s  %s\n", reach.ShortID(n.ID), st, where, reach.Ago(n.Started), n.What)
	}
	return 0
}

// stopMain: runs stopped, by the agents that hold them -- by their ids, all those of this project, or chosen in a list
func stopMain(t *reach.Term, args []string) int {
	mine := false
	var ids []string
	for _, a := range args {
		if a == "--mine" {
			mine = true
		} else {
			ids = append(ids, a)
		}
	}
	type found struct {
		m reach.Machine
		r reach.TableRow
	}
	var all []found
	for _, s := range reach.Gather(t, false) {
		for _, r := range s.Doc.Rows {
			if r.State != "ended" && !r.Aside {
				all = append(all, found{s.M, r})
			}
		}
	}
	var chosen []found
	var noted []runinfo.RunNote
	code := 0
	switch {
	case mine:
		root := projectOf(t)
		for _, f := range all {
			if reach.SameProject(root, f.r.Project) {
				chosen = append(chosen, f)
			}
		}
	case len(ids) > 0:
		for _, id := range ids {
			var hits []found
			for _, f := range all {
				if strings.HasPrefix(f.r.ID, id) {
					hits = append(hits, f)
				}
			}
			switch {
			case len(hits) == 1:
				chosen = append(chosen, hits[0])
			case len(hits) > 1:
				t.Sayf("errand stop: %s is the beginning of %d runs' ids: say more of it", id, len(hits))
				code = 1
			default:
				n, err := runinfo.NoteOf(t, id)
				if err != nil {
					t.Sayln("errand stop:", err)
					code = 1
					continue
				}
				noted = append(noted, n)
			}
		}
		if code != 0 && len(chosen)+len(noted) == 0 {
			return code
		}
	default:
		if !t.TtyIn || !t.TtyOut {
			t.Sayln("errand stop ID…: the runs to stop, by the id `errand status` shows; --mine: every run of this project")
			return 2
		}
		if len(all) == 0 {
			t.Println("  nothing runs")
			return 0
		}
		for i, f := range all {
			t.Printf("  %2d  %s  %s  %s  %s\n", i+1, f.m.Name, reach.ShortID(f.r.ID), f.r.What, reach.StateWords(f.r, kit.Seconds(time.Now())))
		}
		answer, ok := t.Ask("  stop which? ( 1 3, 2-4, all; nothing: none ) ")
		if !ok {
			return 130
		}
		for _, w := range strings.Fields(strings.ReplaceAll(answer, ",", " ")) {
			from, to := 0, 0
			if w == "all" {
				from, to = 1, len(all)
			} else if a, b, ranged := strings.Cut(w, "-"); ranged {
				_, _ = fmt.Sscan(a, &from)
				_, _ = fmt.Sscan(b, &to)
			} else {
				_, _ = fmt.Sscan(w, &from)
				to = from
			}
			if from < 1 || to > len(all) || from > to {
				t.Sayf("errand stop: %s: not in the list", w)
				return 2
			}
			for i := from; i <= to; i++ {
				chosen = append(chosen, all[i-1])
			}
		}
	}
	if len(chosen)+len(noted) == 0 {
		t.Println("  nothing to stop")
	}
	for _, f := range chosen {
		code |= stopRun(t, runinfo.RunNote{ID: f.r.ID, Host: f.m.Spec()})
	}
	for _, n := range noted {
		code |= stopRun(t, n)
	}
	return code
}

// forgetMain: a run that is over dropped from the list, or every one that is; its results are kept, nothing is stopped
func forgetMain(t *reach.Term, args []string) int {
	if len(args) == 0 {
		t.Sayln("errand forget ID, or `errand forget finished`: every one that is over")
		return 2
	}
	code := 0
	for _, id := range args {
		var those []runinfo.RunNote
		if id == "finished" {
			for _, n := range runinfo.Notes(t) {
				if st, _ := n.State(t); st == "ended" {
					those = append(those, n)
				}
			}
		} else if n, err := runinfo.NoteOf(t, id); err != nil {
			t.Sayln("errand forget:", err)
			code = 1
		} else {
			those = append(those, n)
		}
		for _, n := range those {
			if st, _ := n.State(t); st != "ended" && st != "gone" && !n.Batch {
				t.Sayf("  %s  it runs: `errand stop %s` stops it", reach.ShortID(n.ID), reach.ShortID(n.ID))
				code = 1
				continue
			}
			if l, err := reach.LinkTo(t, n.Host); err == nil {
				_, _ = l.Ask(map[string]any{"op": "forget", "id": n.ID})
			}
			n.Forget()
			t.Printf("  %s  forgotten: %s\n", reach.ShortID(n.ID), n.What)
		}
	}
	return code
}

// stopRun: a run stopped, by the agent that holds it
func stopRun(t *reach.Term, n runinfo.RunNote) int {
	l, err := reach.LinkTo(t, n.Host)
	if err != nil {
		t.Sayf("  %s  %v\n", n.ID[:8], err)
		return 1
	}
	got, err := l.Ask(map[string]any{"op": "stop", "id": n.ID})
	switch {
	case err != nil:
		t.Sayf("  %s  %v\n", n.ID[:8], err)
		return 1
	case got["was"] != nil:
		t.Printf("  %s  it had ended\n", n.ID[:8])
	case got["stopped"] == true:
		t.Printf("  %s  stopped\n", n.ID[:8])
	default:
		t.Sayf("  %s  its agent did not say it stopped\n", n.ID[:8])
		return 1
	}
	return 0
}

// replay: a run that was elsewhere and came back, said from its directory here -- what it wrote, then how it ended;
// then it is forgotten: it was looked at
func replay(t *reach.Term, note runinfo.RunNote, o view, cwd string) int {
	r, ok := runinfo.ReadRecord(filepath.Join(note.Leaf, runinfo.ResultName))
	if !ok || r.Exit == nil {
		t.Sayf("errand: the run %s did not say how it ended: its record is %s", note.ID[:8], note.Leaf)
		return 1
	}
	read := func(name string) []byte {
		data, _ := os.ReadFile(filepath.Join(note.Leaf, runinfo.StreamNames[name]))
		return data
	}
	if o.draws(t) {
		t.Output(kit.OUT, read("both")) // ( both streams as they came: which wrote what is not kept )
	} else {
		t.Output(kit.OUT, read("out"))
		t.Output(kit.ERR, read("err"))
	}
	note.Forget()
	return ending(t, note, o, cwd, *r.Exit)
}
