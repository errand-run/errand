// `errand run`: a command run in an environment, on this machine, and recorded ( the guide's "Running a command" ).
//
//	errand run [OPTIONS] TARGET [ARGS…]
//
// The first word after errand's options is the target; what follows it is the target's, as written. A `.py` file is
// run by the place's Python, any other file of the project is executed as it is, anything else is a command of the
// place. The output arrives as it is written, Ctrl-C stops the work, and the exit code is the command's.
//
// The run belongs to its place's agent ( ADR 0035 ), not to this command: this command asks for it, follows it, and
// may go.

package command

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/download"
	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/reach"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
	"github.com/errand-run/errand/internal/tags"
)

type runOptions struct {
	envs             []string
	flags            map[string][]string // a tag's flag ( `--fp 64` ), and the values asked for it
	expr             string              // -t
	grid             string              // what splits several values into several runs; "": a value is one value
	root, label      string
	own              bool // -o: a directory of its own for the run's files
	quiet, plain     bool
	verbose, noQueue bool
	noSetup          bool
	force            bool   // --force: what is in the way of a push, and is not errand's, is moved aside
	open             bool   // -O: each run's page opened in the browser as it starts
	raw              bool   // a shell: every key is its own
	out              string // --out: the run's directory under another tree, or as a pattern
	log              string // -l: its output goes there too
	mem              string // -m: its memory ceiling
	entryTags        string // -e: an expression over the tags written on entries
	time             string // --time: how long the run may take, for a batch system
	ranks            int    // --ranks: how many processes of the program
	exclusive, batch bool
	needs            queue.Needs
	pull             []string // --pull DIR: what else a run elsewhere brings back, from the project's root
	jobs             int      // -j N: how many runs of the command at a time
	runner           string   // -r NAME: the runner asked, whatever the files say
	words            []string // the targets, and what is theirs
}

// several: a value, split into several under `--grid=SEP`. Without a grid a value is one value; a comma where it
// cannot be one -- an environment's name, a tag's value -- is an error that says how to ask for the grid.
func (o *runOptions) several(what, value string) ([]string, error) {
	if o.grid != "" {
		return strings.Split(value, o.grid), nil
	}
	if strings.Contains(value, ",") {
		return nil, fmt.Errorf("%s %s: several values make several runs only under --grid ( `--grid=, %s %s` )", what, value, what, value)
	}
	return []string{value}, nil
}

// alone: the options that take no value
var alone = map[string]bool{"-o": true, "-q": true, "-O": true, "--open": true, "-x": true, "--exclusive": true, "--batch": true, "--plain": true, "--no-setup": true,
	"--no-queue": true, "--force": true, "-v": true, "--verbose": true}

// takesValue: whether the word after an option is its value
func takesValue(a string) bool {
	if strings.Contains(a, "=") || alone[a] {
		return false
	}
	return !(strings.HasPrefix(a, "-j") && len(a) > 2) // ( `-j4` )
}

// belongs: why an option was said that is another verb's
func (o runOptions) belongs(verb string) error {
	for _, one := range []struct {
		said  bool
		name  string
		takes string
	}{{o.batch, "--batch", ""}, {o.own, "-o", ""}, {o.out != "", "--out", ""}, {o.jobs != 0, "-j", ""}, {o.plain, "--plain", ""}, {o.log != "", "-l", ""},
		{o.mem != "", "-m", ""}, {o.time != "", "--time", ""}, {o.runner != "", "-r", "list"}, {o.entryTags != "", "-e", "list"}, {o.noQueue, "--no-queue", "shell"},
		{o.ranks != 0, "--ranks", "shell"}, {len(o.pull) > 0, "--pull", "shell"}} {
		if one.said && one.takes != verb {
			return fmt.Errorf("%s is not an option of `errand %s` ( it belongs to run )", one.name, verb)
		}
	}
	return nil
}

// parseRun: errand's options stand before the target; everything after it is handed to what runs, as written
func parseRun(args []string) (runOptions, error) {
	o := runOptions{flags: map[string][]string{}}
	for i := 0; i < len(args); i++ { // one separator serves the whole command, wherever it is said before the target
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") {
			break
		}
		sep, ok := strings.CutPrefix(a, "--grid=")
		if a == "--grid" && i+1 < len(args) {
			i++
			sep, ok = args[i], true
		} else if !ok && takesValue(a) {
			i++
		}
		if ok {
			if sep == "" {
				return o, errors.New("--grid=SEP: what splits several values, e.g. `--grid=,`")
			}
			o.grid = sep
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, glued := strings.Cut(a, "=")
		take := func() (string, error) {
			if glued {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s: a value is missing", a)
			}
			i++
			return args[i], nil
		}
		var err error
		var v string
		switch {
		case a == "--":
			o.words = args[i+1:]
			return o, nil
		case name == "--grid":
			_, err = take()
		case name == "--env":
			if v, err = take(); err == nil {
				var more []string
				more, err = o.several("--env", v)
				o.envs = append(o.envs, more...)
			}
		case name == "-t" || name == "--env-tags":
			o.expr, err = take()
		case name == "--root":
			o.root, err = take()
		case name == "--time":
			o.time, err = take()
		case name == "--ranks":
			if v, err = take(); err == nil {
				if _, serr := fmt.Sscan(v, &o.ranks); serr != nil || o.ranks < 1 {
					err = fmt.Errorf("--ranks %s: a number of processes, 1 or more", v)
				}
			}
		case name == "--pull":
			if v, err = take(); err == nil {
				o.pull = append(o.pull, strings.Trim(filepath.ToSlash(v), "/"))
			}
		case name == "-n" || name == "--label":
			o.label, err = take()
		case name == "-e" || name == "--entry-tags":
			o.entryTags, err = take()
		case name == "-r" || name == "--runner":
			o.runner, err = take()
		case name == "-j" || name == "--jobs" || (strings.HasPrefix(a, "-j") && len(a) > 2 && a[2] >= '0' && a[2] <= '9'):
			if name != "-j" && name != "--jobs" {
				v = a[2:]
			} else {
				v, err = take()
			}
			if err == nil && v == "auto" {
				o.jobs = runtime.NumCPU()
			} else if err == nil {
				if _, serr := fmt.Sscan(v, &o.jobs); serr != nil || o.jobs < 1 {
					err = fmt.Errorf("-j %s: how many runs at a time, 1 or more", v)
				}
			}
		case name == "--gpus" || name == "--cpus" || name == "--ram" || name == "--gpu-ram":
			if v, err = take(); err == nil {
				var one queue.Needs
				if one, _, _, err = kit.NeedsFrom([]string{name, v}); err == nil {
					switch name {
					case "--gpus":
						o.needs.GPUs = one.GPUs
					case "--cpus":
						o.needs.CPUs = one.CPUs
					case "--ram":
						o.needs.RAM = one.RAM
					default:
						o.needs.GPURam = one.GPURam
					}
				}
			}
		case a == "-x" || a == "--exclusive":
			o.exclusive = true
		case a == "--batch":
			o.batch = true
		case name == "--out":
			if o.out, err = take(); err == nil {
				o.own = true
			}
		case name == "-l" || name == "--log":
			o.log, err = take()
		case name == "-m" || name == "--mem":
			o.mem, err = take()
		case a == "-o":
			o.own = true
		case a == "-q":
			o.quiet = true
		case a == "-O" || a == "--open":
			o.open = true
		case a == "--plain":
			o.plain = true
		case a == "-v" || a == "--verbose":
			o.verbose = true
		case a == "--no-queue":
			o.noQueue = true
		case a == "--no-setup":
			o.noSetup = true
		case a == "--force":
			o.force = true
		case strings.HasPrefix(name, "--") && len(name) > 2: // a tag's flag: it always takes a value
			if v, err = take(); err == nil {
				o.flags[name[2:]], err = o.several(name, v)
			}
		case strings.HasPrefix(a, "-") && a != "-":
			return o, fmt.Errorf("%s: not an option of `errand run` in this program yet. What is meant for the command goes after its target, or after `--`", a)
		default:
			o.words = args[i:]
			return o, nil
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// targets: the first word, split on its commas ( `\,` is a comma that is only a comma ), and the files that follow
// it with its suffix: `errand run scripts/*.py`, the glob expanded by the shell, runs each of them. -> the targets,
// and what follows them, which is theirs.
func targets(words []string, cwd string) ([]string, []string) {
	var out []string
	one := ""
	first := words[0]
	for i := 0; i < len(first); i++ {
		switch {
		case first[i] == '\\' && i+1 < len(first) && first[i+1] == ',':
			one += ","
			i++
		case first[i] == ',':
			out = append(out, one)
			one = ""
		default:
			one += string(first[i])
		}
	}
	out = append(out, one)
	rest := words[1:]
	isFile := func(w string) bool {
		if !filepath.IsAbs(w) {
			w = filepath.Join(cwd, w)
		}
		st, err := os.Stat(w)
		return err == nil && !st.IsDir()
	}
	if suffix := filepath.Ext(out[len(out)-1]); len(out) == 1 && suffix != "" && isFile(out[0]) {
		for len(rest) > 0 && filepath.Ext(rest[0]) == suffix && isFile(rest[0]) {
			out, rest = append(out, rest[0]), rest[1:]
		}
	}
	return out, rest
}

// ── the run ──────────────────────────────────────────────────────────────────

// aRun is one run of a command: one target, in one environment, with the tags chosen.
type aRun struct {
	t       *reach.Term
	o       runOptions
	doc     *project.Document
	cwd     string
	when    time.Time // one stamp for the whole command, however many runs it turns into
	envName string
	env     project.Env
	tags    map[string]any
	words   []string // its target, and what is the target's
	only    bool     // the command is this run alone
	// a set of a file's entries ( entries.go ): what it is filed under, what tells it from its siblings, its tool's
	// report, the name it is shown by, and the place it runs in when the command reached it already
	as, sel, shown string
	report         *project.Report
	what           string
	placed         *placed
	redo           *runinfo.Redo  // what makes it again with some of its entries, when it asks for what it does not have ( agent/rerun.go )
	params         map[string]any // what tells it from its siblings, by name
	requires       *project.Requires
	missing        string
	aside          string  // a run errand makes for itself: a listing
	leaf           *string // where the run's directory is said, for whoever started it to look in
}

func runMain(t *reach.Term, args []string) int {
	o, err := parseRun(args)
	if err != nil {
		t.Sayln("errand run:", err)
		return 2
	}
	if len(o.words) == 0 && len(o.flags) > 0 {
		for flag, values := range o.flags {
			t.Sayf("errand run: --%s is not an option of errand; read as a tag's flag, it took `%s` for its value, and nothing is left to run. What is meant for the command goes after its target\n", flag, strings.Join(values, o.grid))
		}
		return 2
	}
	cwd := t.Cwd
	if len(o.words) > 0 && !strings.Contains(o.words[0], "::") {
		for _, target := range runinfo.SplitTargets(o.words[0]) {
			at := target
			if !filepath.IsAbs(at) {
				at = filepath.Join(cwd, at)
			}
			if st, err := os.Stat(at); err == nil && st.IsDir() {
				t.Sayf("%s is a directory: `%s/**::*` runs the entries under it", target, strings.TrimRight(target, "/"))
				return 1
			}
		}
	}
	doc, err := declared(t, o.root)
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	if v := t.Getenv("ERRAND_OPEN"); v != "" {
		o.open = o.open || v == "1"
	} else {
		o.open = o.open || doc.Settings.Open
	}
	var given []string     // the targets, as typed
	if len(o.words) == 0 { // a bare `errand run`: what the project says it runs, written from its root
		dflt := kit.Strs(doc.DefaultRun)
		if len(dflt) == 0 {
			t.Sayln("errand run: nothing to run. `errand run TARGET [ARGS…]`: a file of the project, a command, or `FILE::NAME` for what a file holds; `errand.default_run` says what a bare `errand run` runs")
			return 2
		}
		for _, target := range dflt {
			given = append(given, runinfo.SplitTargets(target)...)
		}
		o.words, cwd = []string{strings.Join(dflt, ",")}, doc.Root
	} else {
		given = runinfo.SplitTargets(o.words[0])
	}
	if given, err = runinfo.WholeGlobs(doc, cwd, given); err != nil {
		t.Sayln("errand run:", err)
		return 1
	}
	if o.log != "" {
		path := o.log
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.Cwd, path)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			t.Sayln("errand run: -l:", err)
			return 1
		}
		defer f.Close()
		t.Tee = f
	}
	// where: every environment that fits what was said, once per value of a tag asked for
	declaredTags := map[string]map[string]any{}
	known := map[string]bool{}
	for name, e := range doc.Envs {
		declaredTags[name] = e.Tags
		for t := range e.Tags {
			known[t] = true
		}
	}
	for flag := range o.flags {
		if !known[flag] {
			t.Sayf("errand run: --%s: not an option of errand, nor a tag of an environment of this project. What is meant for the command goes after its target\n", flag)
			return 2
		}
	}
	dflt := doc.DefaultEnv
	if dflt == "" {
		dflt = "local"
	}
	choices, err := tags.Select(declaredTags, tags.Asked{Envs: o.envs, Flags: o.flags, Expr: o.expr, Default: dflt})
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	// what the first word asks: `x::y` is what a file holds, for an entry runner; anything else is a file run whole,
	// or a command ( read from the command line alone, never guessed from the project )
	var whole, held []string
	for _, target := range given {
		if strings.Contains(target, "::") {
			held = append(held, target)
		} else {
			whole = append(whole, target)
		}
	}
	if o.runner != "" && len(held) == 0 {
		t.Sayf("errand run: -r %s picks the runner of what a file holds: `FILE::NAME`, `FILE::*` for all of it\n", o.runner)
		return 2
	}
	var names, rest []string
	if len(whole) > 0 {
		for i, w := range whole {
			whole[i] = strings.ReplaceAll(w, ",", `\,`)
		}
		names, rest = targets(append([]string{strings.Join(whole, ",")}, o.words[1:]...), cwd)
	} else {
		rest = o.words[1:]
	}
	when := time.Now()
	var runs []aRun
	for _, name := range names {
		for _, c := range choices {
			runs = append(runs, aRun{t: t, o: o, doc: doc, cwd: cwd, envName: c.Env, env: doc.Envs[c.Env], tags: c.Tags,
				words: append([]string{name}, rest...)})
		}
	}
	if len(held) > 0 {
		for _, c := range choices {
			base := aRun{t: t, o: o, doc: doc, cwd: cwd, when: when, envName: c.Env, env: doc.Envs[c.Env], tags: c.Tags, only: len(choices) == 1}
			sets, code := base.entries(held, rest)
			if code != 0 {
				return code
			}
			runs = append(runs, sets...)
		}
	}
	if len(runs) > 1 && !o.quiet { // said before anything starts, so that a grid nobody meant is seen in time
		var axes []string
		if len(names) > 1 {
			axes = append(axes, strings.Join(names, " | "))
		}
		seen := map[string]bool{}
		var envs []string
		for _, c := range choices {
			if !seen[c.Env] {
				seen[c.Env] = true
				envs = append(envs, c.Env)
			}
		}
		if len(envs) > 1 {
			axes = append(axes, "env = "+strings.Join(envs, " | "))
		}
		for flag, values := range o.flags {
			if len(values) > 1 {
				axes = append(axes, flag+" = "+strings.Join(values, " | "))
			}
		}
		if len(axes) == 0 { // the files of a target: each is named as it runs
			t.Sayf("  %d runs\n", len(runs))
		} else {
			t.Sayf("  %d runs: %s\n", len(runs), strings.Join(axes, ", "))
		}
	}
	worst := 0
	for i := range runs {
		// ( what a file holds is named by the file: where it runs is said with it only when there are several places )
		runs[i].when, runs[i].only = when, len(runs) == 1 || (runs[i].as != "" && len(choices) == 1)
	}
	leaves := make([]string, len(runs))
	for i := range runs {
		runs[i].leaf = &leaves[i]
	}
	if o.jobs > 1 && len(runs) > 1 {
		code := severalAtOnce(t, runs, o.jobs)
		if code != 130 {
			epilogue(t, o, runs)
		}
		return code
	}
	for i := range runs {
		code := runs[i].run()
		if code == 130 {
			return 130 // Ctrl-C stops the command, not one run of it
		}
		if code != 0 && code != 77 {
			worst = code
		}
		if len(runs) == 1 {
			return code
		}
	}
	epilogue(t, o, runs)
	if worst != 0 {
		return 1
	}
	return 0
}

// epilogue: what came of several runs, said once, at the end -- what failed, and why; what was skipped: a skip is not a
// pass, and a command that tested nothing must not look like one that passed
func epilogue(t *reach.Term, o runOptions, runs []aRun) {
	if o.quiet || o.batch {
		return
	}
	var failed, skipped []string
	for _, r := range runs {
		if r.leaf == nil || *r.leaf == "" {
			continue
		}
		res, ok := runinfo.ReadRecord(filepath.Join(*r.leaf, runinfo.ResultName))
		what := r.what
		if what == "" {
			what = strings.Join(r.words, " ")
		}
		why := ""
		if res.Error != nil {
			why = kit.FirstLine(*res.Error)
		}
		switch {
		case !ok || res.Status == "FAIL":
			failed = append(failed, "  FAILED "+what+"  "+why)
		case res.Status == "SKIP":
			skipped = append(skipped, "    "+what+"  "+why)
		}
	}
	t.Say(strings.Repeat("=", 46))
	if len(skipped) > 0 {
		t.Sayf("  %d skipped:\n%s", len(skipped), strings.Join(skipped, "\n"))
	}
	switch {
	case len(failed) > 0:
		t.Say(strings.Join(failed, "\n"))
	case len(skipped) > 0:
		t.Sayf("  all good  ( %d skipped )", len(skipped))
	default:
		t.Say("  all good")
	}
}

// needs: what a run takes of its machine -- what its environment says every run in it takes, and, over it, what the
// command line asks ( ADR 0014 )
func (r aRun) needs() queue.Needs {
	n := r.o.needs
	said := func(name string) string {
		switch v := r.env.Needs[name].(type) {
		case nil:
			return ""
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		default:
			return fmt.Sprint(v)
		}
	}
	for _, name := range []string{"gpus", "gpu_ram", "ram", "cpus"} {
		value := said(name)
		if value == "" {
			continue
		}
		one, _, _, err := kit.NeedsFrom([]string{"--" + strings.ReplaceAll(name, "_", "-"), value})
		if err != nil {
			continue
		}
		switch {
		case name == "gpus" && n.GPUs == 0:
			n.GPUs = one.GPUs
		case name == "gpu_ram" && n.GPURam.IsZero():
			n.GPURam = one.GPURam
		case name == "ram" && n.RAM == 0:
			n.RAM = one.RAM
		case name == "cpus" && n.CPUs == 0:
			n.CPUs = one.CPUs
		}
	}
	return n
}

// run: the run started, followed to its end -> its exit code
func (r aRun) run() int {
	t, o, doc, root := r.t, r.o, r.doc, r.doc.Root
	cwd := r.cwd
	if rel, err := filepath.Rel(root, cwd); err == nil && !strings.HasPrefix(rel, "..") {
		cwd = rel
	}
	label := o.label
	if label == "" {
		if label = strings.Join(r.words, " "); r.what != "" {
			label = r.what
		}
		if !r.only { // one of several: where, and with which values
			where := r.envName
			for flag := range o.flags {
				where += fmt.Sprintf(" %s=%v", flag, r.tags[flag])
			}
			label += "  · " + where
		}
	}
	params := map[string]any{}
	for flag := range o.flags { // a tag that was chosen tells two runs of a grid apart
		params[flag] = r.tags[flag]
	}
	for k, v := range r.params {
		params[k] = v
	}
	spec := runinfo.LaunchSpec{V: 1, Root: root, Outputs: doc.OutputsDir(), Cwd: cwd, Words: r.words, Env: r.envName,
		Stack: r.env.Stack, Tags: r.tags, Params: params, When: kit.Seconds(r.when), Label: label, Own: o.own, Needs: o.needs,
		Exclusive: o.exclusive, NoQueue: o.noQueue, Keep: o.batch, Batch: o.batch, NoSetup: o.noSetup, Verbose: o.verbose, Time: o.time, Ranks: o.ranks}
	if !o.batch { // nobody types to a run that is launched and left: pipes, its input ended
		spec.Tty = ttyOf(t, o.plain)
	}
	if r.as != "" || r.aside != "" { // what a file holds: started from the project's root, where its files are named from
		spec.As, spec.Sel, spec.Shown, spec.Report, spec.Aside, spec.Cwd = r.as, r.sel, r.shown, r.report, r.aside, ""
		spec.Redo = r.redo
	}
	spec.Out, spec.Mem, spec.Needs = o.out, o.mem, r.needs()
	if o.raw && spec.Tty != nil {
		spec.Tty.One, spec.Tty.Gutter, spec.Tty.Colors = true, 0, false
	}
	spec.BuildDir, spec.Src, spec.Requires, spec.Missing, spec.Tools = r.buildDir(), doc.Settings.Src, r.requires, r.missing, r.env.Tools
	if len(spec.Stack) > 0 && spec.Stack[0].Type() == "Ssh" {
		return r.runThere(spec)
	}
	spec.Environ = t.Environ // typed here, run here: in the surroundings it was typed in
	l, err := reach.LinkTo(t, nil)
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	if !o.noSetup { // an image that is built in another environment is made there, and brought here, first
		err := carryImages(t, doc, nil, l, map[string]any{"root": root, "outputs": doc.OutputsDir(), "tags": r.tags, "environ": t.Environ}, spec.Stack, false, o)
		if errors.Is(err, reach.ErrStopped) {
			return 130
		}
		if err != nil {
			t.Sayln("errand:", err)
			return 1
		}
	}
	answer, err := askLaunch(t, l, map[string]any{"op": "launch", "spec": spec}, o.quiet)
	if errors.Is(err, reach.ErrStopped) {
		return 130
	}
	var got runinfo.Launched
	if err == nil {
		if err = reach.DecodeInto(answer["launched"], &got); err == nil && got.Note.ID == "" {
			err = errors.New("this machine's agent did not say what it started")
		}
	}
	if err != nil {
		t.Sayln("errand:", strings.TrimPrefix(err.Error(), "this machine's agent: "))
		return 1
	}
	got.Note.Write()
	if got.Said != "" && !o.quiet {
		t.Say(got.Said)
	}
	if o.batch { // launched, and the shell given back: `errand attach` follows it again, `errand status` says how it goes
		t.Printf("%s  %s\n", got.Note.ID, label)
		if !o.quiet {
			t.Sayf("  launched: errand attach %s to follow it, errand status to see how it goes\n", got.Note.ID)
		}
		return 0
	}
	if r.leaf != nil {
		*r.leaf = got.Note.Leaf
	}
	return follow(t, got.Note, viewOf(o), got.Banner, r.cwd, true)
}

// askLaunch: a run asked for, and what is made ready for it said as it is made -- an image that is built first takes
// as long as it takes. Ctrl-C leaves it: the agent is told, and stops the step it is at. Whoever asked going without a
// word stops nothing: the agent goes on, and the run starts for nobody to follow.
func askLaunch(t *reach.Term, l *reach.Link, req map[string]any, quiet bool) (map[string]any, error) {
	return reach.AskHeld(t, l, req, quiet, nil)
}

// view is how a run is shown.
type view struct{ quiet, plain, raw, open bool }

func viewOf(o runOptions) view { return view{o.quiet, o.plain, o.raw, o.open} }

const openTabs = 4 // the pages one command opens; beyond, the main one, once

// openPage: a run's page, opened in the browser of whoever typed the command -> its address
func openPage(t *reach.Term, leaf string) string {
	t.SizeMu.Lock()
	t.Opened++
	n := t.Opened
	t.SizeMu.Unlock()
	args := []string{"run", runinfo.RunIdent(leaf)}
	switch {
	case n == openTabs+1:
		args = args[:1]
	case n > openTabs:
		return runinfo.PageOf(leaf)
	}
	cmd := exec.Command(kit.SelfPath(), append([]string{"web"}, args...)...)
	cmd.Env = t.Environ
	out, err := cmd.Output()
	if err != nil {
		return "the page does not come: " + kit.EndOf(err.Error(), 200)
	}
	return strings.TrimSpace(string(out))
}

// follow: a run followed from here on -- what it wrote and writes, as it writes it, then how it ended. `mine`: this
// command started it, and what this command was given to read is the run's to read.
func follow(t *reach.Term, note runinfo.RunNote, o view, banner, cwd string, mine bool) int {
	label, host := note.What, queue.Host()
	if note.There != nil {
		host = note.There.Name
	}
	drawn := o.draws(t)
	if drawn && !o.quiet {
		begin := draw.RunBegin{Host: host, Banner: banner, Label: label}
		if o.open {
			begin.Links = append(begin.Links, openPage(t, note.Leaf))
		} else if link := runinfo.PageOf(note.Leaf); link != "" {
			begin.Links = append(begin.Links, link)
		}
		begin.Links = append(begin.Links, "file://"+(&url.URL{Path: note.Leaf}).EscapedPath()+"/")
		t.Event(map[string]any{"begin": begin})
	}
	l, err := reach.LinkTo(t, note.Host)
	if err != nil {
		t.Sayln("errand:", err)
		return 1
	}
	feeds := mine && (!t.TtyIn || t.TtyOut) // what this command was given to read is the run's to read
	c := l.Open(map[string]any{"op": "follow", "id": note.ID, "both": drawn, "mine": feeds, "status": t.TtyOut && !o.quiet})
	defer c.Close()
	reply, err := c.Reply()
	if err != nil {
		t.Sayf("errand: the run %s cannot be followed: %v", note.ID[:8], err)
		return 1
	}
	interrupted := false
	over := make(chan struct{})
	defer close(over)
	defer func() { // what was shown of it goes with it
		for _, part := range []string{"wait", "step"} {
			t.Doing(part, "")
		}
	}()
	go func() { // Ctrl-C stops the work, wherever it runs; a second one, harder
		for {
			select {
			case <-t.Sigs:
				first := syscall.SIGINT
				if interrupted {
					first = syscall.SIGTERM
				}
				interrupted = true
				_ = c.SendJSON(kit.SIGNAL, map[string]any{"stop": true, "first": int(first)})
			case <-over:
				return
			}
		}
	}()
	// A run that has a terminal reads what is typed on this one, and its window is this one's
	typing := t.TtyIn && t.TtyOut && reply["tty"] == true
	if typing {
		t.Event(map[string]any{"keys": true, "raw": o.raw})
		defer t.Event(map[string]any{"keys": false})
		cols, rows := t.Winsize()
		if cols > 0 {
			size := func(cols, rows int) {
				_ = c.SendJSON(kit.RESIZE, map[string]any{"cols": max(1, cols-gutterOf(drawn)), "rows": rows})
			}
			size(cols, rows)
			defer t.OnResize(size)()
		}
	} else if t.TtyOut && reply["tty"] == true { // typed to nobody, but the window is still this one's
		if cols, rows := t.Winsize(); cols > 0 {
			size := func(cols, rows int) {
				_ = c.SendJSON(kit.RESIZE, map[string]any{"cols": max(1, cols-gutterOf(drawn)), "rows": rows})
			}
			size(cols, rows)
			defer t.OnResize(size)()
		}
	}
	if (feeds && !t.TtyIn) || typing {
		go func() {
			for {
				select {
				case data, more := <-t.Stdin:
					if !more {
						_ = c.Send(kit.EOF, nil)
						return
					}
					_ = c.Send(kit.IN, data)
				case <-over:
					return
				}
			}
		}()
	} else if mine {
		_ = c.Send(kit.EOF, nil)
	}
	got := make(chan reach.LinkMsg)
	go func() {
		defer close(got)
		for {
			kind, payload, err := c.Recv()
			if err != nil {
				return
			}
			select {
			case got <- reach.LinkMsg{Kind: kind, Payload: payload}:
			case <-over:
				return
			}
		}
	}()
	code, ended, lost := 1, false, ""
	var again []runinfo.RunNote // what was made after it, when it asked for what it did not have ( agent/rerun.go )
	for !ended && lost == "" {
		var kind byte
		var payload []byte
		select {
		case m, more := <-got:
			if !more {
				lost = "the connection to " + whereOf(note) + " was lost"
				continue
			}
			kind, payload = m.Kind, m.Payload
		case <-t.Gone: // whoever followed it went, without a word: the run is its agent's, and stays to be looked at
			return 1
		case <-t.Detach: // asked to let go of it: it goes on, with its input as it is, and `errand attach` follows it again
			_ = c.SendJSON(kit.SIGNAL, map[string]any{"keep": true})
			t.Sayf("errand: detached. The run goes on, over there if it is: `errand attach %s` follows it again", note.ID[:8])
			return 0
		}
		switch kind {
		case kit.OUT, kit.ERR:
			t.Output(kind, payload)
		case kit.NOTE:
			var n map[string]any
			_ = json.Unmarshal(payload, &n)
			if what, ok := n["waiting"].(string); ok {
				switch {
				case t.TtyOut && what != "": // a wait is under way: shown while it lasts, and gone when it is over
					t.Doing("wait", "waiting for "+what)
				case t.TtyOut:
					t.Doing("wait", "")
				case what != "":
					t.Say("  waiting for " + what)
				}
			}
			if step, ok := n["step"]; ok && t.TtyOut {
				var st draw.StepState
				if reach.DecodeInto(step, &st) == nil {
					t.Event(map[string]any{"doing": draw.Doing{Part: "step", Step: &st}})
				}
			}
			if text, _ := n["say"].(string); text != "" && !o.quiet {
				t.Say(text)
			}
			lost, _ = n["lost"].(string)
			if more, ok := n["again"]; ok {
				var made []runinfo.Launched
				if reach.DecodeInto(more, &made) == nil {
					for _, m := range made {
						again = append(again, note.Carried(m.Note))
					}
				}
			}
			if w, ok := download.WantFrom(n["want"]); ok && note.Host != nil { // the machine it runs on has no way to a URL
				go download.RelayWant(t, l, note, w)
			}
		case kit.EXIT:
			var e map[string]any
			_ = json.Unmarshal(payload, &e)
			code, ended = int(kit.Num(e["code"], 1)), true
		}
	}
	if !ended { // the way to it broke, not the run: its agent holds it
		t.Sayf("errand: %s. The run goes on if it was running: `errand attach %s` follows it again", lost, note.ID[:8])
		return 1
	}
	if note.Host != nil { // what it wrote, brought back before anything is said of it
		if err := runinfo.Collect(t, l, note, o.quiet); err != nil {
			t.Sayf("errand: the run ended ( exit %d ), and its files could not be brought back: %v\n  `errand attach %s` tries again", code, err, note.ID[:8])
			return 1
		}
	} else {
		_, _ = l.Ask(map[string]any{"op": "forget", "id": note.ID}) // looked at: its agent keeps nothing of it
	}
	note.Forget()
	said := ending(t, note, o, cwd, code)
	if interrupted {
		return 130
	}
	if len(again) == 0 {
		if res, ok := runinfo.ReadRecord(filepath.Join(note.Leaf, runinfo.ResultName)); ok && res.Status == "FAIL" && (code == 0 || (code == 75 && len(res.Asked) > 0)) {
			return 1 // it asked, and was not run again: that is a failure, whatever its tool's own code said
		}
		return said
	}
	// It asked for what it did not have, and was run again: the command goes on with those, and ends as the last does
	worst := 0
	for _, next := range again {
		next.Write()
		if got := follow(t, next, o, banner, cwd, mine); got == 130 {
			return 130
		} else if got != 0 && got != 77 {
			worst = got
		}
	}
	return worst
}

func whereOf(note runinfo.RunNote) string {
	if note.Host != nil {
		return note.Host.Host
	}
	return "this machine's agent"
}

// draws: whether a run is drawn for a person -- a header, a gutter -- and whoever draws told so
func (o view) draws(t *reach.Term) bool {
	drawn := drawnFor(t, o.plain)
	t.Event(map[string]any{"mode": draw.DrawMode{Drawn: drawn, Color: drawn && t.Getenv("NO_COLOR") == ""}})
	return drawn
}

func drawnFor(t *reach.Term, plain bool) bool {
	return t.TtyOut && !plain && t.Getenv("ERRAND_OUTPUT") != "plain"
}

// gutterOf: what a gutter takes of a run's terminal, when the run is drawn
func gutterOf(drawn bool) int {
	if drawn {
		return draw.GutterWidth
	}
	return 0
}

// ttyOf: the terminal a run is given -- that of whoever typed the command, when it is one ( ADR 0005: a process has a
// terminal when its client has one ). Colours are asked of programs that look at the environment and not at the
// terminal ( ADR 0016 ), unless the surroundings say otherwise: `NO_COLOR`, or a value already chosen.
func ttyOf(t *reach.Term, plain bool) *runinfo.TtySpec {
	if !t.TtyOut {
		return nil
	}
	cols, rows := t.Winsize()
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	kind := t.Getenv("TERM")
	if kind == "" {
		kind = "xterm-256color"
	}
	drawn := drawnFor(t, plain)
	tty := &runinfo.TtySpec{Cols: max(1, cols-gutterOf(drawn)), Rows: rows, Term: kind, Gutter: gutterOf(drawn), Input: t.TtyIn}
	tty.Colors = drawn && kind != "dumb" && t.Getenv("NO_COLOR") == "" && t.Getenv("FORCE_COLOR") == "" && t.Getenv("CLICOLOR_FORCE") == ""
	return tty
}

// seenAt: where what a run made to be looked at opens, here -- its views, and its page when one is served. `serve`: what
// needs a server to open is given one; else the command that does is said.
func seenAt(t *reach.Term, note runinfo.RunNote, serve bool) []string {
	var out []string
	data, _ := os.ReadFile(filepath.Join(note.Leaf, "steps.jsonl"))
	type viewed struct {
		View  string `json:"view"`
		Title string `json:"title"`
		Open  []any  `json:"open"`
	}
	var views []*viewed
	at := map[string]*viewed{}
	for _, line := range strings.Split(string(data), "\n") {
		var e viewed
		if json.Unmarshal([]byte(line), &e) != nil || e.View == "" {
			continue
		}
		v := at[e.View]
		if v == nil {
			v = &viewed{View: e.View}
			at[e.View] = v
			views = append(views, v)
		}
		if e.Title != "" { // named again without saying again how it is opened: opened as it was
			v.Title = e.Title
		}
		if len(e.Open) > 0 {
			v.Open = e.Open
		}
	}
	for _, e := range views {
		p, rel := e.View, e.View
		if !filepath.IsAbs(p) {
			p = filepath.Join(note.Leaf, p)
		} else if i := strings.Index(p, "/"+filepath.Base(note.Leaf)+"/"); i >= 0 { // said by its path over there
			rel = p[i+len(filepath.Base(note.Leaf))+2:]
			p = filepath.Join(note.Leaf, rel)
		}
		where := "file://" + (&url.URL{Path: p}).EscapedPath()
		for _, x := range e.Open {
			if o, ok := x.(map[string]any); ok && kit.Text(o["static"]) != "" { // served as it is, by the page's program
				if !serve {
					where = "errand open " + reach.ShellQuote(p)
					continue
				}
				cmd := exec.Command(kit.SelfPath(), "web", "static", note.Leaf, rel, kit.Text(o["static"]))
				cmd.Env = t.Environ
				if got, err := cmd.Output(); err == nil && len(got) > 0 {
					where = strings.TrimSpace(string(got))
				}
			}
		}
		name := e.Title
		if name == "" {
			name = filepath.Base(e.View)
		}
		out = append(out, "    ↗ "+name+"  "+where)
	}
	var page struct {
		Port   int `json:"port"`
		Public int `json:"public"`
		Pid    int `json:"pid"`
	}
	if data, err := os.ReadFile(filepath.Join(kit.LocalCache(), "page", "said.json")); err == nil && json.Unmarshal(data, &page) == nil && system.Signal(page.Pid, 0) == nil && page.Pid > 0 {
		if page.Public > 0 {
			page.Port = page.Public
		}
		sum := sha1.Sum([]byte(strings.TrimRight(filepath.ToSlash(note.Leaf), "/")))
		out = append(out, fmt.Sprintf("    ↗ http://127.0.0.1:%d/r/%s", page.Port, hex.EncodeToString(sum[:])[:16]))
	}
	return out
}

// ending: what closes a run -- its status, how long it took, its results when they are short, and, when it failed,
// where its whole output is
func ending(t *reach.Term, note runinfo.RunNote, o view, cwd string, code int) int {
	leaf := note.Leaf
	res, ok := runinfo.ReadRecord(filepath.Join(leaf, runinfo.ResultName))
	if !o.quiet {
		end := draw.RunEnd{Status: "FAIL", Label: note.What}
		if ok {
			end.Status = res.Status
			if res.Duration != nil {
				end.Tail = "  " + runinfo.Took(*res.Duration)
			}
			if len(res.Again) > 0 {
				end.Tail += " · asked for " + res.Again[0].Asked + ", run again"
			}
			counted := len(res.Entries) > 0 // what its tool reported: how many passed, failed, were skipped, when any
			if counted {
				for _, k := range []string{"passed", "failed", "skipped"} {
					if n := kit.Num(res.Results[k], 0); n > 0 {
						end.Tail += fmt.Sprintf(" · %.0f %s", n, k)
					}
				}
			}
			for k, v := range res.Results {
				if counted && (k == "passed" || k == "failed" || k == "skipped") {
					continue
				}
				end.Tail += fmt.Sprintf(" · %s %v", k, v)
			}
		}
		if end.Status == "FAIL" {
			end.Output = filepath.Join(leaf, "output.txt")
			if rel, err := filepath.Rel(cwd, end.Output); err == nil && !strings.HasPrefix(rel, "..") {
				end.Output = rel
			}
		}
		t.Event(map[string]any{"end": end})
		for _, line := range seenAt(t, note, t.TtyOut) {
			t.Say(line)
		}
	}
	if code < 0 {
		return 128 - code
	}
	return code
}
