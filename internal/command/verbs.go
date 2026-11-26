// Commands that look, and commands that set up: `errand list`, `errand shell`, `errand init`, `errand agents`,
// `errand caches`.

package command

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/caches"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/tags"
)

// chosen: the environments a command's options choose, one run of nothing in each
func chosen(t *reach.Term, o runOptions) ([]aRun, int) {
	doc, err := declared(t, o.root)
	if err != nil {
		t.Sayln("errand:", err)
		return nil, 1
	}
	declared := map[string]map[string]any{}
	for name, e := range doc.Envs {
		declared[name] = e.Tags
	}
	dflt := doc.DefaultEnv
	if dflt == "" {
		dflt = "local"
	}
	choices, err := tags.Select(declared, tags.Asked{Envs: o.envs, Flags: o.flags, Expr: o.expr, Default: dflt})
	if err != nil {
		t.Sayln("errand:", err)
		return nil, 1
	}
	var out []aRun
	for _, c := range choices {
		out = append(out, aRun{t: t, o: o, doc: doc, cwd: t.Cwd, when: time.Now(), envName: c.Env, env: doc.Envs[c.Env], tags: c.Tags, only: len(choices) == 1})
	}
	return out, 0
}

// listMain: what targets designate, and the parameters each entry takes -- a dry run
func listMain(t *reach.Term, args []string) int {
	o, err := parseRun(args)
	if err != nil {
		t.Sayln("errand list:", strings.ReplaceAll(err.Error(), "errand run", "errand list"))
		return 2
	}
	if err := o.belongs("list"); err != nil {
		t.Sayln("errand:", err)
		return 2
	}
	places, code := chosen(t, o)
	if code != 0 {
		return code
	}
	for _, r := range places {
		var given []string
		if len(o.words) > 0 {
			given = runinfo.SplitTargets(o.words[0])
		} else {
			for _, target := range kit.Strs(r.doc.DefaultRun) {
				given = append(given, runinfo.SplitTargets(target)...)
			}
			r.cwd = r.doc.Root
			if len(given) == 0 {
				given = []string{"**::*"}
			}
		}
		if len(places) > 1 {
			t.Println("  " + r.envName)
		}
		var err error
		if given, err = runinfo.WholeGlobs(r.doc, r.cwd, given); err != nil {
			t.Sayln("errand list:", err)
			return 1
		}
		var held []string
		for _, target := range given {
			if strings.Contains(target, "::") {
				held = append(held, target)
				continue
			}
			argv, path, _ := target2(target, r)
			t.Printf("  %s  ( %s )\n", path, strings.Join(argv[:1], " "))
		}
		if len(held) == 0 {
			continue
		}
		order, files, runners, at, code := r.resolved(held)
		if code != 0 {
			return code
		}
		var lines []string
		built := map[string]bool{}
		for _, f := range order {
			for _, l := range runners[files[f].runner].Build {
				if l = agent.Fill([]string{l}, at.env.Tools, nil, at.about(runners[files[f].runner], f, files[f].info))[0]; !built[l] {
					built[l] = true
					lines = append(lines, l)
				}
			}
		}
		if len(lines) > 0 {
			if code := at.build(lines); code != 0 {
				return code
			}
		}
		toList, infos := map[string][]string{}, map[string]runinfo.FileInfo{}
		for _, f := range order {
			infos[f] = files[f].info
			if runners[files[f].runner].List != nil {
				toList[files[f].runner] = append(toList[files[f].runner], f)
			}
		}
		listed, params := map[string][]string{}, map[string]map[string]string{}
		for name, those := range toList {
			got, more, code := at.listing(name, runners[name], those, infos)
			if code != 0 {
				return code
			}
			for f, e := range got {
				listed[f] = e
			}
			for f, p := range more {
				if params[f] == nil {
					params[f] = map[string]string{}
				}
				for n := range p {
					params[f][n] = ""
				}
			}
		}
		for _, f := range order {
			a := files[f]
			t.Printf("  %s  ( %s )\n", f, a.runner)
			for _, e := range listed[f] {
				wanted := a.whole
				for _, n := range a.names {
					wanted = wanted || runinfo.NameMatches(n, e) || n == e || runners[a.runner].Run.Native
				}
				if wanted {
					t.Println("    " + f + "::" + e)
				}
			}
			if p := params[f]; len(p) > 0 {
				names := make([]string, 0, len(p))
				for n := range p {
					names = append(names, n)
				}
				sort.Strings(names)
				t.Println("    parameters: " + strings.Join(names, " "))
			}
		}
	}
	return 0
}

func target2(word string, r aRun) ([]string, string, string) {
	return runinfo.Target([]string{word}, r.doc.Root, r.cwd)
}

// shellMain: an interactive shell in an environment -- through its layers, over ssh, inside its container. Nothing of
// it is recorded.
func shellMain(t *reach.Term, args []string) int {
	o, err := parseRun(args)
	if err != nil {
		t.Sayln("errand shell:", err)
		return 2
	}
	if err := o.belongs("shell"); err != nil {
		t.Sayln("errand:", err)
		return 2
	}
	if len(o.words) > 0 {
		t.Sayf("errand shell: it takes no command: `errand run --env NAME %s` runs one", strings.Join(o.words, " "))
		return 2
	}
	places, code := chosen(t, o)
	if code != 0 {
		return code
	}
	if len(places) != 1 {
		t.Sayln("errand shell: one environment")
		return 2
	}
	r := places[0]
	r.o.quiet, r.o.plain, r.o.raw, r.o.noQueue = true, true, true, true
	// yours when the place has it
	shell := t.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	r.words = []string{"sh", "-c", `for s in "$1" "$2"; do if command -v "$s" >/dev/null 2>&1; then exec "$s" -i; fi; done; exec sh -i`, "sh", shell, filepath.Base(shell)}
	r.aside, r.what, r.only = "shell", "shell", true
	if len(r.env.Stack) > 0 && r.env.Stack[0]["windows"] != nil { // no terminal there: what is typed goes a line at a time
		r.words = []string{"cmd.exe"}
	} else if !t.TtyIn || !t.TtyOut { // what it is given to read is its script
		r.o.raw = false
		r.words = []string{"sh"}
	}
	code = r.run()
	if code == 130 {
		code = 0
	}
	return code
}

// ── init ─────────────────────────────────────────────────────────────────────

const envsStart = `"""Where this project runs, for this user on this machine. Not versioned.

An environment is a stack of layers ( micromamba, uv, Apptainer, Ssh, Slurm, ... ), read
left to right, outermost first. Uncomment and adapt.
"""
import errand

# errand.envs[ 'local' ] = errand.Env( [ errand.Micromamba( 'myproject', python = '3.13',
#                                                         requirements = 'requirements.txt' ) ] )
#
# errand.envs[ 'cluster' ] = errand.Env(
#     [ errand.Ssh( host = 'my-cluster', root = '~/errand/myproject' ),
#       errand.Slurm( partition = 'gpu', time = '00:30:00' ),
#       errand.Apptainer( image = 'containers/main.sif' ) ],
#     gpu = True )
#
# errand.default_env = 'local'      # used when nothing is asked for; else the first declared
`

const projectStart = `"""What this project does: how its work is run, and what a bare ` + "`errand run`" + ` means.

Written by ` + "`errand init`" + `; edit freely. Everything here is optional -- ` + "`errand run <command>`" + `
works without it. This file is versioned; where the project RUNS ( environments, which
depend on the user and the machine ) is ` + "`errand-envs.py`" + `, which is not.
"""
import errand

`

var importsErrand = regexp.MustCompile(project.Builtin()["errand"].Takes.Holds)

func initMain(t *reach.Term, args []string) int {
	force := false
	root := t.Cwd
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--force":
			force = true
		case args[i] == "--root" && i+1 < len(args):
			i++
			root = filepath.Join(t.Cwd, args[i])
			if filepath.IsAbs(args[i]) {
				root = args[i]
			}
		default:
			t.Sayf("errand init: %s: not an option of it", args[i])
			return 2
		}
	}
	if found, _ := filepath.Glob(filepath.Join(root, "errand-*.py")); len(found) > 0 && !force {
		t.Printf("  %s already exists here; init does not overwrite it ( errand init --force to replace it )\n", filepath.Base(found[0]))
		return 1
	}
	lines, targets, own := guessed(root)
	text := projectStart
	if len(lines) > 0 {
		text += "# What was recognised here. pytest, Catch2 and cargo are runners already: these lines\n# only matter if the defaults do not fit ( a directory, a build command ). Uncomment to keep.\n"
		for _, l := range lines {
			text += "# " + l + "\n"
		}
		text += "\n"
	}
	if len(lines) > 0 || own {
		if len(targets) == 0 {
			targets = []string{"**::*"}
		}
		text += "# What a bare `errand run` runs: targets from the project root.\n# errand.default_run = [ '" + strings.Join(targets, "', '") + "' ]\n"
	}
	if err := os.WriteFile(filepath.Join(root, "errand-project.py"), []byte(text), 0o644); err != nil {
		t.Sayln("errand init:", err)
		return 1
	}
	t.Println("  wrote errand-project.py    ( what the project does; versioned )")
	envs := filepath.Join(root, "errand-envs.py")
	if _, err := os.Stat(envs); err != nil {
		_ = os.WriteFile(envs, []byte(envsStart), 0o644)
		t.Println("  wrote errand-envs.py    ( where YOU run it; not versioned )")
	}
	for _, pattern := range []string{"errand-outputs/", "errand-envs.py", "errand-vars.py"} {
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			break
		}
		path := filepath.Join(root, ".gitignore")
		data, _ := os.ReadFile(path)
		known := false
		for _, l := range strings.Split(string(data), "\n") {
			l = strings.Trim(strings.TrimSpace(l), "/")
			known = known || l == strings.Trim(pattern, "/")
		}
		if known {
			continue
		}
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			data = append(data, '\n')
		}
		_ = os.WriteFile(path, append(data, (pattern+"\n")...), 0o644)
		t.Printf("  added %s to .gitignore\n", pattern)
	}
	return 0
}

// guessed: the suites there are under a directory, as the runner lines that would fit them, what a bare `errand run`
// would mean, and whether errand's own entries are there. Nothing is run: directories are listed, a few files read.
func guessed(root string) (lines, targets []string, own bool) {
	files := runinfo.ProjectFiles(root, nil)
	depth := func(f string) int { return strings.Count(f, "/") }
	pyDirs, cpp, cargo := map[string]bool{}, "", ""
	read := func(f string) string {
		data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		return string(data)
	}
	sort.SliceStable(files, func(i, j int) bool { return depth(files[i]) < depth(files[j]) })
	for _, f := range files {
		base, dir := filepath.Base(f), filepath.ToSlash(filepath.Dir(f))
		switch {
		case strings.HasSuffix(base, ".py"):
			if strings.HasPrefix(base, "errand-") {
				continue
			}
			if importsErrand.MatchString(read(f)) {
				own = true
			} else if depth(f) <= 2 && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")) && dir != "examples" && dir != "docs" {
				top := dir
				if at := strings.IndexByte(dir, '/'); at > 0 {
					top = dir[:at]
				}
				pyDirs[top] = true
			}
		case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".cpp") && cpp == "" && depth(f) <= 3:
			if strings.Contains(strings.ToLower(read(f)), "catch") {
				cpp = dir
			}
		case base == "Cargo.toml" && cargo == "" && depth(f) <= 2:
			cargo = f
		}
	}
	if len(pyDirs) > 0 {
		dirs := make([]string, 0, len(pyDirs))
		for d := range pyDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs { // a conventional directory says it all
			if d == "tests" || d == "test" {
				dirs = []string{d}
				break
			}
		}
		lines = append(lines, "errand.runners.replace( errand.Pytest( dirs = [ '"+strings.Join(dirs, "', '")+"' ] ) )")
		for _, d := range dirs {
			if d == "." {
				targets = append(targets, "**::*")
			} else {
				targets = append(targets, d+"/**::*")
			}
		}
	}
	if cpp != "" {
		build := ""
		if _, err := os.Stat(filepath.Join(root, cpp, "Makefile")); err == nil {
			build = ", build = 'make -C " + cpp + "'"
		}
		lines = append(lines, "errand.runners.replace( errand.Catch2( dir = '"+cpp+"'"+build+" ) )")
		if cpp == "." {
			targets = append(targets, "**::*")
		} else {
			targets = append(targets, cpp+"/**::*")
		}
	}
	if cargo != "" {
		arg := ""
		if cargo != "Cargo.toml" {
			arg = "manifest = '" + cargo + "' "
		}
		lines = append(lines, "errand.runners.replace( errand.Cargo( "+arg+") )")
	}
	seen := map[string]bool{}
	var kept []string
	for _, x := range targets {
		if !seen[x] {
			seen[x] = true
			kept = append(kept, x)
		}
	}
	return lines, kept, own
}

// ── agents ───────────────────────────────────────────────────────────────────

// agentsMain: what is kept between commands -- the agents, here and on the machines work was sent to; stopping them
func agentsMain(t *reach.Term, args []string) int {
	force, verb := false, ""
	var names []string
	for _, a := range args {
		switch {
		case a == "--force":
			force = true
		case verb == "" && (a == "stop" || a == "restart"):
			verb = a
		case strings.HasPrefix(a, "-"):
			t.Sayf("errand agents: %s: not an option of it", a)
			return 2
		default:
			names = append(names, a)
		}
	}
	if verb == "" && len(names) > 0 {
		t.Sayln("errand agents [stop [NAME]] [restart] [--force]")
		return 2
	}
	if verb == "" {
		for _, s := range reach.Once(reach.Gather(t, false)) {
			switch {
			case s.None:
				t.Printf("  %s  dormant, not asked\n", s.M.Name)
				continue
			case s.Err != nil:
				t.Printf("  %s  cannot say ( %s )\n", s.M.Name, kit.FirstLine(s.Err.Error()))
				continue
			case len(s.Doc.Agents) == 0:
				t.Printf("  %s  no agent\n", s.M.Name)
			}
			for _, g := range s.Doc.Agents {
				kind := g.Kind
				if kind == "" {
					kind = "machine"
				}
				state := fmt.Sprintf("idle %s of %s", reach.Span(g.Idle), reach.Span(g.Keep))
				if g.Running > 0 {
					state = fmt.Sprintf("%d running", g.Running)
				}
				if g.Retired {
					state += ", retired: it goes with its last job"
				}
				t.Printf("  %-16s %-10s pid %-7d up %-10s %s  ( release %s )\n", s.M.Name, kind, g.Pid, reach.Span(s.Doc.Now-g.Started), state, g.Release)
			}
			for _, k := range s.Doc.Kept {
				t.Printf("  %-16s %s\n", s.M.Name, k.Text)
			}
		}
		return 0
	}
	code := 0
	for _, m := range reach.Ledger(reach.HereName()) {
		wanted := len(names) == 0
		for _, n := range names {
			wanted = wanted || m.Named(n) || strings.Contains(m.Name, n)
		}
		if !wanted || m.Here() {
			continue
		}
		h := *m.Spec()
		h.Environ = t.Environ
		words := []string{"quit"}
		if force {
			words = append(words, "--force")
		}
		out, err := h.There(words...)
		if err != nil {
			t.Printf("  %s  %s\n", m.Name, kit.FirstLine(err.Error()))
			code = 1
			continue
		}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(l, "agent ") {
				t.Printf("  %s  %s\n", m.Name, l)
			}
		}
	}
	here := len(names) == 0
	for _, n := range names {
		here = here || n == "here" || n == reach.HereName()
	}
	if here { // this machine's last: one of them carries this command out, and goes when it is over
		for _, l := range quitHere(force, os.Getpid()) {
			t.Printf("  %s  %s\n", reach.HereName(), l)
		}
		if l, err := reach.LinkTo(t, nil); err == nil {
			got, _ := l.Ask(map[string]any{"op": "quit", "force": force, "after": true, "start": verb == "restart"})
			t.Printf("  %s  agent ( pid %d ) %s\n", reach.HereName(), os.Getpid(), kit.Text(got["said"]))
		}
	}
	return code
}

// quitHere: the agents of this machine told to go -> what each said; `but`: not that one
func quitHere(force bool, but int) []string {
	var out []string
	for _, dir := range reach.Others(reach.PlaceRoot(), "") {
		v := reach.VersionOf(dir)
		if v.Pid == but {
			continue
		}
		l, err := reach.LinkToAgent(dir)
		if err != nil {
			continue
		}
		got, err := l.Ask(map[string]any{"op": "quit", "force": force})
		l.Close()
		said := kit.Text(got["said"])
		if err != nil {
			said = err.Error()
		}
		kind := v.Kind
		if kind == "" {
			kind = "machine"
		}
		out = append(out, fmt.Sprintf("agent ( %s, pid %d ) %s", kind, v.Pid, said))
	}
	return out
}

// AgainMain: the agent of a directory, started again when the one that is there is gone
func AgainMain(args []string) int {
	if len(args) != 2 || args[0] != "--dir" {
		return 2
	}
	for until := time.Now().Add(2 * time.Minute); reach.AgentAlive(args[1]); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(until) {
			return 1
		}
	}
	conn, err := reach.ReachDaemon(args[1], "place")
	if err != nil {
		fmt.Fprintln(os.Stderr, "errand:", err)
		return 1
	}
	conn.Close()
	return 0
}

func QuitMain(args []string) int {
	for _, l := range quitHere(len(args) > 0 && args[0] == "--force", -1) {
		fmt.Println(l)
	}
	return 0
}

// cachesCommand: `errand caches [MACHINE…] [--dry-run] [--unused DAYS]`, `errand caches hold MACHINE [DIR…]`
func cachesCommand(t *reach.Term, args []string) int {
	dry := false
	var unused any
	var words []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dry-run":
			dry = true
		case args[i] == "--unused" && i+1 < len(args):
			i++
			days := 0.0
			if _, err := fmt.Sscan(args[i], &days); err != nil {
				t.Sayf("errand caches: --unused %s: a number of days", args[i])
				return 2
			}
			unused = days
		case strings.HasPrefix(args[i], "-"):
			t.Sayf("errand caches: %s: not an option of it", args[i])
			return 2
		default:
			words = append(words, args[i])
		}
	}
	hold := len(words) > 0 && words[0] == "hold"
	var dirs []string
	if hold {
		if len(words) < 2 {
			t.Sayln("errand caches hold MACHINE [DIR...]: which machine holds them ( `errand machines` names them )")
			return 2
		}
		words, dirs = words[1:2], words[2:]
	}
	if len(words) == 0 {
		words = []string{"here"}
	}
	ms := reach.Ledger(reach.HereName())
	code := 0
	for _, w := range words {
		var m *reach.Machine
		for i := range ms {
			if ms[i].Named(w) {
				m = &ms[i]
			}
		}
		if m == nil {
			t.Sayf("errand caches: %s: no such machine ( `errand machines` lists them; `here` is this one )", w)
			code = 1
			continue
		}
		l, err := reach.LinkTo(t, m.Spec())
		if err != nil {
			t.Printf("  %s  %s\n", m.Name, kit.FirstLine(err.Error()))
			code = 1
			continue
		}
		if hold {
			if len(dirs) == 0 {
				dirs = []string{""}
			}
			for _, d := range dirs {
				got, err := l.Ask(map[string]any{"op": "caches_hold", "id": d})
				if err != nil {
					t.Sayln("errand caches:", err)
					code = 1
					continue
				}
				for _, h := range caches.ListOf(got["held"]) {
					t.Printf("  %v: held by %s ( %v )\n", caches.ListOf(h)[0], m.Name, got["host"])
				}
			}
			continue
		}
		got, err := l.Ask(map[string]any{"op": "caches", "purge": true, "dry_run": dry, "unused": unused, "days": caches.UnusedDays})
		if err != nil {
			t.Sayln("errand caches:", err)
			code = 1
			continue
		}
		t.Println(m.Name)
		var held []map[string]any
		_ = reach.DecodeInto(got["dirs"], &held)
		if len(held) == 0 {
			t.Println("  no library cache seen here ( a run under errand writes its $ERRAND_CACHES down )")
		}
		for _, d := range held {
			t.Printf("  %s  %s\n", kit.Text(d["path"]), filepath.Base(kit.Text(d["project"])))
			those := caches.ListOf(d["caches"])
			if len(those) == 0 {
				t.Println("    no cache kept by errand's rules")
			}
			for _, x := range those {
				c, _ := x.(map[string]any)
				line := fmt.Sprintf("    %s: %.0f entries, %s", kit.Text(c["name"]), kit.Num(c["entries"], 0), caches.Human(kit.Num(c["size"], 0)))
				if n := kit.Num(c["dead"], 0); n > 0 {
					line += fmt.Sprintf(" · %.0f dead ( %s )", n, caches.Human(kit.Num(c["dead_size"], 0)))
				}
				if n := kit.Num(c["unused"], 0); n > 0 {
					line += fmt.Sprintf(" · %.0f unused ( %s )", n, caches.Human(kit.Num(c["unused_size"], 0)))
				}
				if done, ok := c["purged_now"].(map[string]any); ok {
					if n := len(caches.ListOf(done["removed"])); n > 0 {
						word := "removed"
						if dry {
							word = "would go"
						}
						line += fmt.Sprintf(" -- %d entries, %s %s", n, caches.Human(kit.Num(done["freed"], 0)), word)
					} else {
						line += " -- nothing to remove"
					}
				}
				t.Println(line)
			}
		}
	}
	return code
}

// envsJSON: everything the project declares, as one document
func envsJSON(t *reach.Term, doc any) {
	b, _ := json.MarshalIndent(doc, "", " ")
	t.Println(string(b))
}
