// What a file holds, run by its tool ( the guide's "Running a suite"; ADR 0036 ).
//
//	errand run 'tests/**::*'                 every test under tests/, a file per run
//	errand run tests/test_api.py::test_a     one, handed to the file's tool as it is written
//	errand run 'tests/test_api.py::grad_*'   those the tool lists whose name matches
//	errand run -j 8 tests/test_big.py::*     one file's tests, spread over eight runs at a time
//
// An entry is what a tool can name in a file; a run is one invocation of the tool, carrying a set of entries. This
// composes the sets: which files a target names and which runner takes each is asked of the agent of the machine that
// holds the copy ( runinfo/targets.go ); a file's entries go in one run; `-j N`, with fewer files than N, spreads a file's
// entries over several. The tool is asked what a file holds -- at the end of the environment, as a run of its own --
// only when that is needed: a name written as a pattern, or entries to spread.
//
// A runner is data ( `project.Runner` ): its words are filled here, and nothing of it is code.

package command

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
)

// aside: a terminal of its own for a run that is not drawn as it goes -- what it says goes to `emit`, nothing typed
// reaches it, and it shares the way to this machine's agent, which it never closes
func aside(t *reach.Term, emit func(kind byte, payload []byte), ttyOut bool) *reach.Term {
	ended := make(chan []byte)
	close(ended)
	cols, rows := t.Winsize()
	return &reach.Term{Cwd: t.Cwd, Environ: t.Environ, TtyOut: ttyOut, Emit: emit, Stdin: ended, Sigs: make(chan syscall.Signal, 4),
		Detach: make(chan struct{}), Gone: t.Gone, Answers: t.Answers, Here: t.Here, Cols: cols, Rows: rows, Tee: t.Tee}
}

// number: a value as a number when it is one
func number(text string) any {
	if v, err := strconv.ParseFloat(text, 64); err == nil {
		return v
	}
	return text
}

// isElsewhere: whether an environment is on another machine
func isElsewhere(stack []project.Layer) bool { return len(stack) > 0 && stack[0].Type() == "Ssh" }

// buildDir: where what this run's environment builds goes, as it is seen where it runs
func (r aRun) buildDir() string {
	if isElsewhere(r.env.Stack) {
		return r.doc.BuildDir(r.envName)
	}
	return r.doc.BuildHere(r.envName)
}

// about: what a file's own words are filled with ( `{build_dir}`, `{binary}`, `{near}`… )
func (r aRun) about(runner project.Runner, file string, info runinfo.FileInfo) map[string]string {
	stem := strings.TrimSuffix(path.Base(file), path.Ext(file))
	one := map[string]string{"build_dir": r.buildDir(), "stem": stem, "beside": path.Dir(file), "dir": path.Dir(file),
		"near": info.Near, "near_name": info.NearName, "file": file}
	if runner.Binary != "" {
		words := agent.Fill([]string{runner.Binary}, nil, nil, one)
		one["binary"] = path.Clean(words[0])
		if !strings.Contains(one["binary"], "/") {
			one["binary"] = "./" + one["binary"]
		}
	}
	return one
}

// asideRun: a run errand makes for itself in this run's environment -- a build, a listing --, its output kept -> what
// it wrote, on each stream, and its exit code
func (r aRun) asideRun(kind, what, doing string, words []string) (string, string, int) {
	t := r.t
	var out, said bytes.Buffer
	var mu sync.Mutex
	sub := aside(t, func(kind byte, payload []byte) {
		mu.Lock()
		defer mu.Unlock()
		switch kind {
		case kit.OUT:
			out.Write(payload)
		case kit.ERR:
			said.Write(payload)
		case kit.NOTE: // what errand itself has to say of it -- an environment that is built -- is said
			if bytes.HasPrefix(payload, []byte(`{"say"`)) {
				t.Emit(kit.NOTE, payload)
			}
		}
	}, false)
	done := make(chan struct{})
	go func() { // Ctrl-C stops it as it stops a run
		for {
			select {
			case s := <-t.Sigs:
				sub.Sigs <- s
			case <-done:
				return
			}
		}
	}()
	one := r
	one.t, one.o.quiet, one.o.batch, one.o.own, one.o.label = sub, true, false, false, ""
	one.words, one.aside, one.what, one.only = words, kind, what, true
	one.requires, one.redo, one.report, one.missing = nil, nil, nil, ""
	if kind != "build" {
		one.requires = r.requires
	}
	stop := t.Working("work", doing)
	code := one.run()
	stop()
	close(done)
	mu.Lock()
	defer mu.Unlock()
	return out.String(), said.String(), code
}

func lastOfText(text string, n int) string {
	if text = strings.TrimSpace(text); len(text) > n {
		text = "…" + text[len(text)-n:]
	}
	return text
}

// preparing: this run, where its environment is prepared -- its own list of layers, when it declares one
func (r aRun) preparing() (aRun, int) {
	if len(r.env.Prepare) == 0 {
		return r, 0
	}
	p := r
	p.env.Stack = r.env.Preparing()
	there := func(s []project.Layer) string {
		if len(s) == 0 || s[0].Type() != "Ssh" {
			return ""
		}
		return s[0].String("host") + "\x00" + s[0].String("root") + "\x00" + strings.Join(s[0].Strings("options"), "\x00")
	}
	if there(p.env.Stack) == there(r.env.Stack) {
		return p, 0
	}
	p.placed = nil
	if there(p.env.Stack) != "" {
		at, code := p.reach(p.env.Stack, p.tags)
		if at == nil {
			return p, code
		}
		p.placed = at
	}
	return p, 0
}

// carry: what was built where the environment is prepared, to where its runs go, when the two do not see the same copy
// ( ADR 0011 ): through here, only what changed, its dates kept
func (r aRun) carry(p aRun) error {
	if (p.placed == nil) == (r.placed == nil || r.placed.h == nil) && (p.placed == nil || p.placed == r.placed || p.placed.l == r.placed.l) {
		return nil
	}
	dir := r.doc.BuildDir(r.envName)
	here := filepath.FromSlash(r.doc.BuildHere(r.envName))
	if !filepath.IsAbs(here) {
		here = filepath.Join(r.doc.Root, here)
	}
	if p.placed != nil && p.placed.h != nil {
		if _, err := filesync.Pull(p.placed.l, path.Join(filepath.ToSlash(p.placed.root), path.Dir(dir)), filepath.Dir(here), []string{path.Base(dir)}, nil, false, p.placed.h.Host, r.t.Say); err != nil {
			return err
		}
	}
	if r.placed != nil && r.placed.h != nil {
		if _, err := os.Stat(here); err != nil {
			return nil
		}
		sel, _ := filesync.NewSelection(nil, false, nil)
		// ( the build directory is errand's: whatever is in it )
		if _, err := filesync.Push(r.placed.l, here, path.Join(filepath.ToSlash(r.placed.root), dir), sel, "", r.placed.h.Host, true, r.t.Say); err != nil {
			return err
		}
	}
	return nil
}

// build: what the runners of some files build, once for the command and the environment, where it is prepared; a
// build that fails fails the command, before anything runs
func (r aRun) build(lines []string) int {
	t := r.t
	p, code := r.preparing()
	if code != 0 {
		return code
	}
	for _, line := range lines {
		if !r.o.quiet {
			t.Say("  build: " + line)
		}
		out, said, code := p.asideRun("build", "build "+line, "building", runinfo.ShellWords(line))
		if code == 130 {
			return 130
		}
		if code != 0 {
			t.Sayf("errand: the build failed ( %s ):\n%s", line, lastOfText(out+said, 3000))
			return 1
		}
	}
	if err := r.carry(p); err != nil {
		t.Sayln("errand: what was built could not be carried to where it runs:", err)
		return 1
	}
	return 0
}

// listing: what the files of a runner hold, asked of its tool in the place the runs will go -> a file's entries, and
// the parameters they declare
func (r aRun) listing(name string, runner project.Runner, files []string, infos map[string]runinfo.FileInfo) (map[string][]string, map[string]map[string]bool, int) {
	t := r.t
	line, err := regexp.Compile(runner.List.Line)
	if err != nil {
		t.Sayf("errand: the runner %s lists with a pattern that cannot be read: %v", name, err)
		return nil, nil, 1
	}
	held, params := map[string][]string{}, map[string]map[string]bool{}
	groups := [][]string{files}
	if runner.List.Each {
		groups = nil
		for _, f := range files {
			groups = append(groups, []string{f})
		}
	}
	var tags []string
	if r.o.entryTags != "" && runner.Run != nil {
		tags = append(append(tags, runner.Run.Tags...), r.o.entryTags)
	}
	for _, group := range groups {
		one := r.about(runner, group[0], infos[group[0]])
		words := agent.Fill(runner.List.Words, r.env.Tools, map[string][]string{"files": group, "variant": infos[group[0]].Variant, "tags": tags}, one)
		r.requires, r.missing = runner.Requires, ""
		out, said, code := r.asideRun("list", "list "+name, fmt.Sprintf("asking %s what %d file(s) hold", name, len(group)), words)
		if code == 130 {
			return nil, nil, 130
		}
		found := false
		for _, l := range strings.Split(out, "\n") {
			m := line.FindStringSubmatch(strings.TrimRight(l, "\r"))
			if m == nil {
				continue
			}
			entry, file := m[line.SubexpIndex("name")], group[0]
			if at := line.SubexpIndex("file"); at >= 0 {
				file = ""
				for _, asked := range group { // the tool names a file from where it stands, which need not be the root
					if asked == m[at] || strings.HasSuffix(asked, "/"+m[at]) || strings.HasSuffix(m[at], "/"+asked) {
						file = asked
						break
					}
				}
			}
			if file == "" {
				continue
			}
			held[file], found = append(held[file], entry), true
			if at := line.SubexpIndex("params"); at >= 0 && m[at] != "" {
				if params[file] == nil {
					params[file] = map[string]bool{}
				}
				for _, w := range strings.Fields(m[at]) {
					n, _, _ := strings.Cut(w, "=")
					params[file][n] = true
				}
			}
		}
		if !found && (code != 0 || len(groups) == 1) {
			if code == 127 && runner.Missing != "" {
				t.Sayln("errand:", agent.Fill([]string{runner.Missing}, nil, nil, one)[0])
				return nil, nil, 1
			}
			tail := said
			if strings.TrimSpace(tail) == "" {
				tail = out
			}
			t.Sayf("errand: %s listed nothing in %s ( `%s`, exit %d ):\n%s", name, strings.Join(group, " "), strings.Join(words, " "), code, lastOfText(tail, 1500))
			return nil, nil, 1
		}
	}
	return held, params, 0
}

// axes: what follows the target, once per point of the grid asked on the parameters the entries declare
// ( `--grid=, … --n=1,2`; a command's other words are never rewritten )
func axes(rest []string, sep string, declared map[string]bool) [][]string {
	out := [][]string{nil}
	for i := 0; i < len(rest); i++ {
		name, value, glued := strings.Cut(rest[i], "=")
		if sep == "" || !declared[name] || (!glued && i+1 >= len(rest)) {
			for k := range out {
				out[k] = append(append([]string{}, out[k]...), rest[i])
			}
			continue
		}
		if !glued {
			i++
			value = rest[i]
		}
		var more [][]string
		for _, one := range out {
			for _, v := range strings.Split(value, sep) {
				more = append(more, append(append([]string{}, one...), name+"="+v))
			}
		}
		out = more
	}
	return out
}

// askedOf is what is asked of one file: everything it holds, or the names asked.
type fileAsked struct {
	runner string
	whole  bool
	names  []string // as typed, a pattern or not
	info   runinfo.FileInfo
}

// resolved: what the targets that ask for what files hold designate, in this run's environment -> the files in order,
// what is asked of each, the runners, the run with its place reached; or how the command ends
func (r aRun) resolved(held []string) ([]string, map[string]*fileAsked, map[string]project.Runner, aRun, int) {
	t, o, doc := r.t, r.o, r.doc
	stop := func(err error) ([]string, map[string]*fileAsked, map[string]project.Runner, aRun, int) {
		t.Sayln("errand run:", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return nil, nil, nil, r, 1
	}
	// the place: this machine's agent, or the other machine's once the copy there is as it is here
	at := &placed{root: doc.Root}
	if len(r.env.Stack) > 0 && r.env.Stack[0].Type() == "Ssh" {
		if r.placed == nil {
			got, code := r.reach(r.env.Stack, r.tags)
			if got == nil {
				return nil, nil, nil, r, code
			}
			r.placed = got
		}
		at = r.placed
	} else {
		l, err := reach.LinkTo(t, nil)
		if err != nil {
			return stop(err)
		}
		at.l = l
	}
	cwd := "" // where the command was typed, from the root: the same on both sides
	if rel, err := filepath.Rel(doc.Root, r.cwd); err == nil && !strings.HasPrefix(rel, "..") {
		cwd = filepath.ToSlash(rel)
	}
	runners := doc.Runners
	if runners == nil {
		runners = project.Builtin()
	}
	answer, err := at.l.Ask(map[string]any{"op": "resolve", "root": at.root, "cwd": cwd, "targets": held, "runners": runners,
		"only": o.runner, "exclude": doc.Settings.Exclude})
	if err != nil {
		return stop(err)
	}
	var took []runinfo.Taken
	if err := reach.DecodeInto(answer["taken"], &took); err != nil {
		return stop(fmt.Errorf("what the targets designate, as the agent said it, cannot be read: %v", err))
	}
	// a file's selection: everything, or the names asked -- a file named by two targets is one run, of both
	files := map[string]*fileAsked{}
	var order []string
	for _, tk := range took {
		runner := runners[tk.Runner]
		if runner.NotData != "" {
			return stop(fmt.Errorf("the runner %s cannot be read by this program: %s", tk.Runner, runner.NotData))
		}
		if runner.Run == nil {
			return stop(fmt.Errorf("%s: its runner, %s, does not say how its entries are run", strings.Join(tk.Files, " "), tk.Runner))
		}
		if o.entryTags != "" && len(runner.Run.Tags) == 0 {
			return stop(fmt.Errorf("-e filters errand's own entries, by the tags written on them: %s is %s's, whose filter is its own, written after the target", strings.Join(tk.Files, " "), tk.Runner))
		}
		for _, f := range tk.Files {
			a := files[f]
			if a == nil {
				a = &fileAsked{runner: tk.Runner, info: tk.Info[f]}
				files[f] = a
				order = append(order, f)
			}
			if len(tk.Names) == 0 {
				a.whole = true
			}
			a.names = append(a.names, tk.Names...)
		}
	}
	return order, files, runners, r, 0
}

// entries: the runs of the targets that ask for what files hold, in this run's environment -> them, or how the
// command ends
func (r aRun) entries(held, rest []string) ([]aRun, int) {
	t, o := r.t, r.o
	stop := func(err error) ([]aRun, int) {
		t.Sayln("errand run:", err)
		return nil, 1
	}
	order, files, runners, r, code := r.resolved(held)
	if code != 0 {
		return nil, code
	}
	// what is built first, once each, whoever needs it
	var lines []string
	built := map[string]bool{}
	for _, f := range order {
		runner := runners[files[f].runner]
		one := r.about(runner, f, files[f].info)
		for _, l := range runner.Build {
			// ( a line is the user's: only what is errand's in it is filled, where it stands )
			if l = agent.Fill([]string{l}, r.env.Tools, nil, one)[0]; !built[l] {
				built[l] = true
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > 0 {
		if code := r.build(lines); code != 0 {
			return nil, code
		}
	}
	// how many runs a file's entries are spread over: one, unless `-j` asks for more runs than there are files
	spread := 1
	if o.jobs > len(order) {
		spread = (o.jobs + len(order) - 1) / len(order)
	}
	pattern := func(a *fileAsked) bool {
		for _, n := range a.names {
			if runinfo.IsGlob(n) {
				return true
			}
		}
		return false
	}
	gridded := false
	for _, w := range rest {
		gridded = gridded || (o.grid != "" && strings.Contains(w, o.grid))
	}
	// what must be asked of the tools: a name written as a pattern, entries to spread, parameters a grid varies
	toList := map[string][]string{}
	for _, f := range order {
		a := files[f]
		runner := runners[a.runner]
		asks := pattern(a) && !a.whole && !runner.Run.Native
		switch {
		case runner.List != nil && (asks || (spread > 1 && a.whole) || (gridded && runner.Run.Params)):
			toList[a.runner] = append(toList[a.runner], f)
		case asks:
			return stop(fmt.Errorf("%s::%s: the runner %s cannot say what a file holds, so a name is handed to it as it is written, not as a pattern", f, strings.Join(a.names, ","), a.runner))
		}
	}
	listed, declared := map[string][]string{}, map[string]map[string]bool{}
	names := make([]string, 0, len(toList))
	for name := range toList {
		names = append(names, name)
	}
	sort.Strings(names)
	infos := map[string]runinfo.FileInfo{}
	for f, a := range files {
		infos[f] = a.info
	}
	for _, name := range names {
		got, params, code := r.listing(name, runners[name], toList[name], infos)
		if code != 0 {
			return nil, code
		}
		for f, entries := range got {
			listed[f] = entries
		}
		for f, p := range params {
			declared[f] = p
		}
	}
	// the sets, and their runs
	var runs []aRun
	for _, f := range order {
		a := files[f]
		runner := runners[a.runner]
		var sets [][]string // nil: the whole file
		sel, fromList := "", false
		switch {
		case a.whole && spread > 1 && len(listed[f]) > 1:
			n := min(spread, len(listed[f]))
			sets, fromList = make([][]string, n), true
			for i, e := range listed[f] {
				sets[i%n] = append(sets[i%n], e)
			}
		case a.whole:
			sets = [][]string{nil}
		default:
			var chosen []string
			seen := map[string]bool{}
			for _, n := range a.names {
				if !runinfo.IsGlob(n) || runner.Run.Native { // handed to the tool as it is written: it knows what it names
					if !seen[n] {
						seen[n] = true
						chosen = append(chosen, n)
					}
					continue
				}
				matched := false
				fromList = true
				for _, e := range listed[f] {
					if runinfo.NameMatches(n, e) {
						matched = true
						if !seen[e] {
							seen[e] = true
							chosen = append(chosen, e)
						}
					}
				}
				if !matched {
					there := listed[f]
					if len(there) > 8 {
						there = there[:8]
					}
					return stop(fmt.Errorf("'%s::%s' matches nothing -- there: %s", f, n, strings.Join(there, "  ")))
				}
			}
			sets = [][]string{chosen}
			sel = strings.Join(a.names, ",")
		}
		// the tool's words, all but the entries and what follows the target
		one := r.about(runner, f, a.info)
		var tags []string
		if o.entryTags != "" {
			tags = append(append(tags, runner.Run.Tags...), o.entryTags)
		}
		self := func(n string) []string { return []string{"{" + n + "}"} }
		run := *runner.Run
		run.Words = agent.Fill(runner.Run.Words, r.env.Tools, map[string][]string{"variant": a.info.Variant, "tags": tags, "entries": self("entries"),
			"args": self("args"), "exact": self("exact"), "report": self("report")}, one)
		missing := ""
		if runner.Missing != "" {
			missing = agent.Fill([]string{runner.Missing}, nil, nil, one)[0]
		}
		for _, args := range axes(rest, o.grid, declared[f]) {
			for i, set := range sets {
				entries, exact := agent.Told(run, f, set, fromList)
				m := r
				m.words = agent.Fill(run.Words, r.env.Tools, map[string][]string{"entries": entries, "exact": exact, "args": args}, nil)
				m.as, m.sel, m.report, m.requires, m.missing = f, sel, runner.Report, runner.Requires, missing
				m.redo = &runinfo.Redo{Run: run, File: f, Rest: args, Tools: r.env.Tools}
				m.what = f + "::*"
				if sel != "" {
					m.what = f + "::" + sel
				}
				shown := append([]string{}, args...)
				if len(sets) > 1 { // one of several runs of the same file: which, in its name and its directory's
					m.what += fmt.Sprintf("  %d/%d", i+1, len(sets))
					shown = append([]string{fmt.Sprintf("%dof%d", i+1, len(sets))}, shown...)
				}
				if len(args) > 0 {
					m.what += " " + strings.Join(args, " ")
				}
				m.shown = strings.Join(shown, " ")
				if runner.Run.Params { // its entries declare parameters: each is said by its name
					m.params = map[string]any{}
					var rest []string
					for _, w := range shown {
						if name, value, ok := strings.Cut(w, "="); ok && strings.HasPrefix(name, "--") {
							m.params[strings.ReplaceAll(name[2:], "-", "_")] = number(value)
						} else {
							rest = append(rest, w)
						}
					}
					m.shown = strings.Join(rest, " ")
				}
				runs = append(runs, m)
			}
		}
	}
	return runs, 0
}

// severalAtOnce: the runs of a command, `n` at a time ( `-j N` ). Each is shown whole when it ends, in the order they
// end: what several runs write at once is not mixed. Ctrl-C stops those that run, and starts no other.
func severalAtOnce(t *reach.Term, runs []aRun, n int) int {
	if _, err := reach.LinkTo(t, nil); err != nil { // one way to this machine's agent, shared by all
		t.Sayln("errand:", err)
		return 1
	}
	var mu sync.Mutex // one run shown at a time
	stopped := false
	active := map[int]*reach.Term{}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case s := <-t.Sigs:
				mu.Lock()
				stopped = true
				for _, sub := range active {
					select {
					case sub.Sigs <- s:
					default:
					}
				}
				mu.Unlock()
			case <-done:
				return
			}
		}
	}()
	if !runs[0].o.quiet {
		t.Sayf("  %d at a time", min(n, len(runs)))
	}
	slots := make(chan struct{}, n)
	codes := make([]int, len(runs))
	var wg sync.WaitGroup
	for i := range runs {
		slots <- struct{}{}
		mu.Lock()
		if stopped {
			mu.Unlock()
			<-slots
			codes[i] = 130
			continue
		}
		type said struct {
			kind    byte
			payload []byte
		}
		var kept []said
		var keptMu sync.Mutex
		sub := aside(t, func(kind byte, payload []byte) {
			if kind == kit.NOTE && bytes.HasPrefix(payload, []byte(`{"doing"`)) {
				return // what is shown while it lasts has nowhere to be shown
			}
			keptMu.Lock()
			kept = append(kept, said{kind, append([]byte{}, payload...)})
			keptMu.Unlock()
		}, t.TtyOut)
		active[i] = sub
		mu.Unlock()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			one := runs[i]
			one.t = sub
			codes[i] = one.run()
			mu.Lock()
			delete(active, i)
			keptMu.Lock()
			for _, s := range kept {
				t.Emit(s.kind, s.payload)
			}
			keptMu.Unlock()
			mu.Unlock()
			<-slots
		}(i)
	}
	wg.Wait()
	worst := 0
	for _, code := range codes {
		switch {
		case code == 130:
			return 130
		case code != 0 && code != 77:
			worst = 1
		}
	}
	return worst
}
