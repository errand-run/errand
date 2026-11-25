// Environments kept current by themselves ( the guide's "Automatic installations", ADR 0004, 0035 ): carried out by
// the agent of the place that holds what is built, so that it does not depend on whoever asked staying there.
//
// A layer says what it is made from; before a command, that is compared with what was last built, and the difference
// is made up. What is already there and was never seen before was built by somebody: it is **adopted**, not rebuilt.
// Only a declaration that changed since a record errand itself wrote is a reason to touch anything.
//
//	<outputs>/builds/records/<artefact>.json    both ends of its build: the fingerprint it was built from, its stamp
//	<outputs>/builds/records/<artefact>.log     the output of the last attempt
//	<outputs>/builds/records/<artefact>.lock/   who is building it now ( an intent ): a directory, made by `mkdir`
//
// **The lock and the intent are in the directory, not in an agent's memory** ( ADR 0035 ): a login node and a compute
// node see the same one, and so does an agent started later. The intent says which machine does the work, and which
// process: only that machine can say whether it still lives; from another, a heartbeat that stopped says it does not.

package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/layer"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/queue"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

type builtInfo struct {
	Fingerprint string `json:"fingerprint"`
	Stamp       string `json:"stamp"`
	Origin      string `json:"origin"` // built | adopted
	Date        string `json:"date"`
	By          string `json:"by"`
}

type failedAttempt struct {
	Fingerprint string `json:"fingerprint"`
	Date        string `json:"date"`
	Step        string `json:"step"`
	Code        int    `json:"code"`
}

type buildRecord struct {
	V           int            `json:"v"`
	Built       *builtInfo     `json:"built,omitempty"`
	LastAttempt *failedAttempt `json:"last_attempt,omitempty"`
}

// intent is who builds an artefact now.
type intent struct {
	V           int     `json:"v"`
	What        string  `json:"what"`
	Fingerprint string  `json:"fingerprint"`
	Host        string  `json:"host"`
	Pid         int     `json:"pid"`
	Since       any     `json:"since"` // when that process started
	Started     float64 `json:"started"`
}

const (
	intentBeat  = 10 * time.Second // how often an intent says it is still carried out
	intentStale = 60 * time.Second // past this with nothing said, seen from another machine, it is not
)

// setting is where an environment is set up, and for whom.
type setting struct {
	root, outputs string
	tags          map[string]any
	environ       []string     // the surroundings its commands will have
	say           func(string) // errand's own words about it
	force, dryRun bool
	stop          <-chan struct{} // closed when whoever asked no longer wants it
}

func (s setting) records() string { return filepath.Join(s.outputs, "builds", "records") }

func (s setting) ctx() layer.Context {
	return layer.Context{Root: s.root, Tags: s.tags, Environ: s.environ}
}

// line: a line of shell run from the project's root, in the surroundings given -> its exit code, what it printed
func (s setting) line(text string, out io.Writer) (int, string) {
	cmd := exec.Command("sh", "-c", text)
	if layer.OnWindows {
		var err error
		if cmd, err = s.commandOf(text, out); err != nil {
			if out != nil {
				fmt.Fprintln(out, err)
			}
			return 127, err.Error()
		}
	}
	cmd.Dir = s.root
	if s.environ != nil {
		cmd.Env = s.environ
	}
	cmd.SysProcAttr = system.Apart()
	var said strings.Builder
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	} else {
		cmd.Stdout = &said
	}
	if err := cmd.Start(); err != nil {
		return 127, err.Error()
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-s.stop: // ( nil for who cannot be stopped: never ready )
		_ = system.SignalGroup(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(kit.Grace[1]):
			_ = system.SignalGroup(cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return 130, ""
	}
	return cmd.ProcessState.ExitCode(), strings.TrimSpace(said.String())
}

// commandOf: a line that is one command, as Windows starts it: no shell reads it
func (s setting) commandOf(text string, out io.Writer) (*exec.Cmd, error) {
	argv := runinfo.ShellWords(text)
	if len(argv) == 0 {
		return nil, errors.New("an empty line")
	}
	environ := s.environ
	if environ == nil {
		environ = os.Environ()
	}
	word := argv[0]
	if word == "uv" {
		uv, err := s.uv(environ, out)
		if err != nil {
			return nil, err
		}
		word = uv
		if len(argv) > 2 && argv[1] == "venv" && kit.Exists(filepath.Join(s.root, argv[2], "pyvenv.cfg")) { // a venv that is there is made anew
			argv = append(argv, "--clear")
		}
	}
	path, err := kit.LookIn(word, environ, s.root)
	if err != nil && word == "python3" { // ( the name it has there )
		path, err = kit.LookIn("python", environ, s.root)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", word, err)
	}
	return &exec.Cmd{Path: path, Args: argv}, nil
}

// uv: the uv of a Windows -- of the PATH, of its usual places, or of errand's own directory, fetched there when there is
// none: never on the PATH
func (s setting) uv(environ []string, out io.Writer) (string, error) {
	if p, err := kit.LookIn("uv", environ, s.root); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	own := filepath.Join(kit.LocalCache(), "uv")
	for _, p := range []string{filepath.Join(home, ".local", "bin", "uv.exe"), `C:\errand\uv\uv.exe`, filepath.Join(own, "uv.exe")} {
		if kit.Exists(p) {
			return p, nil
		}
	}
	s.say("    no uv here: fetching one into " + own)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-ExecutionPolicy", "Bypass", "-Command",
		"$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'; Invoke-RestMethod https://astral.sh/uv/install.ps1 | Invoke-Expression")
	cmd.Env = append(append([]string{}, environ...), "UV_UNMANAGED_INSTALL="+own)
	said, err := cmd.CombinedOutput()
	if out != nil {
		_, _ = out.Write(said)
	}
	if p := filepath.Join(own, "uv.exe"); kit.Exists(p) {
		return p, nil
	}
	return "", fmt.Errorf("uv could not be fetched: %v %s", err, kit.EndOf(string(said), 400))
}

// withTools: on a Windows, the surroundings its tools make ( the build tools, the venv of a box ): what an installation
// may compile with
func (s setting) withTools(stack []project.Layer) (setting, error) {
	if len(stack) == 0 || stack[0].Type() != "WindowsTools" {
		return s, nil
	}
	got, err := layer.Wrap(stack[:1], layer.Command{}, s.ctx())
	if err != nil {
		return s, err
	}
	environ := s.environ
	if environ == nil {
		environ = os.Environ()
	}
	s.environ = append([]string{}, environ...)
	for k, v := range got.Env {
		s.environ = append(s.environ, k+"="+v)
	}
	return s, nil
}

// present: whether an artefact is there
func (s setting) present(a layer.Artefact) bool {
	if layer.OnWindows {
		return a.Present(s.ctx())
	}
	code, _ := s.line(a.Probe(s.ctx())+" >/dev/null 2>&1", nil)
	return code == 0
}

// stamp: what it looks like from outside
func (s setting) stamp(a layer.Artefact) string {
	if layer.OnWindows {
		_, said := s.line(a.Stamp(s.ctx()), nil)
		return said
	}
	_, said := s.line("( "+a.Stamp(s.ctx())+" ) 2>/dev/null", nil)
	return said
}

func readBuild(path string) buildRecord {
	var r buildRecord
	if data, err := os.ReadFile(path); err == nil {
		_ = kit.JsonUnmarshal(data, &r)
	}
	return r
}

func nowSaid() string { return time.Now().Format("2006-01-02 15:04:05") }

func whoBuilds() string {
	name := "?"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return name + "@" + queue.Host()
}

// verdict: what a record says of an artefact that is there -- ok, adopted, stale, or found ( nothing known of it ).
// A changed declaration comes first, stamp or no stamp: it is a difference from a record errand itself wrote. A stamp
// that changed alone means the record no longer describes what is there.
func verdict(want string, r buildRecord, stamp string) string {
	switch b := r.Built; {
	case b == nil:
		return "found"
	case b.Fingerprint != want:
		return "stale"
	case b.Stamp != "" && stamp != "" && b.Stamp != stamp:
		return "found"
	case b.Origin == "adopted":
		return "adopted"
	}
	return "ok"
}

// newerInput: the file of the declaration newer than the artefact, when there is one. Only asked of what errand
// knows nothing about: dates are wrong about a `git checkout`, so a fingerprint decides as soon as there is one.
func newerInput(a layer.Artefact, root, stamp string) string {
	when, ok := a.Made(stamp)
	if !ok {
		return ""
	}
	for _, rel := range a.Inputs() {
		if st, err := os.Stat(filepath.Join(root, rel)); err == nil && float64(st.ModTime().Unix()) > when {
			return filepath.Base(rel)
		}
	}
	return ""
}

// ── the lock, and the intent in it ───────────────────────────────────────────

// hold: the artefact taken, to build it -- waited for while another builds it. -> what lets go of it, and whether
// somebody else built meanwhile ( the state is to be looked at again )
func (s setting) hold(a layer.Artefact, want string) (func(), bool, error) {
	lock := filepath.Join(s.records(), a.Slug()+".lock")
	file := filepath.Join(lock, "intent.json")
	waited := false
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, false, err
		}
		var held intent
		data, _ := os.ReadFile(file)
		_ = kit.JsonUnmarshal(data, &held)
		st, statErr := os.Stat(file)
		if statErr != nil {
			st, statErr = os.Stat(lock)
		}
		stale := statErr != nil
		switch {
		case stale:
		case held.Host == queue.Host() && held.Pid != 0: // this machine's: it can say
			stale = fmt.Sprint(kit.Started(held.Pid)) != fmt.Sprint(held.Since) || held.Since == nil
		default: // another machine's, or not said yet: its heartbeat
			stale = time.Since(st.ModTime()) > intentStale
		}
		if stale { // whoever built it went without a word: what it left is taken over
			_ = os.RemoveAll(lock)
			continue
		}
		if !waited {
			waited = true
			who := held.Host
			if who == "" {
				who = "somebody"
			}
			s.say(fmt.Sprintf("  %s is being prepared by %s: waiting for it", a.Name, who))
		}
		select {
		case <-s.stop:
			return nil, false, errors.New("stopped while it waited for " + a.Name)
		case <-time.After(300 * time.Millisecond):
		}
	}
	mine := intent{V: 1, What: a.Name, Fingerprint: want, Host: queue.Host(), Pid: os.Getpid(), Since: kit.Started(os.Getpid()), Started: kit.Seconds(time.Now())}
	_ = kit.WriteRecord(file, mine)
	done := make(chan struct{})
	go func() { // still carried out: said for who looks from another machine
		for {
			select {
			case <-done:
				return
			case <-time.After(intentBeat):
				now := time.Now()
				_ = os.Chtimes(file, now, now)
			}
		}
	}()
	return func() { close(done); _ = os.RemoveAll(lock) }, waited, nil
}

// ── bringing up to date ──────────────────────────────────────────────────────

// state: how an artefact is, and what is known of it
func (s setting) look(a layer.Artefact) (present bool, r buildRecord, stamp string) {
	present = s.present(a)
	r = readBuild(filepath.Join(s.records(), a.Slug()+".json"))
	if present {
		stamp = s.stamp(a)
	}
	return
}

// saidLines: what a build prints, said a line at a time as errand's own words -- carriage returns ( a bar redrawn )
// are lines too, and a last line without its end is said when the build ends
type saidLines struct {
	say func(string)
	buf []byte
}

func (w *saidLines) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexAny(string(w.buf), "\r\n")
		if i < 0 {
			return len(p), nil
		}
		if line := strings.TrimRight(string(w.buf[:i]), " "); line != "" {
			w.say(line)
		}
		w.buf = w.buf[i+1:]
	}
}

// lacks: whether the place has what building this asks of it ( its program, a running engine ) -- said, with where
func (s setting) lacks(a layer.Artefact) error {
	for _, need := range a.Needs() {
		if code, _ := s.line("{ "+need[0]+" ; } >/dev/null 2>&1", nil); code != 0 {
			return fmt.Errorf("on %s: %s ( %s )", queue.Host(), need[1], a.Name)
		}
	}
	return nil
}

// decide: what is to be done about an artefact -- what is seen of it, whether it is to be built, and whether what a
// build would replace was changed since errand made it ( by hand, by something else )
func (s setting) decide(a layer.Artefact, want string) (present bool, record buildRecord, stamp, state string, build, moved bool) {
	present, record, stamp = s.look(a)
	state = verdict(want, record, stamp)
	// the file is not the one errand left, while its record is there: it was changed outside errand
	moved = present && record.Built != nil && record.Built.Stamp != "" && stamp != "" && record.Built.Stamp != stamp
	// the last attempt left something to make up for -- not for what is built aside: what failed then touched
	// nothing, and being stale is its own reason
	retried := record.LastAttempt != nil && !a.Aside()
	build = s.force || !present || state == "stale" || retried
	if state == "found" && !build {
		switch older := newerInput(a, s.root, stamp); {
		case moved && a.Aside(): // its recipe did not change ( the fingerprint says so ): it is somebody's, and is kept as it is
			s.say(fmt.Sprintf("  %s: changed since errand made it, its recipe did not: adopted as it is ( errand setup --force rebuilds it )", a.Name))
		case older == "":
		case a.Aside() && record.Built == nil: // an adoption is a guess, and a wrong one costs a release: what a build cannot harm is rebuilt
			s.say(fmt.Sprintf("  %s: older than %s, and errand has no record of it", a.Name, older))
			build = true
		case a.Aside(): // a record that says its recipe is the same: a date is wrong about a `git checkout`
		default:
			s.say(fmt.Sprintf("  %s: older than %s: adopted as it is ( errand setup --force to rebuild )", a.Name, older))
		}
	}
	return
}

// replaces: what a build that is wanted would replace and nobody asked it to -- an image changed outside errand while its
// recipe changed too. Said, and nothing is built: `errand setup --force` goes ahead, the one replaced kept as `.prev`.
func (s setting) replaces(a layer.Artefact, build, moved bool) error {
	if build && moved && !s.force && a.Aside() {
		return fmt.Errorf("%s was changed outside errand since errand made it, and its recipe changed too: building it replaces it ( the one replaced is kept as %s.prev ); `errand setup --force` goes ahead",
			a.Name, a.Layer.String("image"))
	}
	return nil
}

// CarriedImage: an image that is built in another environment, and is wanted here
type CarriedImage struct {
	Name    string `json:"name"` // as a person reads it
	Slug    string `json:"slug"`
	Image   string `json:"image"`   // its path, from the project's root
	Builder string `json:"builder"` // the environment it is built in
}

// wanted: what of a stack is built elsewhere, and must be made now -- not built here: asked for, by whoever drives.
// `all`: every image of the stack, as the builder is asked about the one it builds.
func wanted(stack []project.Layer, s setting, all bool) ([]CarriedImage, error) {
	var out []CarriedImage
	for _, a := range layer.Artefacts(stack) {
		if a.Builder() == "" && !all {
			continue
		}
		_, _, _, _, build, moved := s.decide(a, a.Fingerprint(s.ctx()))
		if err := s.replaces(a, build, moved); err != nil {
			return nil, err
		}
		if build {
			out = append(out, CarriedImage{a.Name, a.Slug(), a.Layer.String("image"), a.Builder()})
		}
	}
	return out, nil
}

// received: an image that was built in another environment and carried here is now what this machine has -- what is
// done with it once it is there, and the record that says what it was made from
func (s setting) received(a layer.Artefact, from string) error {
	if err := os.MkdirAll(s.records(), 0o755); err != nil {
		return err
	}
	want := a.Fingerprint(s.ctx())
	release, _, err := s.hold(a, want)
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(s.records(), a.Slug()+".json")
	logPath := filepath.Join(s.records(), a.Slug()+".log")
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer log.Close()
	for _, step := range a.Installs() {
		s.say("    $ " + step)
		fmt.Fprintf(log, "$ %s\n", step)
		if code, _ := s.line(step, log); code != 0 {
			_ = kit.WriteRecord(path, buildRecord{V: 1, LastAttempt: &failedAttempt{want, nowSaid(), step, code}})
			return fmt.Errorf("failed while preparing %s ( exit %d; its output: %s )", a.Name, code, logPath)
		}
	}
	by := whoBuilds()
	if from != "" {
		by += " ( built in " + from + " )"
	}
	after := s.stamp(a)
	return kit.WriteRecord(path, buildRecord{V: 1, Built: &builtInfo{want, after, "built", nowSaid(), by}})
}

// ensure: whatever a stack declares and does not have, built or brought up to date
func ensure(stack []project.Layer, s setting) error {
	artefacts := layer.Artefacts(stack)
	if len(artefacts) == 0 {
		return nil
	}
	if layer.OnWindows { // a command at a time, which no shell reads
		for _, a := range artefacts {
			if !a.Simple() {
				return fmt.Errorf("%s: errand does not build it on Windows; `provision`, in the layer that reaches it, can", a.Name)
			}
		}
		var err error
		if s, err = s.withTools(stack); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(s.records(), 0o755); err != nil {
		return err
	}
	said := false
	for _, a := range artefacts {
		want := a.Fingerprint(s.ctx())
		path := filepath.Join(s.records(), a.Slug()+".json")
		for {
			judge := s
			if a.Builder() != "" { // forced: it was carried before, by whoever drives, and what is here is what came
				judge.force = false
			}
			present, record, stamp, state, build, moved := judge.decide(a, want)
			if err := s.replaces(a, build, moved); err != nil {
				return err
			}
			if build && a.Builder() != "" { // built in another environment, and carried here: by whoever drives, not by this agent
				if s.dryRun {
					s.say(fmt.Sprintf("    would be built in %s, and carried here", a.Builder()))
					break
				}
				return fmt.Errorf("%s is built in the environment %s, and carried here: `errand setup` does it, and a run does when it is not given --no-setup", a.Name, a.Builder())
			}
			var steps []string
			if build {
				steps = a.Steps(s.ctx())
			}
			if len(steps) > 0 { // before anything is built: what this program can do, and what the place must have
				if why := a.Refusal(); why != "" {
					return errors.New(a.Name + ": " + why)
				}
				if err := s.lacks(a); err != nil {
					return err
				}
			}
			if len(steps) == 0 { // nothing to do, or nothing it could do
				if !s.dryRun && (state == "found" || (build && state == "stale")) {
					_ = kit.WriteRecord(path, buildRecord{V: 1, Built: &builtInfo{want, stamp, "adopted", nowSaid(), whoBuilds()}})
				}
				break
			}
			if s.dryRun {
				for _, step := range steps {
					s.say("    would run: " + step)
				}
				break
			}
			release, waited, err := s.hold(a, want)
			if err != nil {
				return err
			}
			if waited { // somebody built it meanwhile: looked at again, with what it left
				release()
				continue
			}
			if !said {
				said = true
				why := "bringing up to date"
				if s.force {
					why = "rebuilding"
				}
				s.say("  " + why + " " + a.Name)
			}
			err = s.build(a, want, steps, record, present, stamp)
			release()
			if err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func (s setting) build(a layer.Artefact, want string, steps []string, record buildRecord, present bool, stamp string) error {
	path := filepath.Join(s.records(), a.Slug()+".json")
	logPath := filepath.Join(s.records(), a.Slug()+".log")
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer log.Close()
	for _, step := range steps {
		shown := step
		if len(shown) > 200 {
			shown = shown[:200] + " …"
		}
		s.say("    $ " + shown)
		fmt.Fprintf(log, "$ %s\n", step)
		var out io.Writer = log
		var lines *saidLines
		if a.Shows() {
			lines = &saidLines{say: func(l string) { s.say("      " + l) }}
			out = io.MultiWriter(log, lines)
		}
		code, _ := s.line(step, out)
		if lines != nil {
			_, _ = lines.Write([]byte("\n")) // ( a last line with no end )
		}
		if code != 0 {
			if undo := a.Abandon(); undo != "" { // what the attempt left half made
				s.line(undo, nil)
			}
			record.V = 1
			record.LastAttempt = &failedAttempt{want, nowSaid(), step, code}
			_ = kit.WriteRecord(path, record)
			msg := fmt.Sprintf("failed while preparing %s ( exit %d; its output: %s )", a.Name, code, logPath)
			if tail := runinfo.LastLines(logPath, 4); tail != "" && !a.Shows() { // ( what was shown as it was said is not said twice )
				msg += "\n" + tail
			}
			if present && a.Aside() {
				msg += "\nthe existing one is untouched: --no-setup uses it as it is"
			} else if present {
				msg += "\nthe existing one may be half updated: --no-setup uses it as it is"
			}
			return errors.New(msg)
		}
	}
	after := s.stamp(a)
	return kit.WriteRecord(path, buildRecord{V: 1, Built: &builtInfo{want, after, "built", nowSaid(), whoBuilds()}})
}

// stateOf: ok / adopted / stale / not built -- what `errand envs` says of an environment's stack on this machine
func stateOf(stack []project.Layer, s setting) string {
	if layer.OnWindows {
		if with, err := s.withTools(stack); err == nil {
			s = with
		}
	}
	artefacts := layer.Artefacts(stack)
	found := map[string]bool{}
	for _, a := range artefacts {
		present, record, stamp := s.look(a)
		if !present {
			return "not built"
		}
		state := verdict(a.Fingerprint(s.ctx()), record, stamp)
		if f := record.LastAttempt; f != nil && (state == "stale" || state == "found" || !a.Aside()) {
			return "stale ( last build failed, " + f.Date + " )"
		}
		if state == "found" { // nothing recorded and everything present: somebody else's doing, which will be adopted
			state = "ok"
			if a.Aside() && newerInput(a, s.root, stamp) != "" { // ...unless it is what a build cannot harm, and older than its recipe
				state = "stale"
			}
		}
		found[state] = true
	}
	for _, st := range []string{"stale", "adopted"} {
		if found[st] {
			return st
		}
	}
	return "ok"
}
