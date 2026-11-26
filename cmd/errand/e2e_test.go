package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/draw"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/runinfo"
	"github.com/errand-run/errand/internal/system"
)

var built, builtNext, builtOld string // the program; the same with another hash ( an update ); one of a release before

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("/tmp", "errand-e2e-")
	if err != nil {
		panic(err)
	}
	built = filepath.Join(dir, "errand")
	builtNext, builtOld = filepath.Join(dir, "next", "errand"), filepath.Join(dir, "old", "errand")
	for program, flags := range map[string]string{built: "", builtNext: "-X github.com/errand-run/errand/internal/reach.buildMark=next", builtOld: "-X github.com/errand-run/errand/internal/reach.buildMark=old -X github.com/errand-run/errand/internal/reach.Release=0"} {
		if out, err := exec.Command("go", "build", "-ldflags", flags, "-o", program, ".").CombinedOutput(); err != nil {
			panic(string(out))
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// scene: a project of a moment, on a machine whose queue is its own
type scene struct {
	t    *testing.T
	root string
	far  string // the other machine's: home, cache, queue, and the log of what ssh was asked
	env  []string
	// the errand typed, when it is not the one just built ( another version of it )
	program string
}

func newPlace(t *testing.T) *scene {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "errand-p-") // ( short: a socket's path is )
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() {
		_ = exec.Command("pkill", "-f", root).Run()
		_ = os.RemoveAll(root)
	})
	_ = os.MkdirAll(filepath.Join(root, "queue"), 0o700)
	env := []string{"XDG_RUNTIME_DIR=" + filepath.Join(root, "queue"), "NO_COLOR=1", "ERRAND_CACHE_DIR=" + filepath.Join(root, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config")} // ( never the configuration of whoever runs this )
	for _, kv := range os.Environ() {
		name := strings.SplitN(kv, "=", 2)[0]
		if !strings.HasPrefix(name, "ERRAND") && !strings.HasPrefix(name, "SLURM_") && name != "XDG_RUNTIME_DIR" && name != "XDG_CONFIG_HOME" && name != "NO_COLOR" {
			env = append(env, kv)
		}
	}
	// another machine, for a moment: an `ssh` that reaches this one, with a home, a cache and a queue of its own
	far := filepath.Join(root, "far")
	for _, d := range []string{"home", "cache", "queue"} {
		_ = os.MkdirAll(filepath.Join(far, d), 0o700)
	}
	ssh := filepath.Join(root, "ssh")
	_ = os.WriteFile(ssh, []byte(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o|-p|-i|-J) shift 2;; -*) shift;; *) break;; esac; done
host="$1"; shift
[ "$host" = nowhere ] && { echo "ssh: Could not resolve hostname nowhere: nodename nor servname provided" >&2; exit 255; }
echo "$host: $*" >> `+far+`/log
[ -f `+far+`/offline ] && export ERRAND_NO_INTERNET=1
export HOME=`+far+`/home ERRAND_CACHE_DIR=`+far+`/cache XDG_RUNTIME_DIR=`+far+`/queue SHELL=/bin/sh
cd "$HOME" && exec /bin/sh -c "$*"
`), 0o755)
	env = append(env, "ERRAND_SSH="+ssh)
	return &scene{t: t, root: filepath.Join(root, "proj"), far: far, env: env}
}

func (p *scene) write(rel, text string) string {
	p.t.Helper()
	path := filepath.Join(p.root, rel)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		p.t.Fatal(err)
	}
	return path
}

type outcome struct {
	code        int
	out, err    string
	leaf        string
	result      runinfo.Record
	hasResult   bool
	p           *scene
	startedAt   time.Time
	finishedAt  time.Time
	commandLine []string
}

func (p *scene) command(args ...string) *exec.Cmd {
	_ = os.MkdirAll(p.root, 0o755)
	program := built
	if p.program != "" {
		program = p.program
	}
	cmd := exec.Command(program, args...)
	cmd.Dir, cmd.Env = p.root, p.env
	return cmd
}

// errand: the program run to its end -- what it printed, how it ended, and the record of the run it made
func (p *scene) errand(stdin string, args ...string) outcome {
	p.t.Helper()
	cmd := p.command(args...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	o := outcome{p: p, startedAt: time.Now(), commandLine: args}
	err := cmd.Run()
	o.finishedAt = time.Now()
	o.out, o.err = out.String(), errs.String()
	if ee, ok := err.(*exec.ExitError); ok {
		o.code = ee.ExitCode()
	} else if err != nil {
		p.t.Fatalf("errand %v: %v", args, err)
	}
	o.leaf, o.result, o.hasResult = p.latest()
	return o
}

// latest: the newest run of the project
func (p *scene) latest() (string, runinfo.Record, bool) {
	var newest string
	var at float64
	_ = filepath.Walk(filepath.Join(p.root, "errand-outputs", "runs"), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == runinfo.LaunchName {
			if r, ok := runinfo.ReadRecord(path); ok && r.Started >= at {
				newest, at = filepath.Dir(path), r.Started
			}
		}
		return nil
	})
	r, ok := runinfo.ReadRecord(filepath.Join(newest, runinfo.ResultName))
	return newest, r, ok
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return string(data)
}

func TestACommandIsRunAndRecorded(t *testing.T) {
	p := newPlace(t)
	o := p.errand("", "run", "sh", "-c", "echo to-out; echo to-err >&2; echo more")
	if o.code != 0 || o.out != "to-out\nmore\n" || !strings.Contains(o.err, "to-err\n") || !strings.Contains(o.err, "│ PASS") {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", o.code, o.out, o.err)
	}
	r := o.result
	if !o.hasResult || r.Status != "PASS" || r.Error != nil || r.Env != "local" || r.Name != "sh" || r.Duration == nil || r.RAM == nil ||
		!strings.HasPrefix(r.Place, "local@") || r.Params["args"] != "-c echo to-out; echo to-err >&2; echo more" {
		t.Fatalf("its record: %+v", r)
	}
	// one directory per run, under what was typed to run it; the streams as they were written, and both as they came
	if rel, _ := filepath.Rel(p.root, o.leaf); !strings.HasPrefix(rel, "errand-outputs/runs/sh/") {
		t.Errorf("filed under %s", rel)
	}
	if got := read(t, filepath.Join(o.leaf, "stdout.txt")); got != "to-out\nmore\n" {
		t.Errorf("stdout.txt: %q", got)
	}
	if got := read(t, filepath.Join(o.leaf, "stderr.txt")); got != "to-err\n" {
		t.Errorf("stderr.txt: %q", got)
	}
	if got := read(t, filepath.Join(o.leaf, "output.txt")); !strings.Contains(got, "to-out") || !strings.Contains(got, "to-err") {
		t.Errorf("output.txt: %q", got)
	}
	if to, _ := os.Readlink(filepath.Join(filepath.Dir(o.leaf), "latest")); to != filepath.Base(o.leaf) {
		t.Errorf("latest -> %q", to)
	}
	// nothing of its keeper is left once it was followed to its end
	if left := p.jobsLeft(p.cache()); len(left) != 0 {
		t.Errorf("left with this machine's agent: %v", left)
	}
	// a second run of the same thing: another directory, a summary of both
	time.Sleep(1100 * time.Millisecond)
	p.errand("", "run", "-q", "sh", "-c", "echo to-out; echo to-err >&2; echo more")
	var sum map[string]any
	_ = json.Unmarshal([]byte(read(t, filepath.Join(filepath.Dir(o.leaf), runinfo.SummaryName))), &sum)
	if sum["runs"] != 2.0 || sum["passed"] != 2.0 {
		t.Errorf("summary: %v", sum)
	}
}

func TestExitCodes(t *testing.T) {
	p := newPlace(t)
	o := p.errand("", "run", "sh", "-c", "echo before the end >&2; exit 3")
	if o.code != 3 || o.result.Status != "FAIL" || *o.result.Error != "exit 3\nbefore the end" || !strings.Contains(o.err, "↳ errand-outputs/runs/sh/") {
		t.Errorf("a failure: exit %d, %+v, said %q", o.code, o.result.Error, o.err)
	}
	if o = p.errand("", "run", "sh", "-c", "exit 77"); o.code != 0 || o.result.Status != "SKIP" || !strings.Contains(o.err, "│ SKIP") {
		t.Errorf("77 is a skip: exit %d, %s", o.code, o.result.Status)
	}
	if o = p.errand("", "run", "sh", "-c", "kill -SEGV $$"); o.result.Status != "FAIL" || !strings.Contains(*o.result.Error, "signal SIGSEGV") || o.code != 139 {
		t.Errorf("a signal: exit %d, %v", o.code, o.result.Error)
	}
	if o = p.errand("", "run", "no-such-command-here"); o.code != 127 || o.result.Status != "FAIL" || !strings.Contains(o.err, "nor a command here") {
		t.Errorf("a target that designates nothing: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run"); o.code != 2 {
		t.Errorf("no target: exit %d", o.code)
	}
	if o = p.errand("", "run", "--frobnicate", "true"); o.code != 2 || !strings.Contains(o.err, "after its target") {
		t.Errorf("an unknown option before the target: exit %d, %q", o.code, o.err)
	}
}

func TestWhatFollowsTheTargetIsTheTargets(t *testing.T) {
	p := newPlace(t)
	tool := p.write("tools/show.sh", "#!/bin/sh\necho \"$@\"\ncat\n")
	o := p.errand("given to read\n", "run", "-q", "tools/show.sh", "--env", "prod", "-x", "-o")
	if o.code != 0 || o.out != "--env prod -x -o\ngiven to read\n" {
		t.Fatalf("exit %d, %q %q", o.code, o.out, o.err)
	}
	// a file of the project is executed as it is, and filed under its path from the root
	if o.result.File != "tools/show.sh" || !strings.Contains(o.leaf, "/runs/tools/show.sh/") || o.result.Argv[0] != "tools/show.sh" {
		t.Errorf("filed as %q in %s", o.result.File, o.leaf)
	}
	_ = tool
	if o = p.errand("", "run", "-q", "--", "sh", "-c", "echo $0", "-q"); o.out != "-q\n" {
		t.Errorf("after --, nothing is errand's: %q", o.out)
	}
}

func TestWhatARunIsHandedAndSays(t *testing.T) {
	p := newPlace(t)
	p.write("job.sh", `#!/bin/sh
echo "protocol $ERRAND_PROTOCOL out ${ERRAND_OUT_DIR:-none}"
"$ERRAND" say step "half way" --done 0.5 residual=1e-3
echo plot > "$ERRAND_OUT_DIR/plot.txt"
"$ERRAND" say view plot.txt --title "The plot"
"$ERRAND" say result seconds=1.5 primes=9592
"$ERRAND" say result method=newton seconds=2.5
"$ERRAND" say nonsense || echo "refused $?"
`)
	o := p.errand("", "run", "-o", "job.sh")
	if o.code != 0 || !strings.Contains(o.out, "protocol 1 out "+o.leaf) || !strings.Contains(o.out, "refused 2") {
		t.Fatalf("exit %d, %q %q", o.code, o.out, o.err)
	}
	if r := o.result.Results; r["seconds"] != 2.5 || r["primes"] != 9592.0 || r["method"] != "newton" {
		t.Errorf("its results, merged in order: %v", r)
	}
	if !strings.Contains(o.err, "seconds 2.5") {
		t.Errorf("the end line says them: %q", o.err)
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(read(t, filepath.Join(o.leaf, "steps.jsonl"))), "\n") {
		var one map[string]any
		if err := json.Unmarshal([]byte(l), &one); err != nil {
			t.Fatalf("a line of its journal: %q", l)
		}
		lines = append(lines, one)
	}
	if len(lines) != 2 || lines[0]["text"] != "half way" || lines[0]["done"] != 0.5 || lines[0]["residual"] != 0.001 ||
		lines[1]["view"] != "plot.txt" || lines[1]["title"] != "The plot" {
		t.Errorf("its journal: %v", lines)
	}
	if read(t, filepath.Join(o.leaf, "plot.txt")) != "plot\n" {
		t.Error("what it wrote in its own directory is not there")
	}
	if _, err := os.Stat(filepath.Join(o.leaf, "results.jsonl")); err == nil {
		t.Error("the results' file is left behind")
	}
	// without -o, nobody is handed a directory they did not ask for; outside a run, saying does nothing and fails nothing
	if o = p.errand("", "run", "-q", "sh", "-c", "echo ${ERRAND_OUT_DIR:-none}"); o.out != "none\n" {
		t.Errorf("without -o: %q", o.out)
	}
	if o = p.errand("", "say", "step", "by hand", "--done", "0.1"); o.code != 0 || o.out+o.err != "" {
		t.Errorf("outside a run: exit %d, %q", o.code, o.out+o.err)
	}
}

func TestTheMachineIsShared(t *testing.T) {
	p := newPlace(t)
	hold := p.command("hold", "-x", "-n", "a benchmark")
	in, _ := hold.StdinPipe()
	out, _ := hold.StdoutPipe()
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if n, _ := out.Read(buf); !strings.HasPrefix(string(buf[:n]), "granted") {
		t.Fatalf("hold said %q", buf[:n])
	}
	time.AfterFunc(1500*time.Millisecond, func() { in.Close() })
	o := p.errand("", "run", "sh", "-c", "echo ran")
	_ = hold.Wait()
	if o.code != 0 || o.out != "ran\n" || !strings.Contains(o.err, "waiting for a benchmark, which has the machine to itself") {
		t.Fatalf("exit %d, %q %q", o.code, o.out, o.err)
	}
	// the work, not the waiting
	if waited := o.finishedAt.Sub(o.startedAt).Seconds(); waited < 1.2 || *o.result.Duration > 0.8 {
		t.Errorf("waited %.1f s in all, and its record says %.2f s", waited, *o.result.Duration)
	}
	// inside a run, the machine is granted already: nothing in it queues again
	if o = p.errand("", "run", "-q", "-x", "sh", "-c", `"$ERRAND" run -q -x sh -c "echo inner"`); o.out != "inner\n" {
		t.Errorf("an errand inside an errand: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "queue"); !strings.Contains(o.out, "nothing held") {
		t.Errorf("left held: %q", o.out)
	}
}

// jobsLeft: the records of jobs this machine's agents still keep
func (p *scene) jobsLeft(cache string) []string {
	left, _ := filepath.Glob(filepath.Join(cache, "place", "*", "*", "jobs", "*"))
	return left
}

func (p *scene) cache() string { return filepath.Join(filepath.Dir(p.root), "cache") }

func TestAnAgentKilledLosesItsJobs(t *testing.T) {
	p := newPlace(t)
	// the agent killed outright, by its own job: the command that follows is told the way to it broke; the next
	// agent ends what the first left running, and the run's record says why ( ADR 0008 )
	mark := filepath.Join(filepath.Dir(p.root), "pid")
	o := p.errand("", "run", "sh", "-c", "echo before >&2; echo $$ > "+mark+"; sleep 0.4; kill -9 $PPID; sleep 30")
	if o.code == 0 || !strings.Contains(o.err, "this machine's agent went away") {
		t.Fatalf("exit %d, %q", o.code, o.err)
	}
	pid := strings.TrimSpace(read(t, mark))
	if o = p.errand("", "status"); !strings.Contains(o.out, "ended: FAIL") {
		t.Fatalf("status after its agent was killed: %q %q", o.out, o.err)
	}
	if exec.Command("kill", "-0", pid).Run() == nil {
		t.Errorf("what the agent ran ( %s ) still runs", pid)
	}
	if _, r, ok := p.latest(); !ok || r.Status != "FAIL" || !strings.Contains(*r.Error, "the agent that held this run was killed") || !strings.Contains(*r.Error, "before") {
		t.Errorf("its record: %+v", r)
	}
	if o = p.errand("", "attach"); o.code == 0 || !strings.Contains(o.err, "before") {
		t.Errorf("shown from what it left: exit %d, %q %q", o.code, o.out, o.err)
	}
}

func TestCtrlCStopsTheWork(t *testing.T) {
	p := newPlace(t)
	mark := filepath.Join(filepath.Dir(p.root), "still-running")
	cmd := p.command("run", "-q", "sh", "-c", "trap 'echo stopping; exit 9' INT; echo $$ > "+mark+"; while :; do sleep 0.1; done")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	pid := strings.TrimSpace(read(t, mark))
	_ = cmd.Process.Signal(syscall.SIGINT)
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 130 {
		t.Fatalf("errand ended with %v", err)
	}
	if exec.Command("kill", "-0", pid).Run() == nil {
		t.Errorf("what it ran ( %s ) still runs", pid)
	}
	if _, r, ok := p.latest(); !ok || r.Status != "FAIL" || !strings.Contains(out.String(), "stopping") {
		t.Errorf("its record: %+v; it said %q", r, out.String())
	}
}

// python: an interpreter to read a project's declarations with -- none: the cases that declare something are skipped
func python(t *testing.T) []string {
	t.Helper()
	for _, py := range []string{os.Getenv("ERRAND_DECLARE_PYTHON"), "/opt/homebrew/bin/python3.14", "python3"} {
		if py != "" && exec.Command(py, "-c", "import sys; sys.exit( sys.version_info < ( 3, 8 ) )").Run() == nil {
			at, _ := exec.LookPath(py) // ( by its path: some cases put another `python3` before it )
			return []string{"ERRAND_DECLARE_PYTHON=" + at}
		}
	}
	t.Skip("no Python here: a project's declarations cannot be read")
	return nil
}

func TestAProjectsEnvironments(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "fast" ] = errand.Env( [ errand.Vars( { "MODE": "fast", "HERE": "{root}/data" } ) ], fp = "32|64", cuda = False )
errand.envs[ "by-tags" ] = errand.Env( [ errand.Vars( lambda tags: { "FP": "f" + str( tags.get( "fp" ) ) } ) ], fp = "64" )
errand.envs[ "tool" ] = errand.Env( [ errand.Venv( "tools/py" ), errand.Vars( { "MODE": "outer" } ), errand.Vars( { "MODE": "inner" } ) ] )
errand.envs[ "far" ] = errand.Env( [ errand.Ssh( host = "nowhere", root = "/p" ) ] )
errand.default_env = "fast"
`)
	p.write("tools/py", "#!/bin/sh\necho \"the tool's python: $@\"\n")
	p.write("sub/job.py", "print( 'never run by a real python here' )\n")
	o := p.errand("", "run", "-q", "sh", "-c", "echo $MODE $HERE $ERRAND_TAGS")
	if o.code != 0 || o.out != "fast "+p.root+"/data fp=32|64\n" || o.result.Env != "fast" || o.result.Tags["fp"] != "32|64" {
		t.Fatalf("the default environment: exit %d, %q %q", o.code, o.out, o.err)
	}
	// nothing changed: no Python is started to read the project again
	was := p.env
	p.env = append(append([]string{}, p.env...), "ERRAND_DECLARE_PYTHON=/nonexistent/python")
	if o = p.errand("", "run", "-q", "--env", "by-tags", "sh", "-c", "echo $FP"); o.code != 0 || o.out != "f64\n" {
		t.Fatalf("a function of the tags, read from the document kept: exit %d, %q %q", o.code, o.out, o.err)
	}
	p.write("errand-envs.py", read(t, filepath.Join(p.root, "errand-envs.py"))+"\n# changed\n")
	if o = p.errand("", "run", "-q", "true"); o.code != 1 || !strings.Contains(o.err, "declarations could not be read") {
		t.Errorf("a project that changed is read again: exit %d, %q", o.code, o.err)
	}
	p.env = was
	// a .py is run by the place's Python; of two layers that set one variable, the outer one is heard
	o = p.errand("", "run", "-q", "--env", "tool", "sub/job.py", "--n", "3")
	if o.code != 0 || o.out != "the tool's python: "+p.root+"/sub/job.py --n 3\n" || o.result.File != "sub/job.py" {
		t.Fatalf("a .py in an environment: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "run", "-q", "--env", "tool", "sh", "-c", "echo $MODE"); o.out != "outer\n" {
		t.Errorf("two layers, one variable: %q", o.out)
	}
	// from a directory of the project, the project is found, and a file is named from where one stands
	cmd := p.command("run", "-q", "--env", "tool", "job.py")
	cmd.Dir = filepath.Join(p.root, "sub")
	if out, _ := cmd.Output(); string(out) != "the tool's python: "+p.root+"/sub/job.py\n" {
		t.Errorf("from a directory of the project: %q", out)
	}
	if o = p.errand("", "run", "--env", "nope", "true"); o.code != 1 || !strings.Contains(o.err, `no environment "nope"`) || !strings.Contains(o.err, "by-tags, far, fast, tool") {
		t.Errorf("an environment nobody declared: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run", "--env", "far", "true"); o.code != 1 || !strings.Contains(o.err, "cannot reach nowhere: ssh: Could not resolve hostname") || !strings.Contains(o.err, "`ssh nowhere true` must work") {
		t.Errorf("a machine that cannot be reached: exit %d, %q", o.code, o.err)
	}
}

func TestGrids(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
show = errand.Vars( lambda tags: { "FP": str( tags.get( "fp", "any" ) ), "WHO": str( tags.get( "who", "" ) ) } )
errand.envs[ "a" ] = errand.Env( [ show ], who = "a", fp = "32|64" )
errand.envs[ "b" ] = errand.Env( [ show ], who = "b", fp = "64" )
errand.envs[ "c" ] = errand.Env( [ errand.Vars( { "WHO": "c", "FP": "none" } ) ], who = "c", cuda = True )
errand.default_env = "a"
`)
	p.write("one.sh", "#!/bin/sh\necho one $WHO $FP \"$@\"\n")
	p.write("two.sh", "#!/bin/sh\necho two $WHO $FP \"$@\"\n")
	p.write("data.sh", "not a target: an argument\n")
	lines := func(o outcome) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(o.out, "\n", " ; ")), " ")
	}

	// several values make several runs only under --grid, and the grid is said before anything starts
	o := p.errand("", "run", "--grid=,", "--env", "a,b", "one.sh")
	if o.code != 0 || lines(o) != "one a 32|64 ; one b 64 ;" || !strings.Contains(o.err, "2 runs: env = a | b") {
		t.Fatalf("a grid of environments: exit %d, %q\n%s", o.code, lines(o), o.err)
	}
	if o = p.errand("", "run", "--env", "a,b", "one.sh"); o.code != 2 || !strings.Contains(o.err, "--grid=, --env a,b") {
		t.Errorf("a comma without --grid: exit %d, %q", o.code, o.err)
	}
	// a tag's flag selects every environment that can have the value; saying nothing means every value
	o = p.errand("", "run", "-q", "--fp", "64", "one.sh")
	if lines(o) != "one a 64 ; one b 64 ; one c none ;" {
		t.Errorf("--fp 64: %q %q", lines(o), o.err)
	}
	o = p.errand("", "run", "--grid=+", "--fp", "32+64", "--env", "a", "one.sh", "x,y")
	if lines(o) != "one a 32 x,y ; one a 64 x,y ;" || !strings.Contains(o.err, "2 runs: fp = 32 | 64") {
		t.Errorf("a grid of a tag, with another separator: %q %q", lines(o), o.err)
	}
	// each run has its record: the same stamp, a name of its own
	runs, _ := filepath.Glob(filepath.Join(p.root, "errand-outputs", "runs", "one.sh", "*args=x,y*"))
	if len(runs) < 2 || filepath.Base(runs[0])[:19] != filepath.Base(runs[len(runs)-1])[:19] {
		t.Errorf("the runs of the grid: %v", runs)
	}
	if o = p.errand("", "run", "--env", "b", "--fp", "32", "one.sh"); o.code != 1 || !strings.Contains(o.err, "cannot be fp=32") {
		t.Errorf("an environment named that cannot have the value: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run", "-q", "-t", "cuda | who=b", "one.sh"); lines(o) != "one b 64 ; one c none ;" {
		t.Errorf("an expression: %q %q", lines(o), o.err)
	}
	if o = p.errand("", "run", "--nonsense", "1", "one.sh"); o.code != 2 || !strings.Contains(o.err, "nor a tag of an environment") {
		t.Errorf("a flag that is nobody's: exit %d, %q", o.code, o.err)
	}
	// several targets: a comma, or files of one kind in a row -- up to the first word that is not one
	if o = p.errand("", "run", "-q", "one.sh,two.sh", "arg"); lines(o) != "one a 32|64 arg ; two a 32|64 arg ;" {
		t.Errorf("two targets by a comma: %q %q", lines(o), o.err)
	}
	if o = p.errand("", "run", "-q", "one.sh", "two.sh", "--", "data.sh"); lines(o) != "one a 32|64 -- data.sh ; two a 32|64 -- data.sh ;" {
		t.Errorf("files in a row: %q %q", lines(o), o.err)
	}
	// one of them fails: the command says so, the others ran
	p.write("bad.sh", "#!/bin/sh\necho bad; exit 4\n")
	if o = p.errand("", "run", "-q", "bad.sh,one.sh"); o.code != 1 || lines(o) != "bad ; one a 32|64 ;" {
		t.Errorf("one of several fails: exit %d, %q", o.code, lines(o))
	}
}

func TestLetItRun(t *testing.T) {
	p := newPlace(t)
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("long.sh", "#!/bin/sh\necho started; read nothing && echo read-something; while [ ! -e "+gate+" ]; do sleep 0.1; done; echo finished; exit 5\n")
	o := p.errand("", "run", "--batch", "long.sh")
	id := strings.Fields(o.out + " x")[0]
	if o.code != 0 || len(id) != 16 || !strings.Contains(o.err, "errand attach "+id) {
		t.Fatalf("launched: exit %d, %q %q", o.code, o.out, o.err)
	}
	if waited := o.finishedAt.Sub(o.startedAt); waited > 3*time.Second {
		t.Errorf("the shell was given back after %v", waited)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, id[:8]) || !strings.Contains(o.out, "running") || !strings.Contains(o.out, "long.sh") {
		t.Fatalf("status: %q", o.out)
	}
	// followed again, from its beginning, by its id or the beginning of it; the command that follows may go, and come back
	attach := p.command("attach", id[:6])
	var seen bytes.Buffer
	attach.Stdout = &seen
	_ = attach.Start()
	time.Sleep(800 * time.Millisecond)
	_ = attach.Process.Kill()
	_ = attach.Wait()
	if seen.String() != "started\n" {
		t.Errorf("followed again: %q", seen.String())
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "running") {
		t.Fatalf("a follower that went took the run with it: %q", o.out)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach"); o.code != 5 || o.out != "started\nfinished\n" || !strings.Contains(o.err, "│ FAIL long.sh") {
		t.Errorf("followed to its end: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "nothing runs ") || strings.Contains(o.out, "left to be looked at") {
		t.Errorf("once looked at, it is not listed any more: %q", o.out)
	}
	if left := p.jobsLeft(p.cache()); len(left) != 0 {
		t.Errorf("left with this machine's agent: %v", left)
	}

	// one that ended with nobody looking: said by status, shown whole by attach, then forgotten
	o = p.errand("", "run", "--batch", "-q", "sh", "-c", "echo quick; echo to-err >&2")
	id = strings.TrimSpace(strings.Fields(o.out)[0])
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if o = p.errand("", "status"); strings.Contains(o.out, "ended: PASS") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(o.out, "ended: PASS") {
		t.Fatalf("status of one that ended: %q", o.out)
	}
	if o = p.errand("", "attach", id); o.code != 0 || o.out != "quick\n" || !strings.Contains(o.err, "to-err") || !strings.Contains(o.err, "│ PASS") {
		t.Errorf("shown from its record: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "attach", id); o.code != 1 || !strings.Contains(o.err, "no run "+id) {
		t.Errorf("attached twice: exit %d, %q", o.code, o.err)
	}

	// stopped by its id: its record says how, nothing of it runs on
	_ = os.Remove(gate)
	o = p.errand("", "run", "--batch", "-q", "long.sh")
	id = strings.Fields(o.out)[0]
	if o = p.errand("", "stop", id[:8], "nope"); o.code != 1 || !strings.Contains(o.out, "stopped") || !strings.Contains(o.err, "no run nope") {
		t.Errorf("stop: exit %d, %q %q", o.code, o.out, o.err)
	}
	if _, r, ok := p.latest(); !ok || r.Status != "FAIL" || r.Name != "long.sh" {
		t.Errorf("the record of a run that was stopped: %+v", r)
	}
	if o = p.errand("", "queue"); !strings.Contains(o.out, "nothing held") {
		t.Errorf("what it held of the machine: %q", o.out)
	}
}

func TestAnotherMachine(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.configure( zones = { "/results": "back" } )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Vars( { "WHERE": "there" } ) ] )
`)
	copyOf := filepath.Join(p.far, "home", "copy", "proj")
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("job.sh", "#!/bin/sh\necho \"in $(pwd) $WHERE $@\"; mkdir -p results; echo r > results/r.txt; echo m > \"$ERRAND_OUT_DIR/made.txt\"; echo to-err >&2\n")
	p.write("sub/data.txt", "one\n")
	p.write("errand-outputs/runs/old/x", "a run of here, which is not the copy's\n")

	// the project pushed, the run made over there, followed from here, and what it wrote brought back
	o := p.errand("", "run", "-q", "-o", "--env", "box", "job.sh", "a", "b")
	if o.code != 0 || o.out != "in "+copyOf+" there a b\n" || !strings.Contains(o.err, "to-err") {
		t.Fatalf("a run elsewhere: exit %d, %q %q", o.code, o.out, o.err)
	}
	if !strings.Contains(o.err, "sending itself to box") {
		t.Errorf("the program was not said sent: %q", o.err)
	}
	if !o.hasResult || o.result.Status != "PASS" || o.result.Env != "box" || !strings.HasPrefix(o.result.Place, "box@") || o.result.Project != copyOf {
		t.Fatalf("its record, here: %+v", o.result)
	}
	for _, name := range []string{"output.txt", "stdout.txt", "made.txt", "steps.jsonl"[:0] + "launch.json"} {
		if _, err := os.Stat(filepath.Join(o.leaf, name)); err != nil {
			t.Errorf("not brought back: %s", name)
		}
	}
	if read(t, filepath.Join(p.root, "results", "r.txt")) != "r\n" {
		t.Errorf("a `back` zone did not come back")
	}
	if read(t, filepath.Join(copyOf, "sub", "data.txt")) != "one\n" {
		t.Errorf("the project was not pushed")
	}
	if _, err := os.Stat(filepath.Join(copyOf, "errand-outputs", "runs", "old")); err == nil {
		t.Errorf("errand's outputs here were pushed: each side's are its own")
	}
	if left := p.jobsLeft(filepath.Join(p.far, "cache")); len(left) != 0 {
		t.Errorf("left with the agent over there: %v", left)
	}

	// a second command: nothing is sent again but what changed; the exit code, and what is read, are the command's
	p.write("sub/data.txt", "two\n")
	_ = os.Remove(filepath.Join(p.root, "job.sh"))
	o = p.errand("fed\n", "run", "-q", "--env", "box", "sh", "-c", "cat; cat sub/data.txt; ls; exit 3")
	if o.code != 3 || o.out != "fed\ntwo\nerrand-envs.py\nerrand-outputs\nresults\nsub\n" {
		t.Errorf("a second run: exit %d, %q %q", o.code, o.out, o.err)
	}
	if strings.Contains(o.err, "sending itself") {
		t.Errorf("the program was sent twice: %q", o.err)
	}
	if o.result.Status != "FAIL" || o.result.Exit == nil || *o.result.Exit != 3 {
		t.Errorf("the record of a run that failed elsewhere: %+v", o.result)
	}
	// from a directory of the project, a file is named from where one stands, and runs from there over there
	p.write("sub/where.sh", "#!/bin/sh\npwd\n")
	cmd := p.command("run", "-q", "--env", "box", "where.sh")
	cmd.Dir = filepath.Join(p.root, "sub")
	if out, _ := cmd.Output(); string(out) != copyOf+"/sub\n" {
		t.Errorf("from a directory of the project: %q", out)
	}
	outside := filepath.Join(filepath.Dir(p.root), "outside.sh")
	_ = os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755)
	if o = p.errand("", "run", "--env", "box", outside); o.code != 1 || !strings.Contains(o.err, "is not in the project") {
		t.Errorf("a file that is not in the project: exit %d, %q", o.code, o.err)
	}

	// launched and left: it runs over there, is listed here, followed again, and comes back when it is looked at
	p.write("long.sh", "#!/bin/sh\necho started; while [ ! -e "+gate+" ]; do sleep 0.1; done; echo finished; exit 5\n")
	o = p.errand("", "run", "--batch", "--env", "box", "long.sh")
	id := strings.Fields(o.out + " x")[0]
	if o.code != 0 || len(id) != 16 || !strings.Contains(o.err, "launched on ") {
		t.Fatalf("launched elsewhere: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, id[:8]) || !strings.Contains(o.out, "running") || !strings.Contains(o.out, "    box ") {
		t.Fatalf("status: %q", o.out)
	}
	attach := p.command("attach", id[:6])
	var seen bytes.Buffer
	attach.Stdout = &seen
	_ = attach.Start()
	time.Sleep(1500 * time.Millisecond)
	_ = attach.Process.Kill()
	_ = attach.Wait()
	if seen.String() != "started\n" {
		t.Errorf("followed again: %q", seen.String())
	}
	_ = os.WriteFile(gate, nil, 0o644)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if o = p.errand("", "status"); strings.Contains(o.out, "ended: FAIL") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(o.out, "ended: FAIL") {
		t.Fatalf("status of one that ended elsewhere: %q %q", o.out, o.err)
	}
	if _, r, ok := p.latest(); !ok || r.Name != "long.sh" || r.Status != "FAIL" {
		t.Errorf("status did not bring its record back: %+v", r)
	}
	if o = p.errand("", "attach", id); o.code != 5 || o.out != "started\nfinished\n" || !strings.Contains(o.err, "│ FAIL long.sh") {
		t.Errorf("shown from what came back: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "nothing runs ") || strings.Contains(o.out, "left to be looked at") {
		t.Errorf("once looked at, it is not listed any more: %q", o.out)
	}

	// the way to it breaks while it is followed: the run goes on over there, and is followed again to its end
	_ = os.Remove(gate)
	run := p.command("run", "-q", "--env", "box", "long.sh")
	seen.Reset()
	var said bytes.Buffer
	run.Stdout, run.Stderr = &seen, &said
	_ = run.Start()
	for i := 0; i < 80 && !strings.Contains(seen.String(), "started"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = exec.Command("pkill", "-f", filepath.Join(p.far, "cache", "bin")+".* there").Run()
	if err := run.Wait(); err == nil || !strings.Contains(said.String(), "The run goes on if it was running") {
		t.Errorf("a connection lost: %v, %q", err, said.String())
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "running") {
		t.Fatalf("the run went with its connection: %q %q", o.out, o.err)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach"); o.code != 5 || o.out != "started\nfinished\n" {
		t.Errorf("followed again after the connection was lost: exit %d, %q %q", o.code, o.out, o.err)
	}

	// stopped from here; the machine over there given back
	_ = os.Remove(gate)
	o = p.errand("", "run", "--batch", "-q", "--env", "box", "long.sh")
	id = strings.Fields(o.out + " x")[0]
	if o = p.errand("", "stop", id[:8]); o.code != 0 || !strings.Contains(o.out, "stopped") {
		t.Errorf("stop: exit %d, %q %q", o.code, o.out, o.err)
	}
	far := p.command("queue")
	far.Env = append(append([]string{}, p.env...), "XDG_RUNTIME_DIR="+filepath.Join(p.far, "queue"))
	if out, _ := far.Output(); !strings.Contains(string(out), "nothing held") {
		t.Errorf("what it held of the machine over there: %q", out)
	}
	// Ctrl-C here stops the work there
	run = p.command("run", "-q", "--env", "box", "long.sh")
	seen.Reset()
	run.Stdout = &seen
	_ = run.Start()
	for i := 0; i < 80 && !strings.Contains(seen.String(), "started"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = run.Process.Signal(os.Interrupt)
	err := run.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 130 {
		t.Errorf("Ctrl-C: %v, %q", err, seen.String())
	}
	if out, _ := exec.Command("pgrep", "-f", filepath.Join(copyOf, "long.sh")).Output(); len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("still running over there after Ctrl-C: %s", out)
	}
	// an errand of a release before reaches a machine where a newer one is in use: told to update, by that machine
	// ( this machine's own newer agent gone first: here, it would be this machine that says so )
	_ = exec.Command("pkill", "-f", "place --dir "+p.cache()).Run()
	time.Sleep(500 * time.Millisecond)
	p.program = builtOld
	if o = p.errand("", "run", "--env", "box", "true"); o.code != 1 || !strings.Contains(o.err, "box: a newer errand") || !strings.Contains(o.err, "update it") {
		t.Errorf("an older errand, elsewhere: exit %d, %q", o.code, o.err)
	}
}

func TestAnUpdateIsARelay(t *testing.T) {
	p := newPlace(t)
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	agents := func() int { // the agents of this machine that live
		out, _ := exec.Command("pgrep", "-f", filepath.Join(p.cache(), "place")).Output()
		return len(strings.Fields(string(out)))
	}
	p.write("long.sh", "#!/bin/sh\necho started; while [ ! -e "+gate+" ]; do sleep 0.1; done; echo finished; exit 5\n")
	o := p.errand("", "run", "--batch", "-q", "long.sh")
	id := strings.Fields(o.out + " x")[0]
	if o.code != 0 || len(id) != 16 {
		t.Fatalf("launched: exit %d, %q %q", o.code, o.out, o.err)
	}

	// another version of errand is typed on the same machine: its agent takes what is new, the one before answers
	// for its job through it
	p.program = builtNext
	if o = p.errand("", "run", "-q", "echo", "new"); o.code != 0 || o.out != "new\n" {
		t.Fatalf("a run by the new version: exit %d, %q %q", o.code, o.out, o.err)
	}
	if n := agents(); n != 2 {
		t.Fatalf("%d agents live, while the one before still has a job", n)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, id[:8]) || !strings.Contains(o.out, "running") {
		t.Fatalf("the job of the agent before, asked of the new one: %q %q", o.out, o.err)
	}
	attach := p.command("attach", id)
	var seen bytes.Buffer
	attach.Stdout = &seen
	_ = attach.Start()
	for i := 0; i < 50 && !strings.Contains(seen.String(), "started"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	err := attach.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 5 || seen.String() != "started\nfinished\n" {
		t.Errorf("followed through the new agent: %v, %q", err, seen.String())
	}
	// its last job ended: the agent before goes by itself
	for i := 0; i < 50 && agents() != 1; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if n := agents(); n != 1 {
		t.Errorf("%d agents live once the job of the one before ended", n)
	}
	if left := p.jobsLeft(p.cache()); len(left) != 0 {
		t.Errorf("left with this machine's agents: %v", left)
	}

	// stopped through the new agent, too
	_ = os.Remove(gate)
	p.program = ""
	o = p.errand("", "run", "--batch", "-q", "long.sh")
	id = strings.Fields(o.out + " x")[0]
	p.program = builtNext
	if o = p.errand("", "run", "-q", "true"); o.code != 0 {
		t.Fatalf("exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "stop", id[:8]); o.code != 0 || !strings.Contains(o.out, "stopped") {
		t.Errorf("stopped through the new agent: exit %d, %q %q", o.code, o.out, o.err)
	}

	// an errand of a release before finds a newer one in use here: it is told to update, and starts nothing
	p.program = builtOld
	if o = p.errand("", "run", "echo", "x"); o.code != 1 || o.out != "" || !strings.Contains(o.err, "a newer errand") || !strings.Contains(o.err, "update it") {
		t.Errorf("an older errand: exit %d, %q %q", o.code, o.out, o.err)
	}
}

// tools: a `python3` and a `pip` of a moment, so that building an environment asks nothing of the network -- a venv
// made by it holds a `python` that writes down what it is asked to install, and says its packages' directory changed
func (p *scene) tools() string {
	dir := filepath.Join(filepath.Dir(p.root), "tools")
	_ = os.MkdirAll(dir, 0o755)
	inner := `#!/bin/sh
here="$(cd "$(dirname "$0")/.." && pwd)"
case "$1 $2 $3" in
"-m pip install") shift 3; echo "pip install $*" >> "$here/installed"
    case "$*" in *broken*) echo "ERROR: no matching distribution for broken" >&2; exit 1;; *slow*) sleep 1;; esac
    echo "Successfully installed $*";;
"-c "*) echo "$(wc -l < "$here/installed" | tr -d ' ') 2";;
*) echo "the venv's python: $*";;
esac
`
	_ = os.WriteFile(filepath.Join(dir, "python3"), []byte(`#!/bin/sh
[ "$1 $2" = "-m venv" ] || { echo "python3 $*"; exit 0; }
mkdir -p "$3/bin" && : >> "$3/installed" && echo made >> "$3/made" && cat > "$3/bin/python" <<'INNER'
`+inner+`INNER
chmod +x "$3/bin/python"
`), 0o755)
	p.env = append(p.env, "PATH="+dir+":"+os.Getenv("PATH"))
	return dir
}

func TestEnvironmentsAreKeptCurrent(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.tools()
	declare := func(pip string) {
		p.write("errand-envs.py", `import errand
errand.envs[ "v" ] = errand.Env( [ errand.Venv( python = "env/bin/python", create = True, requirements = "requirements.txt", pip = [ `+pip+` ] ) ] )
errand.default_env = "v"
`)
	}
	declare(`"numpy"`)
	p.write("requirements.txt", "scipy\n")
	p.write("job.py", "pass\n")
	installed := func() string { return read(t, filepath.Join(p.root, "env", "installed")) }
	record := filepath.Join(p.root, "errand-outputs", "builds", "records")

	if o := p.errand("", "envs"); !strings.Contains(o.out, "v ") || !strings.Contains(o.out, "not built") {
		t.Fatalf("before anything: %q %q", o.out, o.err)
	}
	// the first command that needs it builds it, where it runs, and says so
	o := p.errand("", "run", "job.py", "a")
	if o.code != 0 || o.out != "the venv's python: "+p.root+"/job.py a\n" || !strings.Contains(o.err, "bringing up to date venv:env/bin/python") {
		t.Fatalf("built by the first command: exit %d, %q %q", o.code, o.out, o.err)
	}
	if installed() != "pip install -r requirements.txt\npip install numpy\n" {
		t.Fatalf("what was installed: %q", installed())
	}
	if o = p.errand("", "envs"); !strings.Contains(o.out, " ok ") {
		t.Errorf("once built: %q", o.out)
	}
	// nothing changed: nothing is built again, and nothing is said
	if o = p.errand("", "run", "job.py"); o.code != 0 || strings.Contains(o.err, "bringing") || installed() != "pip install -r requirements.txt\npip install numpy\n" {
		t.Errorf("built again with nothing changed: %q %q", o.err, installed())
	}
	// its declaration changes -- a file it names, or what it lists: the next command brings it up to date
	p.write("requirements.txt", "scipy\nsympy\n")
	if o = p.errand("", "envs"); !strings.Contains(o.out, "stale") {
		t.Errorf("a changed requirements file: %q", o.out)
	}
	if o = p.errand("", "run", "--no-setup", "-q", "job.py"); o.code != 0 || strings.Count(installed(), "\n") != 2 {
		t.Errorf("--no-setup built something: %q", installed())
	}
	if o = p.errand("", "run", "-q", "job.py"); o.code != 0 || strings.Count(installed(), "\n") != 4 {
		t.Errorf("brought up to date: exit %d, %q %q", o.code, o.err, installed())
	}
	if made := read(t, filepath.Join(p.root, "env", "made")); made != "made\nmade\n" {
		t.Logf("( the venv's creation was run %d times )", strings.Count(made, "\n"))
	}

	// a build that fails stops the run, says where its output is, and is tried again by the next command
	declare(`"numpy", "broken"`)
	o = p.errand("", "run", "job.py")
	if o.code != 1 || o.out != "" || !strings.Contains(o.err, "failed while preparing venv:env/bin/python") || !strings.Contains(o.err, "no matching distribution") {
		t.Fatalf("a build that fails: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o.result.Status != "FAIL" || !strings.Contains(*o.result.Error, "failed while preparing") {
		t.Errorf("its run's record: %+v", o.result)
	}
	if o = p.errand("", "envs"); !strings.Contains(o.out, "last build failed") {
		t.Errorf("after a failure: %q", o.out)
	}
	if logs, _ := filepath.Glob(filepath.Join(record, "*.log")); len(logs) != 1 || !strings.Contains(read(t, logs[0]), "no matching distribution") {
		t.Errorf("the output of the last attempt: %v", logs)
	}
	declare(`"numpy"`)
	before := strings.Count(installed(), "\n")
	if o = p.errand("", "run", "-q", "job.py"); o.code != 0 || strings.Count(installed(), "\n") == before {
		t.Errorf("tried again after a failure: exit %d, %q", o.code, o.err)
	}

	// what is there and was never seen was built by somebody: adopted, not rebuilt
	_ = os.RemoveAll(record)
	before = strings.Count(installed(), "\n")
	if o = p.errand("", "run", "-q", "job.py"); o.code != 0 || strings.Count(installed(), "\n") != before {
		t.Errorf("an environment never seen before was rebuilt: %q", o.err)
	}
	if o = p.errand("", "envs"); !strings.Contains(o.out, "adopted") {
		t.Errorf("after an adoption: %q", o.out)
	}
	// errand setup: now, and nothing else; --dry-run says what it would run; --force rebuilds what was adopted
	if o = p.errand("", "setup", "--dry-run", "--force"); o.code != 0 || !strings.Contains(o.err, "would run: python3 -m venv env") || strings.Count(installed(), "\n") != before {
		t.Errorf("setup --dry-run: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "setup", "--force"); o.code != 0 || !strings.Contains(o.err, "rebuilding venv:env/bin/python") || strings.Count(installed(), "\n") == before {
		t.Errorf("setup --force: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "envs"); !strings.Contains(o.out, " ok ") {
		t.Errorf("after setup --force: %q", o.out)
	}

	// two commands at once that need the same thing built: one builds it, the other waits for it
	declare(`"numpy", "slow"`)
	before = strings.Count(installed(), "\n")
	first, second := p.command("run", "job.py"), p.command("run", "job.py")
	var said1, said2 bytes.Buffer
	first.Stderr, second.Stderr = &said1, &said2
	_ = first.Start()
	_ = second.Start()
	err1, err2 := first.Wait(), second.Wait()
	if err1 != nil || err2 != nil || strings.Count(installed(), "\n") != before+2 {
		t.Errorf("two at once: %v %v, %d installs\n%s\n%s", err1, err2, strings.Count(installed(), "\n")-before, said1.String(), said2.String())
	}
	if both := said1.String() + said2.String(); strings.Count(both, "bringing up to date") != 1 || !strings.Contains(both, "is being prepared by") {
		t.Errorf("two at once, what was said:\n%s\n%s", said1.String(), said2.String())
	}
	if locks, _ := filepath.Glob(filepath.Join(record, "*.lock")); len(locks) != 0 {
		t.Errorf("a lock left: %v", locks)
	}

	// launched and left: the installation goes on without whoever asked
	declare(`"slow", "more"`)
	o = p.errand("", "run", "--batch", "-q", "job.py")
	if waited := o.finishedAt.Sub(o.startedAt); o.code != 0 || waited > 900*time.Millisecond {
		t.Errorf("--batch waited for the installation: exit %d, %v", o.code, waited)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if o = p.errand("", "status"); strings.Contains(o.out, "ended: PASS") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(o.out, "ended: PASS") || !strings.Contains(installed(), "pip install slow more") {
		t.Errorf("installed in the background: %q, %q", o.out, installed())
	}
}

func TestBuiltWhereItRuns(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.tools()
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ),
                                     errand.Venv( python = "env/bin/python", create = True, requirements = "requirements.txt" ) ] )
`)
	p.write("requirements.txt", "scipy\n")
	p.write("job.py", "pass\n")
	copyOf := filepath.Join(p.far, "home", "copy", "proj")
	// an environment is installed where it runs, from files of the project: pushed first, built over there
	o := p.errand("", "run", "--env", "box", "job.py")
	if o.code != 0 || o.out != "the venv's python: "+copyOf+"/job.py\n" || !strings.Contains(o.err, "bringing up to date venv:env/bin/python") {
		t.Fatalf("built over there: exit %d, %q %q", o.code, o.out, o.err)
	}
	if read(t, filepath.Join(copyOf, "env", "installed")) != "pip install -r requirements.txt\n" {
		t.Errorf("what was installed over there: %q", read(t, filepath.Join(copyOf, "env", "installed")))
	}
	if _, err := os.Stat(filepath.Join(p.root, "env")); err == nil {
		t.Errorf("it was built here too")
	}
	if recs, _ := filepath.Glob(filepath.Join(copyOf, "errand-outputs", "builds", "records", "*.json")); len(recs) != 1 {
		t.Errorf("its record lives with it, over there: %v", recs)
	}
	// what was built there is not removed by the next push, and is not built again
	if o = p.errand("", "run", "--env", "box", "job.py"); o.code != 0 || strings.Contains(o.err, "bringing") || strings.Contains(o.err, "deleted there") {
		t.Errorf("a second run: exit %d, %q", o.code, o.err)
	}
	// ... nor by a push that walks the copy again ( its agent gone, another started: the copy is looked at whole )
	_ = exec.Command("pkill", "-f", filepath.Join(p.far, "cache", "place")).Run()
	time.Sleep(300 * time.Millisecond)
	p.write("job.py", "pass # changed\n")
	if o = p.errand("", "run", "--env", "box", "job.py"); o.code != 0 || strings.Contains(o.err, "bringing") || strings.Contains(o.err, "moved aside") {
		t.Errorf("after the copy was walked again: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "envs"); !strings.Contains(o.out, "box") || !strings.Contains(o.out, " ok ") {
		t.Errorf("its state, asked of the machine that holds it: %q %q", o.out, o.err)
	}
	p.write("requirements.txt", "scipy\nsympy\n")
	if o = p.errand("", "setup", "box"); o.code != 0 || !strings.Contains(o.err, "bringing up to date") ||
		strings.Count(read(t, filepath.Join(copyOf, "env", "installed")), "\n") != 2 {
		t.Errorf("errand setup, over there: exit %d, %q", o.code, o.err)
	}
}

func TestACommandIsCarriedOutByTheAgent(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ 'local' ] = errand.Env( [ ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	agents := func() []string {
		out, _ := exec.Command("pgrep", "-f", "place --dir "+p.cache()).Output()
		return strings.Fields(string(out))
	}
	logins := func() int { return strings.Count(read(t, filepath.Join(p.far, "log")), "\n") }
	// one agent on this machine, started by the first command, there for the next ones: typing a command starts
	// nothing that does the work
	if o := p.errand("", "run", "-q", "echo", "one"); o.code != 0 || o.out != "one\n" {
		t.Fatalf("exit %d, %q %q", o.code, o.out, o.err)
	}
	first := agents()
	if o := p.errand("", "run", "-q", "echo", "two"); o.out != "two\n" || len(first) != 1 || strings.Join(agents(), " ") != first[0] {
		t.Fatalf("the agents: %v then %v", first, agents())
	}
	// a command runs in the surroundings and the directory it was typed in, not in those of the day its agent started
	was := p.env
	p.env = append(append([]string{}, was...), "COLOUR=green")
	p.write("sub/x", "")
	cmd := p.command("run", "-q", "sh", "-c", "echo $COLOUR in $(basename $(pwd))")
	cmd.Dir = filepath.Join(p.root, "sub")
	if out, _ := cmd.Output(); string(out) != "green in sub\n" {
		t.Errorf("a command's own surroundings: %q", out)
	}
	p.env = was
	if o := p.errand("", "run", "-q", "sh", "-c", "echo ${COLOUR:-none}"); o.out != "none\n" {
		t.Errorf("the surroundings of a command before leaked into the next: %q", o.out)
	}
	// the project is read by the command line, in the shell it is typed in: its agent starts no Python
	p.env = append(append([]string{}, was...), "ERRAND_DECLARE_PYTHON=/nonexistent/python")
	p.write("errand-envs.py", read(t, filepath.Join(p.root, "errand-envs.py"))+"\n# changed\n")
	if o := p.errand("", "run", "-q", "true"); o.code != 1 || !strings.Contains(o.err, "declarations could not be read") {
		t.Errorf("read with the Python of the shell that typed the command: exit %d, %q", o.code, o.err)
	}
	p.env = was
	// the link to a machine is kept between commands: the second one logs in nowhere
	if o := p.errand("", "run", "-q", "--env", "box", "echo", "there"); o.out != "there\n" {
		t.Fatalf("exit %d, %q %q", o.code, o.out, o.err)
	}
	before := logins()
	if o := p.errand("", "run", "-q", "--env", "box", "echo", "again"); o.out != "again\n" || logins() != before {
		t.Errorf("the second command logged in %d times: %q %q", logins()-before, o.out, o.err)
	}
}

func TestWhatChangesIsSaid(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("long.sh", "#!/bin/sh\necho started; while [ ! -e "+gate+" ]; do sleep 0.1; done; exit 5\n")
	watch := p.command("watch")
	var seen bytes.Buffer
	watch.Stdout = &seen
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watch.Process.Kill(); _ = watch.Wait() }()
	said := func(what string) bool {
		for i := 0; i < 60; i++ {
			if strings.Contains(seen.String(), what) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	// a run starts, here then on another machine; each is said once, when it happens
	o := p.errand("", "run", "--batch", "-q", "long.sh")
	here := strings.Fields(o.out + " x")[0]
	if !said(here[:8] + "  running  long.sh") {
		t.Fatalf("a run started here was not said: %q", seen.String())
	}
	o = p.errand("", "run", "--batch", "-q", "--env", "box", "long.sh")
	there := strings.Fields(o.out + " x")[0]
	if !said(there[:8] + "  running  long.sh") {
		t.Fatalf("a run started elsewhere was not said: %q", seen.String())
	}
	// nothing happens: nothing is said
	time.Sleep(1500 * time.Millisecond)
	quiet := seen.Len()
	time.Sleep(1500 * time.Millisecond)
	if seen.Len() != quiet {
		t.Errorf("something was said while nothing happened: %q", seen.String()[quiet:])
	}
	// they end; one is looked at
	_ = os.WriteFile(gate, nil, 0o644)
	if !said(here[:8]+"  ended: FAIL  long.sh") || !said(there[:8]+"  ended: FAIL  long.sh") {
		t.Fatalf("their ends were not said: %q", seen.String())
	}
	if o = p.errand("", "attach", here); o.code != 5 || !said(here[:8]+"  forgotten") {
		t.Errorf("looked at, and not said forgotten: exit %d, %q", o.code, seen.String())
	}
	if n := strings.Count(seen.String(), here[:8]); n != 3 {
		t.Errorf("a run said %d times in all ( started, ended, forgotten ):\n%s", n, seen.String())
	}
}

// apptainer: one of a moment, in the tools -- it enters nothing, and says it was asked to: what it starts is told
// which image it is "in", and each entry is written down
func (p *scene) apptainer() string {
	dir := p.tools()
	entered := filepath.Join(filepath.Dir(p.root), "entered")
	_ = os.WriteFile(filepath.Join(dir, "apptainer"), []byte(`#!/bin/sh
if [ "$1" = build ]; then # an image made from a recipe: it says how it was asked, and what the recipe said goes in it
    shift
    echo "build $* | tmp=$APPTAINER_TMPDIR cache=$APPTAINER_CACHEDIR" >> `+p.builds()+`
    while :; do case "$1" in -*) shift;; *) break;; esac; done
    target="$1"; recipe="$2"
    [ -z "$APPTAINER_TMPDIR" ] || [ -d "$APPTAINER_TMPDIR" ] || { echo "FATAL: no such directory $APPTAINER_TMPDIR" >&2; exit 255; }
    [ -d "$(dirname "$target")" ] || { echo "FATAL: no directory for $target" >&2; exit 255; }
    case "$recipe" in docker://*) body="$recipe";; *) body="$(cat "$recipe")" || exit 255;; esac
    echo "building $target"
    case "$body" in *VERYSLOW*) sleep 30;; *SLOW*) sleep 1;; esac
    case "$body" in *FAIL*) echo "FATAL: the build of $target failed" >&2; echo partial > "$target"; exit 255;; esac
    echo "built from: $body" > "$target"
    exit 0
fi
[ "$1" = exec ] || { echo "apptainer $*" >&2; exit 2; }
shift
flags=""
while :; do case "$1" in --bind) flags="$flags $1 $2"; shift 2;; -*) flags="$flags $1"; shift;; *) break;; esac; done
image="$1"; shift
[ -f "$image" ] || { echo "FATAL: could not open image $image" >&2; exit 255; }
echo "$image$flags" >> `+entered+`
IN_CONTAINER="$(basename "$image")" exec "$@"
`), 0o755)
	return entered
}

func TestInsideAContainer(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	entered := p.apptainer()
	p.write("errand-envs.py", `import errand
errand.envs[ "c" ] = errand.Env( [ errand.Apptainer( image = "images/a.sif", flags = [ "--nv" ] ), errand.Vars( { "INNER": "set" } ) ] )
errand.envs[ "gone" ] = errand.Env( [ errand.Apptainer( image = "images/missing.sif" ) ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Apptainer( image = "images/a.sif" ) ] )
`)
	p.write("images/a.sif", "an image\n")
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("long.sh", "#!/bin/sh\necho started; while [ ! -e "+gate+" ]; do sleep 0.1; done; echo finished\n")
	agents := func() []string {
		out, _ := exec.Command("pgrep", "-f", "place --dir "+p.cache()).Output()
		pids := strings.Join(strings.Fields(string(out)), ",")
		out, _ = exec.Command("ps", "-ww", "-o", "pid=,command=", "-p", pids).Output() // ( `pgrep -l` says the arguments on macOS only )
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	// a run in a container is a job of that container's agent, entered once
	o := p.errand("", "run", "--env", "c", "sh", "-c", "echo in $IN_CONTAINER, $INNER")
	if o.code != 0 || o.out != "in a.sif, set\n" || o.result.Status != "PASS" {
		t.Fatalf("a run inside: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "run", "-q", "--env", "c", "sh", "-c", "echo again in $IN_CONTAINER"); o.out != "again in a.sif\n" {
		t.Fatalf("a second run inside: %q %q", o.out, o.err)
	}
	if said := read(t, entered); strings.Count(said, "\n") != 1 || !strings.Contains(said, "--nv") || !strings.Contains(said, "--bind "+p.cache()) {
		t.Errorf("the container was entered: %q", said)
	}
	if now := agents(); len(now) != 2 || !strings.Contains(strings.Join(now, "\n"), "--inside --parent") {
		t.Fatalf("the agents of this machine: %v", now)
	}
	// its place on the machine is granted by the machine's agent, and held there: seen from outside, given back at its end
	o = p.errand("", "run", "--batch", "-q", "-x", "--env", "c", "long.sh")
	id := strings.Fields(o.out + " x")[0]
	held := ""
	for i := 0; i < 50 && !strings.Contains(held, "long.sh"); i++ {
		time.Sleep(100 * time.Millisecond)
		held = p.errand("", "queue").out
	}
	machine := strings.Fields(agents()[0])[0]
	for _, line := range agents() {
		if !strings.Contains(line, "--inside") {
			machine = strings.Fields(line)[0]
		}
	}
	if !strings.Contains(held, "long.sh") || !strings.Contains(held, "pid "+machine) {
		t.Errorf("the machine's queue, while it runs ( the machine's agent is %s ):\n%s", machine, held)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, id[:8]) || !strings.Contains(o.out, "running") {
		t.Errorf("status of a run inside: %q %q", o.out, o.err)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach", id); o.code != 0 || o.out != "started\nfinished\n" {
		t.Errorf("followed, through the machine's agent: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "queue"); !strings.Contains(o.out, "nothing held") {
		t.Errorf("left held: %q", o.out)
	}
	// an image that is not there is said, and nothing runs
	if o = p.errand("", "run", "--env", "gone", "true"); o.code != 1 || !strings.Contains(o.err, "images/missing.sif is not there") {
		t.Errorf("an image that is not there: exit %d, %q", o.code, o.err)
	}
	// the same on another machine: that machine's agent enters the container
	if o = p.errand("", "run", "-q", "--env", "box", "sh", "-c", "echo there in $IN_CONTAINER"); o.code != 0 || o.out != "there in a.sif\n" {
		t.Errorf("inside a container, elsewhere: exit %d, %q %q", o.code, o.out, o.err)
	}
	// the container's agent goes when the machine's does: nothing is left running under an agent that is not there
	_ = exec.Command("kill", "-9", machine).Run()
	for i := 0; i < 50 && len(agents()) > 0 && agents()[0] != ""; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if now := agents(); len(now) > 0 && now[0] != "" {
		t.Errorf("left once the machine's agent was killed: %v", now)
	}
}

func TestAFileByItsURL(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	hits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if strings.HasPrefix(r.URL.Path, "/data/") {
			_, _ = w.Write([]byte("contents of " + r.URL.Path + "\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	p.write("errand-envs.py", `import errand
errand.envs[ 'local' ] = errand.Env( [ ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	downloads := filepath.Join(p.root, "errand-cache", "downloads")
	// a run asks for a file by its URL, and is given its path: fetched once, under the hash of what came
	get := `cat "$("$ERRAND" download ` + server.URL + `/data/a.txt)"`
	o := p.errand("", "run", "-q", "sh", "-c", get+`; "$ERRAND" download `+server.URL+`/data/a.txt`)
	lines := strings.Split(strings.TrimSpace(o.out), "\n")
	if o.code != 0 || len(lines) != 2 || lines[0] != "contents of /data/a.txt" || !strings.HasPrefix(lines[1], downloads+"/") || !strings.HasSuffix(lines[1], "/a.txt") {
		t.Fatalf("a download: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "run", "-q", "sh", "-c", get); o.out != "contents of /data/a.txt\n" || hits["/data/a.txt"] != 1 {
		t.Errorf("asked again: %q, fetched %d times", o.out, hits["/data/a.txt"])
	}
	// a hash, when given, is checked; what does not match is not kept; a refusal is said as it is
	o = p.errand("", "run", "sh", "-c", `"$ERRAND" download `+server.URL+`/data/b.txt --sha256 0000`)
	if o.code == 0 || !strings.Contains(o.err, "its sha256 is") {
		t.Errorf("a hash that does not match: exit %d, %q", o.code, o.err)
	}
	if kept, _ := filepath.Glob(filepath.Join(downloads, "*", "b.txt")); len(kept) != 0 {
		t.Errorf("kept though its hash is not the one asked: %v", kept)
	}
	if o = p.errand("", "run", "sh", "-c", `"$ERRAND" download `+server.URL+`/nowhere`); o.code == 0 || !strings.Contains(o.err, "404") {
		t.Errorf("a URL that answers no: exit %d, %q", o.code, o.err)
	}
	// outside a run, with no way to it: said at once, nobody is waited for
	alone := p.command("download", server.URL+"/data/c.txt")
	alone.Env = append(append([]string{}, p.env...), "ERRAND_NO_INTERNET=1")
	if out, err := alone.CombinedOutput(); err == nil || !strings.Contains(string(out), "nobody to ask") {
		t.Errorf("outside a run: %v, %q", err, out)
	}

	// on a machine with no way out, the run leaves its request and waits; whoever follows it downloads the file
	// here and sends it into the copy
	_ = os.WriteFile(filepath.Join(p.far, "offline"), nil, 0o644)
	there := `cat "$("$ERRAND" download ` + server.URL + `/data/far.txt)"`
	o = p.errand("", "run", "--env", "box", "sh", "-c", there)
	if o.code != 0 || o.out != "contents of /data/far.txt\n" || !strings.Contains(o.err, "has no way to "+server.URL+"/data/far.txt -- downloading it here") {
		t.Fatalf("a download for a machine with no way out: exit %d, %q %q", o.code, o.out, o.err)
	}
	copyOf := filepath.Join(p.far, "home", "copy", "proj")
	if kept, _ := filepath.Glob(filepath.Join(copyOf, "errand-cache", "downloads", "*", "far.txt")); len(kept) != 1 {
		t.Errorf("the file, in the copy over there: %v", kept)
	}
	if kept, _ := filepath.Glob(filepath.Join(downloads, "*", "far.txt")); len(kept) != 1 || hits["/data/far.txt"] != 1 {
		t.Errorf("the file, in the project here: %v, fetched %d times", kept, hits["/data/far.txt"])
	}
	if left, _ := filepath.Glob(filepath.Join(copyOf, "errand-cache", "downloads", ".errand-work", "wanted", "*")); len(left) != 0 {
		t.Errorf("a request left over there: %v", left)
	}
	// nobody follows: the request waits, the run with it; somebody comes, and it is answered
	o = p.errand("", "run", "--batch", "-q", "--env", "box", "sh", "-c", `cat "$("$ERRAND" download `+server.URL+`/data/later.txt)"`)
	id := strings.Fields(o.out + " x")[0]
	time.Sleep(2 * time.Second)
	if waiting, _ := filepath.Glob(filepath.Join(copyOf, "errand-cache", "downloads", ".errand-work", "wanted", "*")); len(waiting) != 1 || hits["/data/later.txt"] != 0 {
		t.Fatalf("with nobody following, the request waits: %v, fetched %d times", waiting, hits["/data/later.txt"])
	}
	if o = p.errand("", "attach", id); o.code != 0 || o.out != "contents of /data/later.txt\n" {
		t.Errorf("answered once somebody came: exit %d, %q %q", o.code, o.out, o.err)
	}
	// no way from anywhere: said, with both
	was := p.env
	p.env = append(append([]string{}, was...), "ERRAND_NO_INTERNET=1")
	o = p.errand("", "run", "--env", "box", "sh", "-c", `"$ERRAND" download `+server.URL+`/data/never.txt`)
	if o.code == 0 || !strings.Contains(o.err, "nor from the machine that follows the run") {
		t.Errorf("no way from anywhere: exit %d, %q", o.code, o.err)
	}
	p.env = was
}

func TestAgentsGoWhenNothingNeedsThem(t *testing.T) {
	p := newPlace(t)
	p.env = append(append(p.env, python(t)...), "ERRAND_KEEP_ALIVE=1")
	p.apptainer()
	p.write("errand-envs.py", `import errand
errand.envs[ "c" ] = errand.Env( [ errand.Apptainer( image = "images/a.sif" ) ] )
`)
	p.write("images/a.sif", "an image\n")
	agents := func() int {
		out, _ := exec.Command("pgrep", "-f", "place --dir "+p.cache()).Output()
		return len(strings.Fields(string(out)))
	}
	// a run in a container, longer than the agents' keep: both agents stay for it -- the container's, which holds
	// it, and the machine's, which holds its place on the machine
	o := p.errand("", "run", "--batch", "-q", "--env", "c", "sh", "-c", "sleep 4; echo done")
	id := strings.Fields(o.out + " x")[0]
	time.Sleep(3 * time.Second)
	if n := agents(); n != 2 {
		t.Fatalf("%d agents live while a run goes on in the container, past their keep", n)
	}
	// the run over and looked at, nothing needs them: they go at their keep, the one under the machine's with it
	if o = p.errand("", "attach", id); o.code != 0 || o.out != "done\n" {
		t.Fatalf("exit %d, %q %q", o.code, o.out, o.err)
	}
	for i := 0; i < 60 && agents() != 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if n := agents(); n != 0 {
		t.Errorf("%d agents still live with nothing to do, past their keep", n)
	}
	// and the next command finds everything again
	if o = p.errand("", "run", "-q", "--env", "c", "sh", "-c", "echo back in $IN_CONTAINER"); o.out != "back in a.sif\n" {
		t.Errorf("after they went: %q %q", o.out, o.err)
	}
}

// slurm: a batch system of a moment, in the tools -- `sbatch` keeps a job waiting a little, then starts its line with
// its number; `squeue` says how it is; `scancel` ends it ( `-9`: without a word ). What was asked of it is written down.
func (p *scene) slurm() string {
	tools := p.tools()
	dir := filepath.Join(filepath.Dir(p.root), "slurm")
	_ = os.MkdirAll(dir, 0o755)
	write := func(name, text string) {
		_ = os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\ndir="+dir+"\n"+text), 0o755)
	}
	write("sbatch", `n=$(( $(cat $dir/last 2>/dev/null || echo 100) + 1 )); echo $n > $dir/last
echo "$*" >> $dir/submitted
out=/dev/null; line=""
while [ $# -gt 0 ]; do case "$1" in --wrap) line="$2"; shift 2;; --output) out="$2"; shift 2;; --chdir) cd "$2"; shift 2;; *) shift;; esac; done
echo PENDING > $dir/$n.state
( sleep ${FAKE_SLURM_WAIT:-0.6}; [ -f $dir/$n.state ] || exit 0; echo RUNNING > $dir/$n.state
  SLURM_JOB_ID=$n sh -c "$line" > "$out" 2>&1 & echo $! > $dir/$n.pid; wait; rm -f $dir/$n.state $dir/$n.pid ) > /dev/null 2>&1 < /dev/null &
echo $n
`)
	write("squeue", `[ -f $dir/down ] && { echo "slurm_load_jobs error: Unable to contact slurm controller" >&2; exit 1; }
for f in $dir/*.state; do [ -f "$f" ] || continue; n=$(basename $f .state); state=$(cat $f)
  [ $state = PENDING ] && echo "$n|PENDING|Priority||10:00" || echo "$n|$state|None|node7|9:00"; done
exit 0
`)
	write("scancel", `sig=TERM
while :; do case "$1" in -9) sig=KILL; shift;; --signal=*) sig="${1#--signal=}"; shift;; --batch) shift;; *) break;; esac; done
[ -f $dir/$1.pid ] && kill -$sig $(cat $dir/$1.pid) 2>/dev/null
[ $sig = USR1 ] && exit 0
echo "$1" >> $dir/cancelled
[ -f $dir/$1.pid ] || rm -f $dir/$1.state
exit 0
`)
	return dir
}

func TestHandedToABatchSystem(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	slurm := p.slurm()
	p.write("errand-envs.py", `import errand
errand.envs[ "hpc" ] = errand.Env( [ errand.Slurm( partition = "short", time = "00:10:00", keep_alive = 0 ), errand.Vars( { "IN": "the allocation" } ) ] )
errand.envs[ "far" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Slurm() ] )
`)
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("long.sh", "#!/bin/sh\necho started on job $SLURM_JOB_ID; while [ ! -e "+gate+" ]; do sleep 0.1; done; echo finished; exit 5\n")
	submitted := func() string { return read(t, filepath.Join(slurm, "submitted")) }
	agents := func() []string {
		out, _ := exec.Command("pgrep", "-f", "place --dir "+p.cache()).Output()
		return strings.Fields(string(out))
	}

	// a run is handed to the batch system, waited for while it queues, followed from what it writes, and recorded
	o := p.errand("", "run", "--env", "hpc", "sh", "-c", "echo job $SLURM_JOB_ID in $IN; echo to-err >&2")
	if o.code != 0 || o.out != "job 101 in the allocation\n" || !strings.Contains(o.err, "to-err") || !strings.Contains(o.err, "waiting for slurm job 101: pending ( Priority )") {
		t.Fatalf("a run through the batch system: exit %d, %q %q", o.code, o.out, o.err)
	}
	if !o.hasResult || o.result.Status != "PASS" || o.result.Env != "hpc" {
		t.Errorf("its record: %+v", o.result)
	}
	if said := submitted(); !strings.Contains(said, "--partition short") || !strings.Contains(said, "--time 00:10:00") || !strings.Contains(said, "--job-name errand-") {
		t.Errorf("what the batch system was asked: %q", said)
	}
	// what the work asks for is handed over, not queued for here; the time asked for a command wins
	o = p.errand("", "run", "-q", "--env", "hpc", "--cpus", "4", "--ram", "8G", "-x", "--time", "5", "sh", "-c", "exit 3")
	last := strings.Split(strings.TrimSpace(submitted()), "\n")
	if said := last[len(last)-1]; o.code != 3 || !strings.Contains(said, "--cpus-per-task 4") || !strings.Contains(said, "--mem 8192M") ||
		!strings.Contains(said, "--exclusive") || !strings.Contains(said, "--time 5 ") || o.result.Status != "FAIL" {
		t.Errorf("what the work asks for: exit %d, %q", o.code, said)
	}
	if o = p.errand("", "queue"); !strings.Contains(o.out, "nothing held") {
		t.Errorf("errand queued on top of the batch system: %q", o.out)
	}

	// launched and left: it waits, then runs; the agent that submitted it holds nothing of it -- gone, and another
	// started, the run is known again from its record and followed from its files
	o = p.errand("", "run", "--batch", "-q", "--env", "hpc", "long.sh")
	id := strings.Fields(o.out + " x")[0]
	if o = p.errand("", "status"); !strings.Contains(o.out, id[:8]) || !strings.Contains(o.out, "waiting: slurm job 103: ") {
		t.Errorf("status while it queues: %q %q", o.out, o.err)
	}
	for i := 0; i < 50 && !strings.Contains(o.out, "running  ( on node7 )"); i++ {
		time.Sleep(100 * time.Millisecond)
		o = p.errand("", "status")
	}
	if !strings.Contains(o.out, "running  ( on node7 )") {
		t.Fatalf("status once it runs: %q", o.out)
	}
	for _, pid := range agents() {
		_ = exec.Command("kill", "-9", pid).Run()
	}
	time.Sleep(300 * time.Millisecond)
	if o = p.errand("", "status"); !strings.Contains(o.out, "running") {
		t.Fatalf("the run went with the agent that submitted it: %q %q", o.out, o.err)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach", id); o.code != 5 || o.out != "started on job 103\nfinished\n" || !strings.Contains(o.err, "│ FAIL long.sh") {
		t.Errorf("followed from its files, by another agent: exit %d, %q %q", o.code, o.out, o.err)
	}

	// the batch system does not answer for a while: that says nothing of a run, which goes on and is followed to its end
	_ = os.Remove(gate)
	o = p.errand("", "run", "--batch", "-q", "--env", "hpc", "long.sh")
	id = strings.Fields(o.out + " x")[0]
	for i := 0; i < 50 && !strings.Contains(p.errand("", "status").out, "running  ("); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.WriteFile(filepath.Join(slurm, "down"), nil, 0o644)
	time.Sleep(7 * time.Second)
	if o = p.errand("", "status"); !strings.Contains(o.out, "running") {
		t.Errorf("a run said over because the batch system could not be asked: %q", o.out)
	}
	_ = os.Remove(filepath.Join(slurm, "down"))
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach", id); o.code != 5 || !strings.Contains(o.out, "finished") {
		t.Errorf("followed to its end after the batch system came back: exit %d, %q %q", o.code, o.out, o.err)
	}

	// stopped from here: the batch system is told, the run says how it ended
	_ = os.Remove(gate)
	o = p.errand("", "run", "--batch", "-q", "--env", "hpc", "long.sh")
	id = strings.Fields(o.out + " x")[0]
	for i := 0; i < 50 && !strings.Contains(p.errand("", "status").out, "running  ("); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if o = p.errand("", "stop", id[:8]); o.code != 0 || !strings.Contains(o.out, "stopped") {
		t.Errorf("stop: exit %d, %q %q", o.code, o.out, o.err)
	}
	if _, r, ok := p.latest(); !ok || r.Status != "FAIL" || r.Name != "long.sh" {
		t.Errorf("the record of a run that was stopped: %+v", r)
	}
	// the allocation taken back without a word ( a node that goes ): said for the run, with what the batch system says
	o = p.errand("", "run", "--batch", "-q", "--env", "hpc", "long.sh")
	id = strings.Fields(o.out + " x")[0]
	for i := 0; i < 50 && !strings.Contains(p.errand("", "status").out, "running  ("); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = exec.Command(filepath.Join(filepath.Dir(p.root), "tools", "scancel"), "-9", "106").Run()
	if o = p.errand("", "attach", id); o.code == 0 || !strings.Contains(o.out, "started on job 106") {
		t.Errorf("an allocation that ended without a word: exit %d, %q %q", o.code, o.out, o.err)
	}
	if _, r, ok := p.latest(); !ok || r.Status != "FAIL" || !strings.Contains(*r.Error, "the allocation ended before the run said how it ended ( slurm job 106") {
		t.Errorf("its record: %+v ( %v )", r, r.Error)
	}
	// the same from another machine: the project pushed, submitted there, followed, its files brought back
	if o = p.errand("", "run", "-q", "--env", "far", "sh", "-c", "echo there, job $SLURM_JOB_ID"); o.code != 0 || !strings.HasPrefix(o.out, "there, job 10") {
		t.Errorf("through a login node elsewhere: exit %d, %q %q", o.code, o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(o.leaf, "result.json")); err != nil {
		t.Errorf("its record did not come back")
	}
}

func TestAnAllocationIsKeptAndShared(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	slurm := p.slurm()
	p.write("errand-envs.py", `import errand
errand.envs[ "kept" ] = errand.Env( [ errand.Slurm( cpus = 4, keep_alive = "3s" ) ] )
errand.envs[ "also" ] = errand.Env( [ errand.Slurm( cpus = 4, keep_alive = "3s" ), errand.Vars( { "MORE": "yes" } ) ] )
`)
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("long.sh", "#!/bin/sh\necho $1 started on job $SLURM_JOB_ID; while [ ! -e "+gate+"$1 ]; do sleep 0.1; done; echo $1 finished\n")
	submissions := func() int { return strings.Count(read(t, filepath.Join(slurm, "submitted")), "\n") }
	state := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(slurm, n+".state"))
		return strings.TrimSpace(string(b))
	}

	// the first command asks for an allocation; the one that follows runs in it at once: no queue, no new job --
	// and so does a command of another environment that goes through the same Slurm
	o := p.errand("", "run", "-q", "--env", "kept", "sh", "-c", "echo first in $SLURM_JOB_ID")
	if o.code != 0 || o.out != "first in 101\n" {
		t.Fatalf("the first run: exit %d, %q %q", o.code, o.out, o.err)
	}
	o = p.errand("", "run", "-q", "--env", "kept", "sh", "-c", "echo second in $SLURM_JOB_ID")
	if took := o.finishedAt.Sub(o.startedAt); o.out != "second in 101\n" || submissions() != 1 || strings.Contains(o.err, "pending") || took > 2*time.Second {
		t.Fatalf("the second run: %q %q, %d submissions, %v", o.out, o.err, submissions(), took)
	}
	if o = p.errand("", "run", "-q", "--env", "also", "sh", "-c", "echo third in $SLURM_JOB_ID, $MORE"); o.out != "third in 101, yes\n" || submissions() != 1 {
		t.Errorf("another environment through the same Slurm: %q %q, %d submissions", o.out, o.err, submissions())
	}
	// inside it, runs that say what they need run side by side; one that says nothing takes it whole, and waits
	// ( one core each: the node is this machine, which may have three )
	a := strings.Fields(p.errand("", "run", "--batch", "-q", "--cpus", "1", "--env", "kept", "long.sh", "a").out + " x")[0]
	b := strings.Fields(p.errand("", "run", "--batch", "-q", "--cpus", "1", "--env", "kept", "long.sh", "b").out + " x")[0]
	whole := strings.Fields(p.errand("", "run", "--batch", "-q", "--env", "kept", "sh", "-c", "echo alone").out + " x")[0]
	said := ""
	for i := 0; i < 60; i++ {
		said = p.errand("", "status").out
		if strings.Count(said, "running  ( on node7 )") == 2 && strings.Contains(said, "waiting: the machine to itself") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.Count(said, "running  ( on node7 )") != 2 || !strings.Contains(said, whole[:8]) || !strings.Contains(said, "waiting: the machine to itself") || submissions() != 1 {
		t.Fatalf("two that say what they need, one that says nothing: %d submissions\n%s", submissions(), said)
	}
	// one of them stopped: the other goes on, in the same allocation
	if o = p.errand("", "stop", a[:8]); o.code != 0 || !strings.Contains(o.out, "stopped") || state("101") != "RUNNING" {
		t.Errorf("one run stopped in a shared allocation: exit %d, %q, the job %q", o.code, o.out, state("101"))
	}
	_ = os.WriteFile(gate+"b", nil, 0o644)
	if o = p.errand("", "attach", b); o.code != 0 || o.out != "b started on job 101\nb finished\n" {
		t.Errorf("the other went on: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "attach", whole); o.code != 0 || o.out != "alone\n" {
		t.Errorf("the one that took it whole, once the others were through: exit %d, %q %q", o.code, o.out, o.err)
	}
	// what does not fit in it gets an allocation of its own
	if o = p.errand("", "run", "-q", "--cpus", "8", "--env", "kept", "sh", "-c", "echo big in $SLURM_JOB_ID"); o.out != "big in 102\n" || submissions() != 2 {
		t.Errorf("a run that does not fit: %q %q, %d submissions", o.out, o.err, submissions())
	}
	// nothing uses it for its keep: its agent goes, and the allocation with it
	for i := 0; i < 80 && (state("101") != "" || state("102") != ""); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if state("101") != "" || state("102") != "" {
		t.Errorf("still held past their keep: %q %q", state("101"), state("102"))
	}
	if left, _ := filepath.Glob(filepath.Join(p.cache(), "alloc", "*", "alloc.json")); len(left) != 0 {
		t.Errorf("left of allocations that went: %v", left)
	}
	if o = p.errand("", "run", "-q", "--env", "kept", "sh", "-c", "echo again in $SLURM_JOB_ID"); o.out != "again in 103\n" {
		t.Errorf("after it went: %q %q", o.out, o.err)
	}
	// stopped while its allocation is still queued, with nobody else waiting for it: the allocation is given back
	slow := append(append([]string{}, p.env...), "FAKE_SLURM_WAIT=30")
	for i := 0; i < 80 && state("103") != ""; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	cmd := p.command("run", "--batch", "-q", "--env", "kept", "sh", "-c", "echo never")
	cmd.Env = slow
	out, _ := cmd.Output()
	id := strings.Fields(string(out) + " x")[0]
	if o = p.errand("", "stop", id[:8]); o.code != 0 || !strings.Contains(read(t, filepath.Join(slurm, "cancelled")), "104") {
		t.Errorf("stopped while queued: exit %d, %q %q", o.code, o.out, o.err)
	}
}

// launchers: an `mpiexec`, an `mpirun` and an `srun` of a moment, in the tools -- each starts what it is given as
// many times as it is asked, saying which rank each is, and writes down what it was asked
func (p *scene) launchers() string {
	tools := p.tools()
	asked := filepath.Join(filepath.Dir(p.root), "launched")
	body := `echo "$(basename "$0") $*" >> ` + asked + `
n=1
while :; do case "$1" in -n|--ntasks) n="$2"; shift 2;; --nodes|--cpus-per-task|--gpus-per-task|--mem-per-cpu) shift 2;; -*) shift;; *) break;; esac; done
i=0; pids=""
while [ $i -lt $n ]; do PMI_RANK=$i "$@" & pids="$pids $!"; i=$((i+1)); done
code=0; for pid in $pids; do wait $pid || code=1; done; exit $code
`
	for _, name := range []string{"mpiexec", "mpirun", "srun"} {
		_ = os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body), 0o755)
	}
	return asked
}

func sortedLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := range lines {
		for j := i + 1; j < len(lines); j++ {
			if lines[j] < lines[i] {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
	}
	return strings.Join(lines, " ; ")
}

func TestSeveralRanks(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ 'local' ] = errand.Env( [ ] )
errand.envs[ "named" ] = errand.Env( [ errand.Mpi( "mpirun", flags = [ "--oversubscribe" ] ) ] )
errand.envs[ "hpc" ] = errand.Env( [ errand.Slurm( nodes = 2, ntasks_per_node = 2, keep_alive = 0 ), errand.Vars( { "IN": "alloc" } ) ] )
errand.envs[ "out" ] = errand.Env( [ errand.Mpi( "mpirun" ), errand.Apptainer( image = "images/a.sif" ) ] )
errand.envs[ "in" ] = errand.Env( [ errand.Apptainer( image = "images/a.sif" ), errand.Mpi( "mpirun" ) ] )
`)
	p.write("images/a.sif", "an image\n")
	// with nothing to start several processes with, the run is skipped, and says what to install
	bare := p.command("run", "--ranks", "2", "true")
	bare.Env = append(append([]string{}, p.env...), "PATH=/usr/bin:/bin")
	out, err := bare.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "this place has no launcher") || !strings.Contains(string(out), "SKIP") {
		t.Fatalf("no launcher: %v, %q", err, out)
	}
	slurm := p.slurm()
	entered := p.apptainer()
	asked := p.launchers()
	launched := func() string { return read(t, asked) }

	// the launcher of the place: N processes of one program, one result
	o := p.errand("", "run", "-q", "--ranks", "3", "sh", "-c", "echo rank $PMI_RANK")
	if o.code != 0 || sortedLines(o.out) != "rank 0 ; rank 1 ; rank 2" || !strings.Contains(launched(), "mpiexec -n 3 sh -c") {
		t.Fatalf("three ranks here: exit %d, %q %q\n%s", o.code, o.out, o.err, launched())
	}
	if o = p.errand("", "run", "-q", "--ranks", "2", "sh", "-c", "exit $PMI_RANK"); o.code == 0 || o.result.Status != "FAIL" {
		t.Errorf("a rank that fails: exit %d, %+v", o.code, o.result.Status)
	}
	// what is asked for is what one rank needs: the run takes that many times as much of the machine
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("wait.sh", "#!/bin/sh\nwhile [ ! -e "+gate+" ]; do sleep 0.1; done\n")
	id := strings.Fields(p.errand("", "run", "--batch", "-q", "--ranks", "3", "--cpus", "2", "wait.sh").out + " x")[0]
	held := ""
	for i := 0; i < 50 && !strings.Contains(held, "wait.sh"); i++ {
		time.Sleep(100 * time.Millisecond)
		held = p.errand("", "queue").out
	}
	if !strings.Contains(held, "cpus=6") {
		t.Errorf("three ranks of two cores each, in the machine's queue:\n%s", held)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach", id); o.code != 0 {
		t.Errorf("exit %d, %q", o.code, o.err)
	}
	// an Mpi layer names another: its launcher wins
	if o = p.errand("", "run", "-q", "--env", "named", "--ranks", "2", "sh", "-c", "echo r$PMI_RANK"); sortedLines(o.out) != "r0 ; r1" || !strings.Contains(launched(), "mpirun -n 2 --oversubscribe sh -c") {
		t.Errorf("an Mpi layer: %q %q\n%s", o.out, o.err, launched())
	}
	// under Slurm: one task per rank asked of the batch system, and the ranks started by srun in the allocation
	o = p.errand("", "run", "-q", "--env", "hpc", "--ranks", "4", "--cpus", "2", "sh", "-c", "echo $IN r$PMI_RANK")
	said := read(t, filepath.Join(slurm, "submitted"))
	if o.code != 0 || sortedLines(o.out) != "alloc r0 ; alloc r1 ; alloc r2 ; alloc r3" || !strings.Contains(said, "--ntasks 4") ||
		!strings.Contains(said, "--ntasks-per-node 2") || !strings.Contains(said, "--cpus-per-task 2") || !strings.Contains(launched(), "srun --overlap --mpi=pm") || !strings.Contains(launched(), "--ntasks 4 sh -c") {
		t.Errorf("ranks in an allocation: exit %d, %q %q\n%s\n%s", o.code, o.out, o.err, said, launched())
	}
	// a launcher named before a container starts the ranks outside it, each entering the image; after it, inside
	entries := func() int { b, _ := os.ReadFile(entered); return strings.Count(string(b), "\n") }
	was := entries()
	if o = p.errand("", "run", "-q", "--env", "out", "--ranks", "2", "sh", "-c", "echo $IN_CONTAINER r$PMI_RANK"); sortedLines(o.out) != "a.sif r0 ; a.sif r1" ||
		entries() != was+2 {
		t.Errorf("ranks started outside a container: %q %q, entered %d times", o.out, o.err, entries()-was)
	}
	was = entries()
	if o = p.errand("", "run", "-q", "--env", "in", "--ranks", "2", "sh", "-c", "echo $IN_CONTAINER r$PMI_RANK"); sortedLines(o.out) != "a.sif r0 ; a.sif r1" ||
		entries() != was+1 {
		t.Errorf("ranks started inside a container: %q %q, entered %d times", o.out, o.err, entries()-was)
	}
}

// ── a terminal for the runs ( ADR 0005, 0016 ) ────────────────────────────────

// typed: errand typed on a terminal of a moment -- the terminal's master is the test's: it reads what is written to
// it, and types on it
type typed struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	mu     sync.Mutex
	out    bytes.Buffer
	gone   chan struct{}
}

// onTerminal: `errand ARGS` typed in a window of this size, with the surroundings of a terminal that shows colours
// ( unless `env` says otherwise )
func (p *scene) onTerminal(cols, rows int, env []string, args ...string) *typed {
	p.t.Helper()
	master, slave, err := draw.OpenPty()
	if err != nil {
		p.t.Fatal(err)
	}
	draw.SetWinsize(master, cols, rows)
	cmd := p.command(args...)
	cmd.Env = nil
	for _, kv := range p.env {
		if !strings.HasPrefix(kv, "NO_COLOR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	slave.Close()
	k := &typed{t: p.t, cmd: cmd, master: master, gone: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				k.mu.Lock()
				k.out.Write(buf[:n])
				k.mu.Unlock()
			}
			if err != nil {
				close(k.gone)
				return
			}
		}
	}()
	p.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		master.Close()
	})
	return k
}

// said: what was written to the terminal, its colours taken out
func (k *typed) said() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return draw.Sgr.ReplaceAllString(k.out.String(), "")
}

// waitFor: what was written to the terminal says `text`, within a while
func (k *typed) waitFor(text string) {
	k.t.Helper()
	for until := time.Now().Add(30 * time.Second); time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(k.said(), text) {
			return
		}
	}
	k.t.Fatalf("%q never appeared on the terminal; it shows %q", text, k.said())
}

// raw: what was written to the terminal, colours and all
func (k *typed) raw() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.out.String()
}

func (k *typed) typeIn(text string) { _, _ = k.master.WriteString(text) }

func (k *typed) resize(cols, rows int) { draw.SetWinsize(k.master, cols, rows) }

// finish: it ends -> its exit code, and everything it wrote
func (k *typed) finish() (int, string) {
	k.t.Helper()
	done := make(chan error, 1)
	go func() { done <- k.cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		_ = k.cmd.Process.Kill()
		k.t.Fatalf("it did not end; the terminal shows %q", k.said())
	}
	select { // ( what it wrote last is read )
	case <-k.gone:
	case <-time.After(2 * time.Second):
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), k.said()
	}
	return 0, k.said()
}

// onScreen: what a terminal shows of what was written to it, once everything transient is gone -- a line is made again
// by a carriage return and an erase, a cursor put back where it was saved; colours and other sequences take no room
func onScreen(raw string) string {
	var lines []string
	var cur []rune
	col, saved := 0, 0
	put := func(r rune) {
		if col < len(cur) {
			cur[col] = r
		} else {
			cur = append(cur, r)
		}
		col++
	}
	rs := []rune(raw)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\r':
			col = 0
		case '\n':
			lines, cur, col = append(lines, strings.TrimRight(string(cur), " ")), nil, 0
		case 0x1b:
			if i+1 < len(rs) && rs[i+1] == '7' {
				saved, i = col, i+1
			} else if i+1 < len(rs) && rs[i+1] == '8' {
				col, i = saved, i+1
			} else if i+1 < len(rs) && rs[i+1] == '[' {
				j := i + 2
				for j < len(rs) && (rs[j] >= '0' && rs[j] <= '9' || rs[j] == ';') {
					j++
				}
				if j < len(rs) && rs[j] == 'K' && col < len(cur) {
					cur = cur[:col]
				}
				i = j
			}
		default:
			put(r)
		}
	}
	return strings.Join(append(lines, string(cur)), "\n")
}

// theTerminalOf: what a run sees of its terminal, and what it is told -- the same wherever it runs
func (p *scene) theTerminalOf(extra ...string) {
	t := p.t
	t.Helper()
	// a terminal, for the three streams
	k := p.onTerminal(100, 30, nil, append(append([]string{"run"}, extra...), "sh", "-c", `[ -t 0 ] && [ -t 1 ] && [ -t 2 ] && echo "terminal on 0 1 2" || echo "no terminal"; stty size 2>/dev/null; echo "term $TERM"`)...)
	code, out := k.finish()
	if code != 0 || !strings.Contains(out, "│ terminal on 0 1 2") || !strings.Contains(out, "│ 30 94") || !strings.Contains(out, "│ term xterm-256color") {
		t.Errorf("a run on a terminal: exit %d\n%q", code, out)
	}
	// the window changes while it runs: it is told, and sees its new size
	k = p.onTerminal(100, 30, nil, append(append([]string{"run"}, extra...), "sh", "-c",
		`trap 'stty size 2>/dev/null' WINCH; echo re""ady; i=0; while [ $i -lt 300 ]; do sleep 0.05; i=$((i+1)); done`)...)
	k.waitFor("│ ready")
	k.resize(120, 40)
	k.waitFor("│ 40 114")
	k.typeIn("\x03") // Ctrl-C: stops it
	if code, out := k.finish(); code != 130 {
		t.Errorf("Ctrl-C on a terminal: exit %d\n%q", code, out)
	}
	// what is typed reaches it, and is echoed by its terminal
	k = p.onTerminal(100, 30, nil, append(append([]string{"run"}, extra...), "sh", "-c", `printf "name"'? '; read n; echo "hello $n"`)...)
	k.waitFor("│ name? ")
	k.waitFor("waits for input?") // a line left open, and silence: a hint after it
	k.typeIn("Hugo\r")
	code, out = k.finish()
	if code != 0 || !strings.Contains(onScreen(out), "name? Hugo") || !strings.Contains(out, "│ hello Hugo") {
		t.Errorf("a line typed to it: exit %d\n%q", code, out)
	}
	// Ctrl-C reaches the work, which says so, and ends as the work decided
	k = p.onTerminal(100, 30, nil, append(append([]string{"run"}, extra...), "sh", "-c", `trap 'echo stopping; exit 9' INT; echo re""ady; while :; do sleep 0.05; done`)...)
	k.waitFor("│ ready")
	k.typeIn("\x03")
	code, out = k.finish()
	if code != 130 || !strings.Contains(out, "stopping") {
		t.Errorf("Ctrl-C: exit %d\n%q", code, out)
	}
}

func TestARunHasTheTerminalOfWhoeverTypedIt(t *testing.T) {
	p := newPlace(t)
	p.theTerminalOf()

	// nothing changes off a terminal: pipes, what was given to read, and the bytes as they were
	o := p.errand("given\n", "run", "-q", "sh", "-c", `[ -t 0 ] && echo in-tty; [ -t 1 ] && echo out-tty; [ -t 2 ] && echo err-tty; echo "FC=$FORCE_COLOR"; cat; echo done`)
	if o.code != 0 || o.out != "FC=\ngiven\ndone\n" || o.err != "" {
		t.Errorf("off a terminal: exit %d, %q %q", o.code, o.out, o.err)
	}
	// a terminal for what is printed, and what was given to read is still read
	cmd := p.command("run", "-q", "sh", "-c", `[ -t 0 ] && echo in-tty; [ -t 1 ] && echo out-tty; cat`)
	cmd.Stdin = strings.NewReader("piped\n")
	master, slave, _ := draw.OpenPty()
	defer master.Close()
	cmd.Stdout, cmd.Stderr = slave, slave
	_ = cmd.Start()
	slave.Close()
	var got bytes.Buffer
	go func() { _, _ = io.Copy(&got, master) }()
	_ = cmd.Wait()
	time.Sleep(100 * time.Millisecond)
	if s := onScreen(got.String()); !strings.Contains(s, "out-tty") || strings.Contains(s, "in-tty") || !strings.Contains(s, "piped") {
		t.Errorf("a terminal out, a pipe in: %q", s)
	}

	// --batch: nobody types to a run that is left, and it has no terminal
	k := p.onTerminal(100, 30, nil, "run", "--batch", "-q", "sh", "-c", `[ -t 0 ] && echo in-tty; [ -t 1 ] && echo out-tty; echo no-terminal`)
	_, said := k.finish()
	id := strings.Fields(said + " x")[0]
	if o = p.errand("", "attach", id); !strings.Contains(o.out, "no-terminal") || strings.Contains(o.out, "tty") {
		t.Errorf("a run left: %q %q", o.out, o.err)
	}

	// colours: programs that look at the environment are told, unless it says otherwise
	for _, c := range []struct {
		env  []string
		want string
	}{
		{nil, "FC=1 CF=1"},
		{[]string{"NO_COLOR=1"}, "FC= CF="},
		{[]string{"FORCE_COLOR=3"}, "FC=3 CF="},
	} {
		k := p.onTerminal(100, 30, c.env, "run", "-q", "sh", "-c", `echo "FC=$FORCE_COLOR CF=$CLICOLOR_FORCE"`)
		if code, out := k.finish(); code != 0 || !strings.Contains(out, c.want+"\r\n") {
			t.Errorf("colours with %v: %q ( wanted %q )", c.env, out, c.want)
		}
	}
}

func TestARunElsewhereHasATerminalToo(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	p.theTerminalOf("--env", "box")
}

func TestARunInAContainerHasATerminalToo(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.apptainer()
	p.write("errand-envs.py", `import errand
errand.envs[ "c" ] = errand.Env( [ errand.Apptainer( image = "images/a.sif" ) ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Apptainer( image = "images/a.sif" ) ] )
`)
	p.write("images/a.sif", "an image\n")
	p.theTerminalOf("--env", "c")
	p.theTerminalOf("--env", "box")
}

func TestWhatErrandDoesIsWhereItBelongs(t *testing.T) {
	p := newPlace(t)
	// the step a run is at: under its last line while it is quiet, and gone when what it writes next arrives
	k := p.onTerminal(100, 30, nil, "run", "sh", "-c", `n=41; "$ERRAND" say step "round $n" --done 0.41 residual=3e-7; sleep 2; echo between; n=80; "$ERRAND" say step "round $n" --done 0.8; sleep 2; echo end`)
	k.waitFor("round 41")
	k.waitFor("round 80")
	code, out := k.finish()
	if code != 0 {
		t.Fatalf("exit %d\n%q", code, out)
	}
	if !strings.Contains(out, "▕████") || !strings.Contains(out, "41 %") || !strings.Contains(out, "residual 3e-07") {
		t.Errorf("the line under the output: %q", out)
	}
	history := onScreen(out)
	if strings.Contains(history, "round 41") || strings.Contains(history, "round 80") || !strings.Contains(history, "│ between") || !strings.Contains(history, "│ end") {
		t.Errorf("the history: %q", history)
	}
	if !strings.Contains(out, "┆") || strings.Contains(history, "┆") || strings.Contains(out, "\x1b[1;") {
		t.Errorf("what is transient is not under the output, or not taken away: %q", out)
	}
	// off a terminal: nothing of it
	o := p.errand("", "run", "-q", "sh", "-c", `"$ERRAND" say step "round 41" --done 0.41; echo end`)
	if o.out != "end\n" || o.err != "" {
		t.Errorf("off a terminal: %q %q", o.out, o.err)
	}

	// a run that waits for the machine: said while it waits, and no line of the history
	gate := filepath.Join(filepath.Dir(p.root), "gate")
	p.write("hold.sh", "#!/bin/sh\nwhile [ ! -e "+gate+" ]; do sleep 0.05; done\n")
	held := func() { // the machine is held by it
		for i := 0; i < 100 && !strings.Contains(p.errand("", "queue").out, "hold.sh"); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	p.errand("", "run", "--batch", "-q", "-x", "hold.sh")
	held()
	k = p.onTerminal(100, 30, nil, "run", "-x", "echo", "me")
	k.waitFor("waiting for")
	_ = os.WriteFile(gate, nil, 0o644)
	code, out = k.finish()
	if code != 0 || strings.Contains(onScreen(out), "waiting for") || !strings.Contains(onScreen(out), "│ me") {
		t.Errorf("a run that waits: exit %d\n%q", code, out)
	}
	// ... and the same, off a terminal, is a line as it was
	_ = os.Remove(gate)
	p.errand("", "run", "--batch", "-q", "-x", "hold.sh")
	held()
	cmd := p.command("run", "-x", "echo", "me")
	var errs bytes.Buffer
	cmd.Stderr = &errs
	_ = cmd.Start()
	for i := 0; i < 100 && !strings.Contains(errs.String(), "waiting for"); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	_ = cmd.Wait()
	if !strings.Contains(errs.String(), "  waiting for ") {
		t.Errorf("off a terminal, a wait is a line: %q ", errs.String())
	}

	// the same for a run followed again
	o = p.errand("", "run", "--batch", "-q", "sh", "-c", `m=going; "$ERRAND" say step "still $m" --done 0.5; sleep 3`)
	id := strings.Fields(o.out + " x")[0]
	k = p.onTerminal(100, 30, nil, "attach", id)
	k.waitFor("still going")
	if code, out := k.finish(); code != 0 || strings.Contains(onScreen(out), "still going") || !strings.Contains(out, "50 %") {
		t.Errorf("attach: exit %d\n%q", code, out)
	}
}

func TestARunHandedToABatchSystemStaysOnPipes(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.slurm()
	p.write("errand-envs.py", `import errand
errand.envs[ "hpc" ] = errand.Env( [ errand.Slurm( partition = "short", time = "00:10:00", keep_alive = 0 ) ] )
`)
	// nothing links the login node to its node: no terminal, and it is said, on a terminal and off one
	k := p.onTerminal(100, 30, nil, "run", "--env", "hpc", "sh", "-c", `[ -t 1 ] && echo "a terminal" || echo "pipes"; n=two; "$ERRAND" say step "phase $n"; sleep 2`)
	code, out := k.finish()
	if code != 0 || !strings.Contains(out, "│ pipes") || strings.Contains(out, "│ a terminal") || !strings.Contains(out, "this run has no terminal") {
		t.Errorf("handed to a batch system, on a terminal: exit %d\n%q", code, out)
	}
	if !strings.Contains(out, "phase two") || strings.Contains(onScreen(out), "phase two") { // ( shown, and gone )
		t.Errorf("its step: %q", out)
	}
	o := p.errand("", "run", "-q", "--env", "hpc", "sh", "-c", `[ -t 1 ] && echo "a terminal" || echo "pipes"`)
	if o.code != 0 || o.out != "pipes\n" || strings.Contains(o.err, "no terminal") {
		t.Errorf("off a terminal: exit %d, %q %q", o.code, o.out, o.err)
	}
}

func TestAPushAndAPullAreWhereTheyBelong(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	p.write("data.txt", "one\n")
	k := p.onTerminal(100, 30, nil, "run", "--env", "box", "true")
	code, out := k.finish()
	history := onScreen(out)
	// the place line says where it goes, what is done to get there on its right while it is done, its numbers after
	if code != 0 || !strings.Contains(out, "↑ push → box") || !strings.Contains(out, "↓ pull ← ") {
		t.Errorf("a push and a pull under way: exit %d\n%q", code, out)
	}
	if !regexp.MustCompile(`(?m)^▸ box +↑ \d+ files? · `).MatchString(history) || strings.Contains(history, "push →") || strings.Contains(history, "pull ←") {
		t.Errorf("the place line, with its numbers, and nothing of what is transient, in the history:\n%s", history)
	}
}

// builds: what the stand-in engines were asked to build, a line each
func (p *scene) builds() string { return filepath.Join(filepath.Dir(p.root), "builds") }

// engine: a `docker` ( or `podman` ) of a moment, in the tools -- it keeps its images as files, enters nothing, and
// says what it was asked: what runs "inside" is told which image it is in, and has what the engine was told to give it
// and nothing of the surroundings of whoever started it, as a container has. -> the directory it keeps its state in:
// `log` ( every call ), `entered` ( every run ), `images/`, and `down` when it is not to answer
func (p *scene) engine(name string) string {
	dir := p.tools()
	state := filepath.Join(filepath.Dir(p.root), name)
	_ = os.MkdirAll(filepath.Join(state, "images"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, name), []byte(`#!/bin/sh
state=`+state+`; engine=`+name+`
echo "$engine $*" >> "$state/log"
img() { echo "$state/images/$(echo "$1" | tr '/:' '__')"; }
case "$1" in
info) [ ! -f "$state/down" ] || { echo "Cannot connect to the `+name+` daemon" >&2; exit 1; }; exit 0;;
image) shift; [ "$1" = inspect ] || exit 2; shift; fmt=""; [ "$1" = -f ] && { fmt="$2"; shift 2; }
    [ -f "$(img "$1")" ] || { echo "Error: No such image: $1" >&2; exit 1; }
    [ -z "$fmt" ] || echo "sha256:$(cksum < "$(img "$1")" | cut -d' ' -f1) $(sed -n 1p "$(img "$1")")"
    exit 0;;
build) shift; echo "build $*" >> `+p.builds()+`
    tag=""; file=""
    while [ $# -gt 0 ]; do case "$1" in -t) tag="$2"; shift 2;; -f) file="$2"; shift 2;; *) last="$1"; shift;; esac; done
    echo "Step 1/1 : building $tag"
    if [ "$last" = - ]; then body="$(cat)"; echo "layer: $body" >> "$(img "$tag")"; exit 0; fi
    body="$(cat "$file")" || exit 1
    case "$body" in *SLOW*) sleep 1;; esac
    case "$body" in *FAIL*) echo "ERROR: the build of $tag failed" >&2; exit 1;; esac
    { date -u +%Y-%m-%dT%H:%M:%SZ; echo "built from: $body"; } > "$(img "$tag")"
    exit 0;;
pull) shift; case "$1" in *nowhere*) echo "Error: pull access denied for $1" >&2; exit 1;; esac
    echo "Pulling $1"; { date -u +%Y-%m-%dT%H:%M:%SZ; echo "pulled"; } > "$(img "$1")"; exit 0;;
run) shift; user=""; host=""; dir=""; vols=""; envs=""; flags=""
    while :; do case "$1" in
    --rm) shift;;
    --user) user="$2"; shift 2;;
    --hostname) host="$2"; shift 2;;
    -w) dir="$2"; shift 2;;
    -v) vols="$vols $2"; shift 2;;
    -e) envs="$envs $2"; shift 2;;
    --gpus|--network|--shm-size) flags="$flags $1=$2"; shift 2;;
    -*) flags="$flags $1"; shift;;
    *) break;;
    esac; done
    image="$1"; shift
    [ -f "$(img "$image")" ] || { echo "Unable to find image $image locally" >&2; exit 125; }
    echo "$engine $image user=$user host=$host dir=$dir flags=$flags vols=$vols" >> "$state/entered"
    cd "$dir" || exit 126
    exec env -i PATH="$PATH" HOME=/nonexistent IN_CONTAINER="$image" $envs "$@";;
*) echo "$engine: $*" >&2; exit 2;;
esac
`), 0o755)
	return state
}

// nLines: how many lines a file holds -- none when it is not there
func nLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func TestAnApptainerImageIsBuilt(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	entered := p.apptainer()
	scratch := filepath.Join(filepath.Dir(p.root), "scratch")
	p.write("errand-envs.py", `import errand
errand.envs[ "c" ] = errand.Env( [ errand.Apptainer( image = "images/a.sif", recipe = "images/a.def", fakeroot = True, scratch = "`+scratch+`", build_flags = [ "--sandbox" ] ), errand.Vars( { "INNER": "set" } ) ] )
errand.envs[ "x" ] = errand.Env( [ errand.Apptainer( image = "images/x.sif", recipe = "images/a.def", exe = "nosuchapptainer" ) ] )
errand.envs[ "u" ] = errand.Env( [ errand.Apptainer( image = "images/u.sif", recipe = "docker://alpine:3" ) ] )
errand.envs[ "i" ] = errand.Env( [ errand.Apptainer( image = "images/i.sif", recipe = "images/a.def", install = "echo {image} >> installed.log", pip = [ "numpy", "scipy" ] ) ] )
errand.envs[ "s" ] = errand.Env( [ errand.Apptainer( image = "images/s.sif", recipe = "images/slow.def" ) ] )
errand.envs[ "v" ] = errand.Env( [ errand.Apptainer( image = "images/v.sif", recipe = "images/veryslow.def" ) ] )
errand.envs[ "b" ] = errand.Env( [ errand.Apptainer( image = "images/b.sif", recipe = "images/a.def", build_on = 5 ) ] )
`)
	recipe := func(body string) { p.write("images/a.def", body) }
	recipe("Bootstrap: docker\nFrom: alpine\n")
	image := filepath.Join(p.root, "images", "a.sif")
	records := filepath.Join(p.root, "errand-outputs", "builds", "records")
	builds := func() int { return nLines(p.builds()) }
	state := func(env string) string {
		o := p.errand("", "envs", env)
		return o.out
	}

	// missing: said so by `envs`, then built by the first command that needs it, with what the build says
	if out := state("c"); !strings.Contains(out, "not built") {
		t.Fatalf("before anything: %q", out)
	}
	o := p.errand("", "run", "--env", "c", "sh", "-c", "echo in $IN_CONTAINER $INNER")
	if o.code != 0 || o.out != "in a.sif set\n" {
		t.Fatalf("a run, the image built for it: exit %d, %q %q", o.code, o.out, o.err)
	}
	for _, said := range []string{"bringing up to date apptainer:a.sif", "$ mkdir -p images && mkdir -p " + scratch + " && APPTAINER_TMPDIR=" + scratch,
		"      building images/a.sif.new"} {
		if !strings.Contains(o.err, said) {
			t.Errorf("not said while it was built: %q in\n%s", said, o.err)
		}
	}
	if !strings.Contains(read(t, image), "built from: Bootstrap: docker") || builds() != 1 {
		t.Errorf("the image: %q, built %d times", read(t, image), builds())
	}
	if _, err := os.Stat(image + ".new"); err == nil {
		t.Error("what it was built aside as is left")
	}
	if got := read(t, p.builds()); !strings.Contains(got, "build --force --fakeroot --sandbox images/a.sif.new images/a.def | tmp="+scratch+" cache="+scratch) {
		t.Errorf("what apptainer was asked, and room for what it unpacks and downloads: %q", got)
	}
	if !strings.Contains(read(t, entered), "images/a.sif") {
		t.Errorf("entered: %q", read(t, entered))
	}
	if out := state("c"); !strings.Contains(out, " ok ") {
		t.Errorf("once built: %q", out)
	}
	// nothing changed: nothing is built, nothing is said of it
	if o = p.errand("", "run", "--env", "c", "true"); o.code != 0 || strings.Contains(o.err, "bringing") || builds() != 1 {
		t.Errorf("built again with nothing changed: %q, built %d times", o.err, builds())
	}
	// the recipe changes: stale, and not touched by --no-setup, rebuilt by the next command, the old one kept
	recipe("Bootstrap: docker\nFrom: alpine:3\n")
	if out := state("c"); !strings.Contains(out, "stale") {
		t.Errorf("a changed recipe: %q", out)
	}
	if o = p.errand("", "run", "--no-setup", "--env", "c", "true"); o.code != 0 || builds() != 1 || strings.Contains(read(t, image), "alpine:3") {
		t.Errorf("--no-setup built: exit %d, built %d times, %q", o.code, builds(), read(t, image))
	}
	enteredBefore := nLines(entered)
	if o = p.errand("", "run", "-q", "--env", "c", "true"); o.code != 0 || builds() != 2 || !strings.Contains(read(t, image), "From: alpine:3") {
		t.Errorf("a changed recipe: exit %d, %q, built %d times", o.code, o.err, builds())
	}
	if nLines(entered) != enteredBefore+1 { // the agent that was inside holds the image as it was: this one is another's
		t.Errorf("an image built again is entered again: %d entries before, %d after", enteredBefore, nLines(entered))
	}
	if !strings.Contains(read(t, image+".prev"), "From: alpine\n") {
		t.Errorf("the one it replaced is not kept: %q", read(t, image+".prev"))
	}

	// a build that fails says why and costs nothing: the image that was there is the one that is used, nothing half
	// made is left, and the next command tries again
	recipe("Bootstrap: docker\nFROM: FAIL\n")
	o = p.errand("", "run", "--env", "c", "sh", "-c", "echo not run")
	if o.code != 1 || o.out != "" || !strings.Contains(o.err, "failed while preparing apptainer:a.sif") || !strings.Contains(o.err, "the build of images/a.sif.new failed") ||
		!strings.Contains(o.err, "existing one is untouched") {
		t.Fatalf("a build that fails: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o.result.Status != "FAIL" || !strings.Contains(*o.result.Error, "failed while preparing") {
		t.Errorf("its run's record: %+v", o.result)
	}
	if _, err := os.Stat(image + ".new"); err == nil {
		t.Error("a half-made image left, taken for the real one next time")
	}
	if !strings.Contains(read(t, image), "From: alpine:3") {
		t.Errorf("the image that was there is gone: %q", read(t, image))
	}
	if out := state("c"); !strings.Contains(out, "last build failed") {
		t.Errorf("after a failure: %q", out)
	}
	if logs, _ := filepath.Glob(filepath.Join(records, "apptainer-a.sif-*.log")); len(logs) != 1 || !strings.Contains(read(t, logs[0]), "the build of images/a.sif.new failed") {
		t.Errorf("the output of the last attempt: %v", logs)
	}
	if o = p.errand("", "run", "--no-setup", "--env", "c", "sh", "-c", "echo in $IN_CONTAINER"); o.out != "in a.sif\n" {
		t.Errorf("the image that was there, used as it is: %q %q", o.out, o.err)
	}
	before := builds()
	if o = p.errand("", "run", "-q", "--env", "c", "true"); o.code != 1 || builds() != before+1 {
		t.Errorf("tried again: exit %d, built %d times more", o.code, builds()-before)
	}
	recipe("Bootstrap: docker\nFrom: alpine:4\n")
	if o = p.errand("", "run", "-q", "--env", "c", "true"); o.code != 0 || !strings.Contains(read(t, image), "alpine:4") {
		t.Errorf("once the recipe is right: exit %d, %q", o.code, o.err)
	}

	// what is there and was never seen is adopted, unless it is older than its recipe: an image can be rebuilt without
	// risk, and a wrong adoption has cost a release
	_ = os.RemoveAll(records)
	before = builds()
	if o = p.errand("", "run", "-q", "--env", "c", "true"); o.code != 0 || builds() != before {
		t.Errorf("an image never seen before was rebuilt: %q", o.err)
	}
	if out := state("c"); !strings.Contains(out, "adopted") {
		t.Errorf("after an adoption: %q", out)
	}
	_ = os.RemoveAll(records)
	long := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(image, long, long)
	if out := state("c"); !strings.Contains(out, "stale") {
		t.Errorf("an image older than its recipe, and not known: %q", out)
	}
	if o = p.errand("", "run", "--env", "c", "true"); o.code != 0 || builds() != before+1 || !strings.Contains(o.err, "older than a.def, and errand has no record of it") {
		t.Errorf("older than its recipe, not known: exit %d, %q, built %d times more", o.code, o.err, builds()-before)
	}
	// errand setup: now, and nothing else; --dry-run says what it would run; --force rebuilds
	before = builds()
	if o = p.errand("", "setup", "--dry-run", "--force", "c"); o.code != 0 || !strings.Contains(o.err, "would run: ") || !strings.Contains(o.err, "apptainer build --force --fakeroot") || builds() != before {
		t.Errorf("setup --dry-run: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "setup", "--force", "c"); o.code != 0 || !strings.Contains(o.err, "rebuilding apptainer:a.sif") || builds() != before+1 {
		t.Errorf("setup --force: exit %d, %q", o.code, o.err)
	}

	// what the place must have is said, with where, before anything is built
	o = p.errand("", "run", "--env", "x", "true")
	if o.code != 1 || !strings.Contains(o.err, "nosuchapptainer, not found ( apptainer:x.sif )") || !strings.Contains(o.err, "on ") {
		t.Errorf("a program the place lacks: exit %d, %q", o.code, o.err)
	}
	// a recipe that is an image to convert is itself, and no file
	if o = p.errand("", "run", "-q", "--env", "u", "true"); o.code != 0 || !strings.Contains(read(t, filepath.Join(p.root, "images", "u.sif")), "built from: docker://alpine:3") {
		t.Errorf("built from an image to convert: exit %d, %q", o.code, o.err)
	}
	// pip is installed into a layer made from the image, and `install` is run when it is where it is used
	if o = p.errand("", "run", "-q", "--env", "i", "true"); o.code != 0 || !strings.Contains(read(t, filepath.Join(p.root, "images", "i.sif")), "python -m pip install numpy scipy") ||
		!strings.Contains(read(t, filepath.Join(p.root, "installed.log")), "images/i.sif") {
		t.Errorf("pip and install: exit %d, %q, %q", o.code, o.err, read(t, filepath.Join(p.root, "images", "i.sif")))
	}
	// `build_on` is the name of an environment, or its layers: anything else is refused saying so
	o = p.errand("", "run", "--env", "b", "true")
	if o.code != 1 || !strings.Contains(o.err, "build_on") || !strings.Contains(o.err, "or the layers of one") {
		t.Errorf("build_on: exit %d, %q", o.code, o.err)
	}
	p.write("images/b.sif", "carried by hand\n")
	if o = p.errand("", "run", "-q", "--env", "b", "sh", "-c", "echo in $IN_CONTAINER"); o.out != "in b.sif\n" {
		t.Errorf("an image that is there is used, whatever it is built on: %q %q", o.out, o.err)
	}

	// two commands at once that need the same image: one builds it, the other waits for it
	p.write("images/slow.def", "Bootstrap: docker\nFrom: SLOW\n")
	before = builds()
	first, second := p.command("run", "--env", "s", "true"), p.command("run", "--env", "s", "true")
	var said1, said2 bytes.Buffer
	first.Stderr, second.Stderr = &said1, &said2
	_ = first.Start()
	_ = second.Start()
	err1, err2 := first.Wait(), second.Wait()
	if both := said1.String() + said2.String(); err1 != nil || err2 != nil || builds() != before+1 || strings.Count(both, "bringing up to date") != 1 || !strings.Contains(both, "is being prepared by") {
		t.Errorf("two at once: %v %v, built %d times\n%s\n%s", err1, err2, builds()-before, said1.String(), said2.String())
	}
	if locks, _ := filepath.Glob(filepath.Join(records, "*.lock")); len(locks) != 0 {
		t.Errorf("a lock left: %v", locks)
	}

	// Ctrl-C while it is built: the build stops, nothing half made is left, nobody is kept waiting for it
	p.write("images/veryslow.def", "Bootstrap: docker\nFrom: VERYSLOW\n")
	before = builds()
	run := p.command("run", "-q", "--env", "v", "true")
	_ = run.Start()
	for i := 0; i < 100 && builds() == before; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = run.Process.Signal(os.Interrupt)
	if err := run.Wait(); err == nil || err.(*exec.ExitError).ExitCode() != 130 {
		t.Errorf("Ctrl-C while an image is built: %v", err)
	}
	slow := filepath.Join(p.root, "images", "v.sif")
	for i := 0; i < 50; i++ { // ( the agent finishes what it was told to stop )
		if locks, _ := filepath.Glob(filepath.Join(records, "*.lock")); len(locks) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if locks, _ := filepath.Glob(filepath.Join(records, "*.lock")); len(locks) != 0 {
		t.Errorf("a lock left after Ctrl-C: %v", locks)
	}
	if _, err := os.Stat(slow + ".new"); err == nil {
		t.Error("a half-made image left after Ctrl-C")
	}
	if _, err := os.Stat(slow); err == nil {
		t.Error("an image taken for good after Ctrl-C")
	}
	if out, _ := exec.Command("pgrep", "-f", "sleep 30").Output(); len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("the build still runs after Ctrl-C: %s", out)
	}
	p.write("images/veryslow.def", "Bootstrap: docker\nFrom: fast\n")
	if o = p.errand("", "run", "-q", "--env", "v", "true"); o.code != 0 {
		t.Errorf("after Ctrl-C: exit %d, %q", o.code, o.err)
	}
}

func TestADockerOrPodmanContainer(t *testing.T) {
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			p := newPlace(t)
			p.env = append(p.env, python(t)...)
			p.env = append(p.env, "HOST_ONLY=yes")
			state := p.engine(name)
			class := strings.ToUpper(name[:1]) + name[1:]
			tag := "proj/" + name + ":1"
			p.write("errand-envs.py", `import errand
errand.envs[ "d" ] = errand.Env( [ errand.`+class+`( image = "`+tag+`", recipe = "Dockerfile", flags = [ "--gpus", "all" ], mounts = { "data": "/data" } ), errand.Vars( { "INNER": "set" } ) ] )
errand.envs[ "pulled" ] = errand.Env( [ errand.`+class+`( image = "base:2" ) ] )
errand.envs[ "lost" ] = errand.Env( [ errand.`+class+`( image = "nowhere/x:1" ) ] )
errand.envs[ "layered" ] = errand.Env( [ errand.`+class+`( image = "`+tag+`-pip", recipe = "Dockerfile", pip = [ "numpy" ] ) ] )
errand.envs[ "plain" ] = errand.Env( [ errand.`+class+`( image = "`+tag+`", user = None ) ] )
errand.envs[ "named" ] = errand.Env( [ errand.`+class+`( image = "`+tag+`", user = "7:8" ) ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.`+class+`( image = "`+tag+`-far", recipe = "Dockerfile" ) ] )
`)
			dockerfile := func(body string) { p.write("Dockerfile", body) }
			dockerfile("FROM alpine\n")
			builds := func() int { return nLines(p.builds()) }
			envs := func(env string) string { return p.errand("", "envs", env).out }
			here := system.Hostname()
			here = strings.SplitN(here, ".", 2)[0]

			if out := envs("d"); !strings.Contains(out, "not built") {
				t.Fatalf("before anything: %q", out)
			}
			// the run is made inside, in the project's directory, with what the layers after the image set, and nothing
			// of the surroundings it was typed in: an engine's container has its own
			o := p.errand("", "run", "--env", "d", "sh", "-c", "echo in $IN_CONTAINER $INNER $(pwd) ${HOST_ONLY:-nothing}; echo x > made.txt")
			if o.code != 0 || o.out != "in "+tag+" set "+p.root+" nothing\n" {
				t.Fatalf("a run inside: exit %d, %q %q", o.code, o.out, o.err)
			}
			for _, said := range []string{"bringing up to date " + name + ":" + tag, "$ " + name + " build -t " + tag + " -f Dockerfile ."} {
				if !strings.Contains(o.err, said) {
					t.Errorf("not said while it was built: %q in\n%s", said, o.err)
				}
			}
			if read(t, filepath.Join(p.root, "made.txt")) != "x\n" {
				t.Error("what it wrote is not in the project")
			}
			entered := read(t, filepath.Join(state, "entered"))
			user := ""
			if name == "docker" { // a daemon's container writes as root unless told otherwise; podman, rootless, already is the caller
				user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
			}
			for _, want := range []string{" user=" + user + " host=" + here + " dir=" + p.root, "flags= --gpus=all", " " + p.root + "/data:/data", " " + p.root + ":" + p.root,
				" " + p.cache() + "/place/"} {
				if !strings.Contains(entered, want) {
					t.Errorf("what the engine was told: %q in\n%s", want, entered)
				}
			}
			hash, _ := kit.HashOfFile(built) // ( the copy its agent runs from )
			program := filepath.Join(filepath.Dir(p.root), "cache", "bin", hash)
			if !strings.Contains(entered, program+":"+program) {
				t.Errorf("errand's program is not seen from inside: %q", entered)
			}
			if out := envs("d"); !strings.Contains(out, " ok ") {
				t.Errorf("once built: %q", out)
			}
			// the protocol of what a run says works from inside
			o = p.errand("", "run", "-o", "--env", "d", "sh", "-c", `"$ERRAND" say step "deep inside" --done 0.5; "$ERRAND" say result n=3; echo protocol $ERRAND_PROTOCOL`)
			if o.code != 0 || !strings.Contains(o.out, "protocol 1") || o.result.Results["n"] != 3.0 {
				t.Errorf("what a run says from inside: exit %d, %q %q %v", o.code, o.out, o.err, o.result.Results)
			}
			if !strings.Contains(read(t, filepath.Join(o.leaf, "steps.jsonl")), "deep inside") {
				t.Errorf("its journal: %q", read(t, filepath.Join(o.leaf, "steps.jsonl")))
			}
			// one container's agent for the two runs, and nothing built again
			if n := nLines(filepath.Join(state, "entered")); n != 1 || builds() != 1 {
				t.Errorf("entered %d times, built %d times", n, builds())
			}
			// a changed recipe is rebuilt; one that fails says why, and the image that was there is the one used
			dockerfile("FROM alpine:3\n")
			if out := envs("d"); !strings.Contains(out, "stale") {
				t.Errorf("a changed Dockerfile: %q", out)
			}
			// ...where the engine answers
			_ = os.WriteFile(filepath.Join(state, "down"), nil, 0o644)
			o = p.errand("", "run", "--env", "d", "true")
			if o.code != 1 || !strings.Contains(o.err, "a running "+name+" ( `"+name+" info` fails ) ( "+name+":"+tag+" )") {
				t.Errorf("an engine that does not answer: exit %d, %q", o.code, o.err)
			}
			_ = os.Remove(filepath.Join(state, "down"))
			dockerfile("FROM FAIL\n")
			o = p.errand("", "run", "--env", "d", "true")
			if o.code != 1 || !strings.Contains(o.err, "failed while preparing "+name+":"+tag) || !strings.Contains(o.err, "the build of "+tag+" failed") {
				t.Errorf("a build that fails: exit %d, %q", o.code, o.err)
			}
			if o = p.errand("", "run", "--no-setup", "--env", "d", "sh", "-c", "echo $IN_CONTAINER"); o.out != tag+"\n" {
				t.Errorf("the image that was there, used as it is: %q %q", o.out, o.err)
			}
			if out := envs("d"); !strings.Contains(out, "last build failed") {
				t.Errorf("after a failure: %q", out)
			}
			dockerfile("FROM alpine:4\n")
			before := builds()
			if o = p.errand("", "run", "-q", "--env", "d", "true"); o.code != 0 || builds() != before+1 {
				t.Errorf("rebuilt: exit %d, %q", o.code, o.err)
			}
			// who runs is the layer's to say
			if o = p.errand("", "run", "-q", "--env", "plain", "true"); o.code != 0 || !strings.Contains(read(t, filepath.Join(state, "entered")), " user= host="+here+" dir="+p.root+" flags= vols=") {
				t.Errorf("no user asked: %q", read(t, filepath.Join(state, "entered")))
			}
			if o = p.errand("", "run", "-q", "--env", "named", "true"); o.code != 0 || !strings.Contains(read(t, filepath.Join(state, "entered")), " user=7:8 ") {
				t.Errorf("a user asked: %q", read(t, filepath.Join(state, "entered")))
			}
			// an image that nobody builds here is fetched, once
			o = p.errand("", "run", "--env", "pulled", "sh", "-c", "echo in $IN_CONTAINER")
			if o.code != 0 || o.out != "in base:2\n" || !strings.Contains(o.err, "$ "+name+" pull base:2") {
				t.Errorf("an image to fetch: exit %d, %q %q", o.code, o.out, o.err)
			}
			if o = p.errand("", "run", "-q", "--env", "pulled", "true"); o.code != 0 || strings.Count(read(t, filepath.Join(state, "log")), name+" pull base:2") != 1 {
				t.Errorf("fetched again: %q", o.err)
			}
			if o = p.errand("", "run", "--env", "lost", "true"); o.code != 1 || !strings.Contains(o.err, "failed while preparing "+name+":nowhere/x:1") || !strings.Contains(o.err, "pull access denied") {
				t.Errorf("an image that cannot be fetched: exit %d, %q", o.code, o.err)
			}
			// pip is installed in a layer on top of what was built, under the same tag
			before = builds()
			if o = p.errand("", "run", "-q", "--env", "layered", "true"); o.code != 0 || builds() != before+2 ||
				!strings.Contains(read(t, filepath.Join(state, "images", strings.NewReplacer("/", "_", ":", "_").Replace(tag+"-pip"))), "RUN python -m pip install numpy") {
				t.Errorf("pip: exit %d, %q, built %d times", o.code, o.err, builds()-before)
			}
			// what is there and was never seen is adopted
			_ = os.RemoveAll(filepath.Join(p.root, "errand-outputs", "builds", "records"))
			before = builds()
			if o = p.errand("", "run", "-q", "--env", "d", "true"); o.code != 0 || builds() != before {
				t.Errorf("an image never seen before was rebuilt: %q", o.err)
			}
			if out := envs("d"); !strings.Contains(out, "adopted") {
				t.Errorf("after an adoption: %q", out)
			}
			// setup says what it does, and the states are said
			if o = p.errand("", "setup", "--force", "d"); o.code != 0 || !strings.Contains(o.err, "rebuilding "+name+":"+tag) {
				t.Errorf("setup --force: exit %d, %q", o.code, o.err)
			}
			// on another machine, it is that machine's engine that builds and enters
			o = p.errand("", "run", "--env", "box", "sh", "-c", "echo there in $IN_CONTAINER")
			if o.code != 0 || o.out != "there in "+tag+"-far\n" || !strings.Contains(o.err, "bringing up to date "+name+":"+tag+"-far") {
				t.Errorf("elsewhere: exit %d, %q %q", o.code, o.out, o.err)
			}
		})
	}
}

func TestAnImageIsBuiltWhereItRuns(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	entered := p.apptainer()
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Apptainer( image = "images/b.sif", recipe = "images/b.def" ), errand.Vars( { "INNER": "set" } ) ] )
`)
	p.write("images/b.def", "Bootstrap: docker\nFrom: alpine\n")
	p.write("images/b.sif", "made here, and not to travel\n") // an image is built where it is used: this one is not sent
	copyOf := filepath.Join(p.far, "home", "copy", "proj")
	o := p.errand("", "envs", "box")
	if !strings.Contains(o.out, "box") || !strings.Contains(o.out, "not built") {
		t.Errorf("asked of the machine it lives on: %q", o.out)
	}
	o = p.errand("", "run", "--env", "box", "sh", "-c", "echo there in $IN_CONTAINER $INNER")
	if o.code != 0 || o.out != "there in b.sif set\n" || !strings.Contains(o.err, "bringing up to date apptainer:b.sif") {
		t.Fatalf("built there, entered there: exit %d, %q %q", o.code, o.out, o.err)
	}
	if got := read(t, filepath.Join(copyOf, "images", "b.sif")); !strings.Contains(got, "built from: Bootstrap: docker") {
		t.Errorf("the image over there: %q", got)
	}
	if nLines(p.builds()) != 1 || !strings.Contains(read(t, entered), filepath.Join(copyOf, "images", "b.sif")) {
		t.Errorf("built %d times, entered: %q", nLines(p.builds()), read(t, entered))
	}
	if recs, _ := filepath.Glob(filepath.Join(copyOf, "errand-outputs", "builds", "records", "apptainer-b.sif-*.json")); len(recs) != 1 {
		t.Errorf("its record, over there: %v", recs)
	}
	if o = p.errand("", "run", "-q", "--env", "box", "true"); o.code != 0 || nLines(p.builds()) != 1 {
		t.Errorf("built again: %q", o.err)
	}
	if o = p.errand("", "envs", "box"); !strings.Contains(o.out, " ok ") {
		t.Errorf("once built: %q", o.out)
	}
	// a push that walks the copy again leaves it where it is
	p.write("images/b.def", "Bootstrap: docker\nFrom: alpine:3\n")
	if o = p.errand("", "envs", "box"); !strings.Contains(o.out, "ok") {
		t.Errorf("( the copy there is not touched by looking: %q )", o.out)
	}
	if o = p.errand("", "run", "-q", "--env", "box", "true"); o.code != 0 || nLines(p.builds()) != 2 || !strings.Contains(read(t, filepath.Join(copyOf, "images", "b.sif")), "alpine:3") {
		t.Errorf("a changed recipe, over there: exit %d, %q, built %d times", o.code, o.err, nLines(p.builds()))
	}
	if _, err := os.Stat(filepath.Join(copyOf, "images", "b.sif.prev")); err != nil {
		t.Errorf("the one it replaced, kept over there: %v", err)
	}
}

func TestAnImageIsBuiltInAnotherEnvironment(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	entered := p.apptainer()
	scratch := filepath.Join(filepath.Dir(p.root), "bscratch") // what the builder's own layers say, which a build must see
	_ = os.MkdirAll(scratch, 0o755)
	p.write("errand-envs.py", `import errand
errand.envs[ "builder" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/builder" ), errand.Vars( { "APPTAINER_TMPDIR": "`+scratch+`" } ) ] )
errand.envs[ "here" ] = errand.Env( [ errand.Apptainer( image = "images/c.sif", recipe = "images/c.def", build_on = "builder", install = "echo {image} >> installed.log" ), errand.Vars( { "INNER": "set" } ) ] )
errand.envs[ "there" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Apptainer( image = "images/c.sif", recipe = "images/c.def", build_on = "builder" ) ] )
errand.envs[ "lost" ] = errand.Env( [ errand.Apptainer( image = "images/l.sif", recipe = "images/c.def", build_on = "nowhere" ) ] )
errand.envs[ "odd" ] = errand.Env( [ errand.Apptainer( image = "images/o.sif", recipe = "images/c.def", build_on = 5 ) ] )
errand.envs[ "inline" ] = errand.Env( [ errand.Apptainer( image = "images/i.sif", recipe = "images/i.def", build_on = [ errand.Ssh( host = "box", root = "copy/inline" ), errand.Vars( { "APPTAINER_TMPDIR": "`+scratch+`" } ) ] ) ] )
errand.envs[ "single" ] = errand.Env( [ errand.Apptainer( image = "images/s.sif", recipe = "images/i.def", build_on = errand.Ssh( host = "box", root = "copy/single" ) ) ] )
`)
	p.write("images/i.def", "Bootstrap: docker\nFrom: debian\n")
	p.write("images/c.def", "Bootstrap: docker\nFrom: alpine\n")
	builderCopy := filepath.Join(p.far, "home", "copy", "builder")
	placeCopy := filepath.Join(p.far, "home", "copy", "proj")
	image := filepath.Join(p.root, "images", "c.sif")
	records := filepath.Join(p.root, "errand-outputs", "builds", "records", "apptainer-c.sif-*.json")

	// not made yet, and said so; a dry run says where it would be made
	if o := p.errand("", "envs", "here"); !strings.Contains(o.out, "not built") {
		t.Fatalf("before anything: %q", o.out)
	}
	if o := p.errand("", "setup", "--dry-run", "here"); o.code != 0 || !strings.Contains(o.err+o.out, "would be built in builder, and carried here") || nLines(p.builds()) != 0 {
		t.Errorf("a dry run: exit %d, %q %q, built %d times", o.code, o.out, o.err, nLines(p.builds()))
	}
	// the first run makes it in the builder, where its recipe is sent, brings it here, and enters it
	o := p.errand("", "run", "--env", "here", "sh", "-c", "echo in $IN_CONTAINER $INNER")
	if o.code != 0 || o.out != "in c.sif set\n" || !strings.Contains(o.err, "apptainer:c.sif is built in builder") || !strings.Contains(o.err, "images/c.sif: carried here") {
		t.Fatalf("built in another environment, entered here: exit %d, %q %q", o.code, o.out, o.err)
	}
	if got := read(t, image); !strings.Contains(got, "built from: Bootstrap: docker\nFrom: alpine") {
		t.Errorf("the image here: %q", got)
	}
	if _, err := os.Stat(filepath.Join(builderCopy, "images", "c.sif")); err != nil || nLines(p.builds()) != 1 {
		t.Errorf("built in the builder's copy: %v, built %d times", err, nLines(p.builds()))
	}
	if !strings.Contains(read(t, p.builds()), "tmp="+scratch) {
		t.Errorf("a run in the builder: it has what the builder's layers say: %q", read(t, p.builds()))
	}
	if !strings.Contains(read(t, filepath.Join(p.root, "installed.log")), "images/c.sif") {
		t.Errorf("what is done with it where it is used: %q", read(t, filepath.Join(p.root, "installed.log")))
	}
	if recs, _ := filepath.Glob(records); len(recs) != 1 || !strings.Contains(read(t, recs[0]), "built in builder") {
		t.Errorf("its record, here: %v", recs)
	}
	if o = p.errand("", "run", "-q", "--env", "here", "true"); o.code != 0 || nLines(p.builds()) != 1 || strings.Contains(o.err, "built in") {
		t.Errorf("an image that is right is neither made nor carried again: exit %d, %q, built %d times", o.code, o.err, nLines(p.builds()))
	}
	if o = p.errand("", "envs", "here"); !strings.Contains(o.out, " ok ") {
		t.Errorf("once carried: %q", o.out)
	}
	// a recipe that changed: made again there, brought again, the one it replaces kept
	p.write("images/c.def", "Bootstrap: docker\nFrom: alpine:3\n")
	if o = p.errand("", "envs", "here"); !strings.Contains(o.out, "stale") {
		t.Errorf("a changed recipe: %q", o.out)
	}
	if o = p.errand("", "run", "-q", "--env", "here", "true"); o.code != 0 || nLines(p.builds()) != 2 || !strings.Contains(read(t, image), "alpine:3") ||
		!strings.Contains(read(t, image+".prev"), "From: alpine\n") || strings.Count(read(t, filepath.Join(p.root, "installed.log")), "\n") != 2 {
		t.Errorf("made again: exit %d, %q, built %d times, %q", o.code, o.err, nLines(p.builds()), read(t, image))
	}
	// where it is used is another machine: it is sent there, and what the builder made is not made again
	o = p.errand("", "run", "--env", "there", "sh", "-c", "echo there in $IN_CONTAINER")
	if o.code != 0 || o.out != "there in c.sif\n" || nLines(p.builds()) != 2 {
		t.Fatalf("carried to another machine: exit %d, %q %q, built %d times", o.code, o.out, o.err, nLines(p.builds()))
	}
	if got := read(t, filepath.Join(placeCopy, "images", "c.sif")); !strings.Contains(got, "alpine:3") || !strings.Contains(read(t, entered), filepath.Join(placeCopy, "images", "c.sif")) {
		t.Errorf("the image over there: %q, entered %q", got, read(t, entered))
	}
	if o = p.errand("", "envs", "there"); !strings.Contains(o.out, " ok ") {
		t.Errorf("once carried over there: %q", o.out)
	}
	if o = p.errand("", "run", "-q", "--env", "there", "true"); o.code != 0 || strings.Contains(o.err, "built in") {
		t.Errorf("carried again: %q", o.err)
	}
	// an image changed outside errand, its recipe not: it is somebody's, and is kept as it is
	p.write("images/c.sif", "by hand\n")
	if o = p.errand("", "run", "--env", "here", "true"); o.code != 0 || nLines(p.builds()) != 2 || read(t, image) != "by hand\n" || !strings.Contains(o.err, "changed since errand made it") {
		t.Errorf("an image changed by hand: exit %d, %q, built %d times, %q", o.code, o.err, nLines(p.builds()), read(t, image))
	}
	// ...and its recipe too: said, and nothing is built or replaced, until it is asked for
	p.write("images/c.sif", "by hand 2\n")
	p.write("images/c.def", "Bootstrap: docker\nFrom: alpine:4\n")
	o = p.errand("", "run", "--env", "here", "true")
	if o.code != 1 || !strings.Contains(o.err, "changed outside errand") || !strings.Contains(o.err, "--force") || nLines(p.builds()) != 2 || read(t, image) != "by hand 2\n" {
		t.Errorf("an image changed by hand, and its recipe: exit %d, %q, built %d times, %q", o.code, o.err, nLines(p.builds()), read(t, image))
	}
	if o = p.errand("", "setup", "--force", "here"); o.code != 0 || nLines(p.builds()) != 3 || !strings.Contains(read(t, image), "alpine:4") || read(t, image+".prev") != "by hand 2\n" {
		t.Errorf("asked to go ahead: exit %d, %q, built %d times, %q, kept %q", o.code, o.err, nLines(p.builds()), read(t, image), read(t, image+".prev"))
	}
	// a build that fails says why, and leaves nothing: the image that was there is as it was
	p.write("images/c.def", "Bootstrap: docker\nFROM: FAIL\n")
	o = p.errand("", "run", "--env", "here", "true")
	if o.code != 1 || !strings.Contains(o.err, "the build of") || !strings.Contains(o.err, "apptainer:c.sif was not built in builder") || nLines(p.builds()) != 4 {
		t.Errorf("a build that fails: exit %d, %q, built %d times", o.code, o.err, nLines(p.builds()))
	}
	if !strings.Contains(read(t, image), "alpine:4") {
		t.Errorf("the image it would have replaced: %q", read(t, image))
	}
	if left, _ := filepath.Glob(filepath.Join(builderCopy, "images", "c.sif.new*")); len(left) != 0 {
		t.Errorf("what the builder left half made: %v", left)
	}
	// ...and --no-setup uses what is there, as it is
	if o = p.errand("", "run", "--no-setup", "-q", "--env", "here", "sh", "-c", "echo in $IN_CONTAINER"); o.out != "in c.sif\n" {
		t.Errorf("as it is: %q %q", o.out, o.err)
	}
	// the layers of the environment written in place, as a list or as one layer: an environment that has no name
	built := nLines(p.builds())
	if o = p.errand("", "run", "--env", "inline", "true"); o.code != 0 || nLines(p.builds()) != built+1 ||
		!strings.Contains(read(t, filepath.Join(p.root, "images", "i.sif")), "From: debian") || !strings.Contains(read(t, p.builds()), "tmp="+scratch) {
		t.Errorf("layers written in place: exit %d, %q, built %d times", o.code, o.err, nLines(p.builds())-built)
	}
	if o = p.errand("", "run", "--env", "single", "true"); o.code != 0 || nLines(p.builds()) != built+2 { // ( another builder's copy: made there )
		t.Errorf("one layer written in place: exit %d, %q, built %d times", o.code, o.err, nLines(p.builds())-built)
	}
	// a name that is no environment, and what is neither a name nor layers
	if o = p.errand("", "run", "--env", "lost", "true"); o.code != 1 || !strings.Contains(o.err, `build_on names the environment "nowhere", which this project does not declare`) {
		t.Errorf("an environment that is not declared: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run", "--env", "odd", "true"); o.code != 1 || !strings.Contains(o.err, "or the layers of one") {
		t.Errorf("something else: exit %d, %q", o.code, o.err)
	}
}

func TestARunIsDrawnInColoursAndIndented(t *testing.T) {
	p := newPlace(t)
	k := p.onTerminal(100, 30, nil, "run", "sh", "-c", `printf '\033[31mred\033[0m plain\n'; echo done; sleep 0.1; echo oops >&2; exit 0`)
	code, _ := k.finish()
	out := k.raw()
	if code != 0 {
		t.Fatalf("exit %d\n%q", code, out)
	}
	// ( what it wrote to stderr is behind the same bar, as it came; what is written to both within a millisecond may come
	// either way: each stream has a pseudo-terminal of its own )
	// where it runs, then the case in from it, its output in from the case behind a bar, and how it ended behind the bar too
	lines := strings.Split(onScreen(out), "\n")
	if len(lines) > 2 && strings.HasPrefix(lines[2], "    ↗ file:///") { // where its directory is, under its head
		lines = append(lines[:2], lines[3:]...)
	} else {
		t.Errorf("its directory is not said:\n%s", strings.Join(lines, "\n"))
	}
	if len(lines) < 6 || !strings.HasPrefix(lines[0], "▸ ") || !strings.HasPrefix(lines[1], "  ▸ sh -c") || lines[2] != "    │ red plain" ||
		lines[3] != "    │ done" || lines[4] != "    │ oops" || !strings.HasPrefix(lines[5], "    │ PASS sh -c") || strings.Contains(onScreen(out), "╰") {
		t.Errorf("the layout:\n%s", strings.Join(lines, "\n"))
	}
	// the palette, by role, and the program's own colours kept as it wrote them
	for what, code := range map[string]string{"where it runs": "\x1b[1m\x1b[35m▸ ", "a case": "\x1b[1m\x1b[36m▸ sh", "how it ended": "\x1b[1m\x1b[32mPASS\x1b[0m",
		"a bar": "\x1b[2m│\x1b[0m", "its own colours": "\x1b[31mred\x1b[0m plain"} {
		if !strings.Contains(out, code) {
			t.Errorf("%s: %q not drawn:\n%q", what, code, out)
		}
	}
	// no colour of errand's when `NO_COLOR` says so; the program's own are its own
	k = p.onTerminal(100, 30, []string{"NO_COLOR=1"}, "run", "sh", "-c", `printf '\033[31mred\033[0m plain\n'`)
	code, _ = k.finish()
	out = k.raw()
	if rest := strings.ReplaceAll(out, "\x1b[31mred\x1b[0m", ""); code != 0 || strings.Contains(rest, "\x1b[") || !strings.Contains(out, "\x1b[31mred\x1b[0m plain") {
		t.Errorf("NO_COLOR: exit %d\n%q", code, out)
	}
}

func TestARunIsLetGoOf(t *testing.T) {
	p := newPlace(t)
	// Ctrl-] lets go of the run: the command ends well, the run goes on with its input as it was, and `attach` follows it
	// again, from the beginning, on a terminal -- what is typed then reaches it
	k := p.onTerminal(100, 30, nil, "run", "sh", "-c", `echo started; printf "name"'? '; read n; echo "got $n"`)
	k.waitFor("│ name? ")
	k.typeIn("\x1d")
	code, out := k.finish()
	if code != 0 || !strings.Contains(out, "detached") || strings.Contains(out, "│ got") {
		t.Fatalf("let go of: exit %d\n%q", code, out)
	}
	id := regexp.MustCompile(`errand attach ([0-9a-f]{8})`).FindStringSubmatch(out)
	if id == nil {
		t.Fatalf("it does not say how to follow it again: %q", out)
	}
	if o := p.errand("", "status"); !strings.Contains(o.out, id[1]) || !strings.Contains(o.out, "running") {
		t.Errorf("it goes on: %q", o.out)
	}
	k = p.onTerminal(100, 30, nil, "attach", id[1])
	k.waitFor("│ started")
	k.typeIn("Hugo\r")
	code, out = k.finish()
	if code != 0 || !strings.Contains(onScreen(out), "│ got Hugo") {
		t.Errorf("followed again, and typed to: exit %d\n%q", code, out)
	}
}
