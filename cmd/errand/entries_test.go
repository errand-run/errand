// What a file holds, run by its tool ( the guide's "Running a suite"; ADR 0036 ), as a person at a terminal sees it.
// The tool is a `pytest` of a moment: a few lines of Python that list, run and report as pytest does, and say what
// they were asked -- the tests do not depend on what the machine's Python has.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/runinfo"
)

const pytestOfAMoment = `import importlib.util, os, sys, time
from xml.sax.saxutils import quoteattr
from pytest import Skipped

args = sys.argv[ 1 : ]
with open( os.environ[ "ASKED_OF_PYTEST" ], "a" ) as f:
    f.write( " ".join( args ) + "\n" )
collect = "--collect-only" in args
junit = next( ( a.split( "=", 1 )[ 1 ] for a in args if a.startswith( "--junitxml=" ) ), None )

def held( path ):
    spec = importlib.util.spec_from_file_location( os.path.basename( path )[ : -3 ], path )
    module = importlib.util.module_from_spec( spec )
    spec.loader.exec_module( module )
    for name, v in list( vars( module ).items() ):
        if name.startswith( "test_" ) and callable( v ):
            yield name, "", v
        elif name.startswith( "Test" ) and isinstance( v, type ):
            for n in vars( v ):
                if n.startswith( "test_" ):
                    yield name + "::" + n, "." + name, getattr( v(), n )

cases, failed, ran = [ ], 0, 0
for target in ( a for a in args if not a.startswith( "-" ) ):
    path, _, sel = target.partition( "::" )
    found = False
    for name, cls, f in held( path ):
        if sel and sel != name:
            continue
        found, ran = True, ran + 1
        if collect:
            print( f"{path}::{name}" )
            continue
        status, word = "passed", ""
        os.environ[ "PYTEST_CURRENT_TEST" ] = f"{path}::{name} (call)"
        try:
            f()
        except Skipped as s:
            status, word = "skipped", str( s )
        except Exception as e:
            status, word, failed = "failure", f"{type( e ).__name__}: {e}", failed + 1
        print( f"{status} {path}::{name}" )
        inner = "" if status == "passed" else f"<{status} message={quoteattr( word )}/>"
        cases.append( f"<testcase classname={quoteattr( path[ : -3 ].replace( '/', '.' ) + cls )} name={quoteattr( name.split( '::' )[ -1 ] )} time=\"0.01\">{inner}</testcase>" )
    if sel and not found:
        print( f"ERROR: not found: {target}", file = sys.stderr )
        sys.exit( 4 )
if collect:
    print( f"\n{ran} tests collected in 0.00s" )
if junit and cases:
    with open( junit, "w" ) as f:
        f.write( "<testsuites><testsuite>" + "".join( cases ) + "</testsuite></testsuites>" )
sys.exit( 5 if ran == 0 else 1 if failed else 0 )
`

// pytest: a `pytest` of a moment, on the path of the Python the runs start -> the file where it says what it was asked
func (p *scene) pytest() string {
	p.t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		p.t.Skip("no python3 here to run a file's tests with")
	}
	dir := filepath.Join(filepath.Dir(p.root), "pytest-of-a-moment")
	_ = os.MkdirAll(filepath.Join(dir, "pytest"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "pytest", "__init__.py"), []byte("class Skipped( Exception ):\n    pass\n\ndef skip( why ):\n    raise Skipped( why )\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "pytest", "__main__.py"), []byte(pytestOfAMoment), 0o644)
	asked := filepath.Join(filepath.Dir(p.root), "asked-of-pytest")
	path := dir
	for i, kv := range p.env { // before what is there already: errand's own package, for the declarations
		if rest, ok := strings.CutPrefix(kv, "PYTHONPATH="); ok {
			path += ":" + rest
			p.env[i] = "PYTHONPATH=unused"
		}
	}
	p.env = append(p.env, "PYTHONPATH="+path, "ASKED_OF_PYTEST="+asked, "PYTHONDONTWRITEBYTECODE=1")
	return asked
}

func (p *scene) suite() {
	p.write("tests/test_a.py", `import pytest
def test_ok(): pass
def test_other(): pass
class TestK:
    def test_m( self ): pass
def test_bad(): assert 1 == 2, "one is not two"
`)
	p.write("tests/sub/test_b.py", `import pytest
def test_one(): pass
def test_skip(): pytest.skip( "not here" )
`)
	p.write("tests/helper.py", "raise SystemExit( 'a library, not a suite: nothing runs it' )\n")
	p.write("build/test_made.py", "def test_x(): pass\n")
}

// askedOf: what the tool was asked, one invocation per line, since `from`
func askedOf(t *testing.T, path string, from int) []string {
	t.Helper()
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if from > len(lines) {
		t.Fatalf("the tool was asked %d times, fewer than before ( %d )", len(lines), from)
	}
	return lines[from:]
}

func TestWhatAFileHolds(t *testing.T) {
	p := newPlace(t)
	asked := p.pytest()
	p.suite()

	// every suite under a directory: a file is one run of its tool, and the tool is not asked what the files hold
	o := p.errand("", "run", "tests/**::*")
	got := askedOf(t, asked, 0)
	if len(got) != 2 || strings.Contains(strings.Join(got, "\n"), "--collect-only") {
		t.Fatalf("two files, two runs, and nothing listed: the tool was asked %q\n%s%s", got, o.out, o.err)
	}
	for _, line := range got {
		if !strings.HasSuffix(line, " tests/sub/test_b.py") && !strings.HasSuffix(line, " tests/test_a.py") {
			t.Errorf("a whole file is handed to the tool by its name, from the root: %q", line)
		}
	}
	if o.code != 1 || !strings.Contains(o.err, "FAIL tests/test_a.py::*") || !strings.Contains(o.err, "PASS tests/sub/test_b.py::*") {
		t.Fatalf("one file fails, the other passes: exit %d\n%s%s", o.code, o.out, o.err)
	}
	if !strings.Contains(o.err, "3 passed · 1 failed") || !strings.Contains(o.err, "1 passed · 1 skipped") {
		t.Errorf("what each tool reported is not said with its run:\n%s", o.err)
	}
	leaf := filepath.Join(p.root, "errand-outputs", "runs", "tests", "test_a.py", "latest")
	res, ok := runinfo.ReadRecord(filepath.Join(leaf, runinfo.ResultName))
	if !ok || res.Status != "FAIL" || res.Error == nil || !strings.Contains(*res.Error, "1 failed: test_bad -- AssertionError: one is not two") {
		t.Fatalf("the record of the file that fails: %+v", res)
	}
	var names []string
	for _, e := range res.Entries {
		names = append(names, e.Name+" "+e.Status)
	}
	if want := []string{"test_ok PASS", "test_other PASS", "TestK::test_m PASS", "test_bad FAIL"}; !reflect.DeepEqual(names, want) {
		t.Errorf("an entry each, named as the tool names what a file holds: %q", names)
	}
	if res.File != "tests/test_a.py" || res.Name != "tests/test_a.py" || kit.Num(res.Results["passed"], -1) != 3 {
		t.Errorf("filed under its file, with its counts: %+v", res)
	}
	other, _ := runinfo.ReadRecord(filepath.Join(p.root, "errand-outputs", "runs", "tests", "sub", "test_b.py", "latest", runinfo.ResultName))
	if other.Status != "PASS" || len(other.Entries) != 2 || other.Entries[1].Status != "SKIP" || other.Entries[1].Message != "not here" {
		t.Errorf("a skipped entry is said skipped, with its word: %+v", other)
	}

	// one entry, by its name: handed to the tool as it is written, in a directory of its own
	before := len(askedOf(t, asked, 0))
	o = p.errand("", "run", "tests/test_a.py::TestK::test_m")
	got = askedOf(t, asked, before)
	if o.code != 0 || len(got) != 1 || !strings.HasSuffix(got[0], " tests/test_a.py::TestK::test_m") {
		t.Fatalf("one entry: exit %d, asked %q\n%s", o.code, got, o.err)
	}
	if o.result.Status != "PASS" || len(o.result.Entries) != 1 || !strings.Contains(filepath.ToSlash(o.leaf), "runs/tests/test_a.py/TestK__test_m/") {
		t.Errorf("its record, and where it is filed: %s %+v", o.leaf, o.result)
	}

	// what follows the target is the tool's
	before = len(askedOf(t, asked, 0))
	o = p.errand("", "run", "tests/test_a.py::test_ok", "-x", "--tb=short")
	if got = askedOf(t, asked, before); o.code != 0 || len(got) != 1 || !strings.HasSuffix(got[0], " tests/test_a.py::test_ok -x --tb=short") {
		t.Errorf("the tool's own arguments: exit %d, asked %q", o.code, got)
	}

	// a name written as a pattern: the tool is asked what the file holds, once, and handed those that match
	before = len(askedOf(t, asked, 0))
	o = p.errand("", "run", "tests/test_a.py::test_o*,tests/sub/test_b.py::{test_one,nope*}")
	if o.code != 1 || !strings.Contains(o.err, "'tests/sub/test_b.py::nope*' matches nothing -- there: test_one  test_skip") {
		t.Fatalf("a pattern that matches nothing is an error that says what is there: exit %d\n%s", o.code, o.err)
	}
	if got = askedOf(t, asked, before); len(got) != 1 || !strings.Contains(got[0], "--collect-only") {
		t.Errorf("nothing runs when a target names nothing: asked %q", got)
	}
	before = len(askedOf(t, asked, 0))
	o = p.errand("", "run", "tests/test_a.py::test_o*,tests/test_a.py::test_m*")
	got = askedOf(t, asked, before)
	if o.code != 0 || len(got) != 2 || !strings.HasSuffix(got[0], "--collect-only -q tests/test_a.py") ||
		!strings.HasSuffix(got[1], " tests/test_a.py::test_ok tests/test_a.py::test_other tests/test_a.py::TestK::test_m") {
		t.Fatalf("listed once, then one run of those that match, a class's by their own name too: exit %d, asked %q\n%s", o.code, got, o.err)
	}
	if left, _ := filepath.Glob(filepath.Join(p.root, "errand-outputs", "state", "aside", "*")); len(left) != 0 {
		t.Errorf("a listing is errand's own: nothing of it is kept, %q", left)
	}
	if _, err := os.Stat(filepath.Join(p.root, "errand-outputs", "runs", "list")); err == nil {
		t.Errorf("a listing was filed among the runs")
	}

	// what a target cannot designate is said before anything runs
	before = len(askedOf(t, asked, 0))
	for _, c := range [][2]string{
		{"tests/test_zz.py::x", "tests/test_zz.py: no such file or directory"},
		{"tests/helper.py::x", "tests/helper.py: no entry runner takes it"},
		{"docs/**::*", "matches no file an entry runner takes"},
		{"build/**::*", "matches no file an entry runner takes"}, // ( what a build leaves is nobody's suite )
	} {
		if o = p.errand("", "run", c[0]); o.code != 1 || !strings.Contains(o.err, c[1]) {
			t.Errorf("%s: exit %d, %q", c[0], o.code, o.err)
		}
	}
	if o = p.errand("", "run", "-r", "nobody", "tests/**::*"); o.code != 1 || !strings.Contains(o.err, "-r nobody: no runner of that name") {
		t.Errorf("a runner nobody declared: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run", "-r", "pytest", "tests/helper.py"); o.code != 2 || !strings.Contains(o.err, "-r pytest picks the runner of what a file holds") {
		t.Errorf("-r with a file run whole: exit %d, %q", o.code, o.err)
	}
	if got = askedOf(t, asked, before); len(got) != 0 {
		t.Errorf("a typo ran something: %q", got)
	}

	// -r NAME: that runner, whatever the files' names say
	before = len(askedOf(t, asked, 0))
	p.write("checks/api.py", "def test_api(): pass\n")
	if o = p.errand("", "run", "checks/api.py::*"); o.code != 1 || !strings.Contains(o.err, "no entry runner takes it") {
		t.Fatalf("a file no runner takes: exit %d, %q", o.code, o.err)
	}
	o = p.errand("", "run", "-r", "python", "checks/api.py::*")
	if o.code != 1 || !strings.Contains(o.err, "-r python: it runs a file whole") {
		t.Errorf("a file runner asked for what a file holds: exit %d, %q", o.code, o.err)
	}

	// an empty run looks like a passing one: the tool's own code for "nothing was selected" is a failure
	p.write("tests/test_empty.py", "x = 1\n")
	if o = p.errand("", "run", "tests/test_empty.py::*"); o.code == 0 || o.result.Status != "FAIL" || o.result.Error == nil || !strings.Contains(*o.result.Error, "no entry was selected") {
		t.Errorf("a file that holds nothing: exit %d, %+v", o.code, o.result)
	}
}

func TestSeveralRunsAtOnce(t *testing.T) {
	p := newPlace(t)
	asked := p.pytest()
	p.suite()

	// as many files as runs asked for: a file each, nothing listed
	o := p.errand("", "run", "-j", "2", "tests/**::*")
	got := askedOf(t, asked, 0)
	if o.code != 1 || len(got) != 2 || strings.Contains(strings.Join(got, "\n"), "--collect-only") {
		t.Fatalf("-j 2 over two files: exit %d, asked %q\n%s", o.code, got, o.err)
	}
	if !strings.Contains(o.err, "2 at a time") || !strings.Contains(o.err, "FAIL tests/test_a.py::*") || !strings.Contains(o.err, "PASS tests/sub/test_b.py::*") {
		t.Errorf("each run is said, whole, when it ends:\n%s", o.err)
	}
	// what one run printed is not cut by another's: its lines come together, then its end
	lines := strings.Split(o.out+o.err, "\n")
	for i, line := range lines {
		if strings.Contains(line, "passed tests/sub/test_b.py::test_one") {
			if i+1 >= len(lines) || !strings.Contains(lines[i+1], "tests/sub/test_b.py::test_skip") {
				t.Errorf("two runs' lines are mixed:\n%s", strings.Join(lines, "\n"))
			}
		}
	}

	// more runs asked than files: the tool is asked what the file holds, and its entries are spread
	before := len(got)
	o = p.errand("", "run", "-j3", "tests/test_a.py::*")
	got = askedOf(t, asked, before)
	if len(got) != 4 || !strings.Contains(got[0], "--collect-only") {
		t.Fatalf("-j 3 on one file of four entries: one listing, three runs -- asked %q\n%s", got, o.err)
	}
	var ran []string
	for _, line := range got[1:] {
		for _, w := range strings.Fields(line) {
			if strings.HasPrefix(w, "tests/test_a.py::") {
				ran = append(ran, strings.TrimPrefix(w, "tests/test_a.py::"))
			}
		}
	}
	sort.Strings(ran)
	if want := []string{"TestK::test_m", "test_bad", "test_ok", "test_other"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("every entry once, over the three runs: %q", ran)
	}
	if o.code != 1 || !strings.Contains(o.err, "tests/test_a.py::*  1/3") || !strings.Contains(o.err, "tests/test_a.py::*  3/3") {
		t.Errorf("each of the three is named: exit %d\n%s", o.code, o.err)
	}
	records, _ := filepath.Glob(filepath.Join(p.root, "errand-outputs", "runs", "tests", "test_a.py", "*of3*", runinfo.ResultName))
	if len(records) != 3 {
		t.Errorf("a directory each, of the same file: %q", records)
	}

	// files run whole, several at a time as well
	p.write("a.sh", "#!/bin/sh\necho a-says\n")
	p.write("b.sh", "#!/bin/sh\necho b-says\n")
	for _, f := range []string{"a.sh", "b.sh"} {
		_ = os.Chmod(filepath.Join(p.root, f), 0o755)
	}
	if o = p.errand("", "run", "-j", "2", "a.sh,b.sh"); o.code != 0 || !strings.Contains(o.out, "a-says") || !strings.Contains(o.out, "b-says") {
		t.Errorf("two scripts at once: exit %d, %q %q", o.code, o.out, o.err)
	}
	if left := p.jobsLeft(p.cache()); len(left) != 0 {
		t.Errorf("the agent kept jobs that were looked at: %q", left)
	}
}

func TestAProjectsRunners(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	asked := p.pytest()
	p.suite()
	p.write("checks/api.py", "def test_api(): pass\n")
	p.write("errand-project.py", `import errand
errand.default_run = [ "tests/sub/**::*", "checks/**::*" ]
errand.runners.replace( errand.Pytest( args = [ "-x" ], patterns = [ "test_*.py", "api.py" ] ) )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	// a bare `errand run`: what the project says it runs, from its root, with the runner as the project made it
	o := p.errand("", "run")
	got := askedOf(t, asked, 0)
	if o.code != 0 || len(got) != 2 {
		t.Fatalf("a bare run: exit %d, asked %q\n%s%s", o.code, got, o.out, o.err)
	}
	for _, line := range got {
		if !strings.Contains(line, " -x ") || !(strings.HasSuffix(line, " tests/sub/test_b.py") || strings.HasSuffix(line, " checks/api.py")) {
			t.Errorf("the project's own Pytest( args, patterns ): %q", line)
		}
	}

	// from a directory of the project: a target is a path from where the command is typed, and the tool is handed
	// the file from the root, where it is started
	before := len(got)
	cmd := p.command("run", "-q", "sub/test_b.py::test_one")
	cmd.Dir = filepath.Join(p.root, "tests")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("from tests/: %v\n%s", err, out)
	}
	if got = askedOf(t, asked, before); len(got) != 1 || !strings.HasSuffix(got[0], " tests/sub/test_b.py::test_one") {
		t.Errorf("typed from tests/, the file is still named from the root: %q", got)
	}

	// on another machine: what the targets designate is asked there, of the copy; its runs are made there, and their
	// records come back with what the tool reported
	before = len(askedOf(t, asked, 0))
	o = p.errand("", "run", "--env", "box", "tests/sub/test_b.py::*")
	got = askedOf(t, asked, before)
	if o.code != 0 || len(got) != 1 || !strings.HasSuffix(got[0], " tests/sub/test_b.py") {
		t.Fatalf("elsewhere: exit %d, asked %q\n%s%s", o.code, got, o.out, o.err)
	}
	if !o.hasResult || o.result.Env != "box" || o.result.Project != filepath.Join(p.far, "home", "copy", "proj") || len(o.result.Entries) != 2 {
		t.Errorf("its record, brought back: %+v", o.result)
	}
	if o = p.errand("", "run", "--env", "box", "tests/test_zz.py::*"); o.code != 1 || !strings.Contains(o.err, "tests/test_zz.py: no such file or directory") {
		t.Errorf("a typo, said from over there: exit %d, %q", o.code, o.err)
	}
}

// What this program holds for a directory nobody declared anything about is what errand's Python says of the runners
// errand comes with.
func TestTheRunnersErrandComesWith(t *testing.T) {
	env := python(t)
	root := t.TempDir()
	cmd := exec.Command(strings.TrimPrefix(env[0], "ERRAND_DECLARE_PYTHON="), "-m", "errand.declared", root)
	cmd.Env = append(os.Environ(), "PYTHONPATH=../../lib")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("errand.declared: %v", err)
	}
	var doc project.Document
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Rev < 2 || !reflect.DeepEqual(doc.Runners, project.Builtin()) {
		t.Errorf("the two say different things:\n python: %+v\n here:   %+v", doc.Runners, project.Builtin())
	}
}

// ── what a run asks for, from its code ( ADR 0036, "what a run needs is asked from its code, by any tool" ) ─────────

// ran: the runs made under `name`, as their records say, the oldest first -- a file's, or a script's
type ran struct {
	dir string
	runinfo.Record
}

func (p *scene) ran(name string) []ran {
	var out []ran
	_ = filepath.Walk(filepath.Join(p.root, "errand-outputs", "runs", filepath.FromSlash(name)), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == runinfo.ResultName {
			if r, ok := runinfo.ReadRecord(path); ok {
				out = append(out, ran{filepath.Dir(path), r})
			}
		}
		return nil
	})
	sort.Slice(out, func(i, k int) bool { return out[i].Started < out[k].Started })
	return out
}

// within: whether something that happens on its own does, in a few seconds
func within(t *testing.T, what string, done func() bool) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if done() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("not within 15 s: %s", what)
}

func count(s, of string) int { return strings.Count(s, of) }

func TestWhatACodeAsksFor(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	asked := p.pytest()
	p.apptainer()
	launched := p.launchers()
	said := func() string { b, _ := os.ReadFile(launched); return string(b) }

	// a script that asks for ranks stops, and is run again whole under the place's launcher, with that many, once
	p.write("ranks.sh", "#!/bin/sh\n\"$ERRAND\" require --ranks 3 || exit $?\necho \"rank ${PMI_RANK:-none} of $ERRAND_RANKS\"\n")
	o := p.errand("", "run", "-q", "ranks.sh")
	if o.code != 0 || sortedLines(o.out) != "rank 0 of 3 ; rank 1 of 3 ; rank 2 of 3" || count(said(), "mpiexec -n 3") != 1 || count(said(), "mpiexec") != 1 {
		t.Fatalf("a script that asks for ranks: exit %d, %q %q\n%s", o.code, o.out, o.err, said())
	}
	if !strings.Contains(o.err, "3 ranks asked, this run has 1") {
		t.Errorf("what stopped it is said: %q", o.err)
	}
	made := p.ran("ranks.sh")
	if len(made) != 2 {
		t.Fatalf("two runs, the one that asked and the one made again: %+v", made)
	}
	first, second := made[0], made[1]
	if first.Status != "SKIP" || len(first.Asked) != 1 || first.Asked[0].Ranks != 3 || len(first.Again) != 1 || first.Again[0].Leaf != second.dir ||
		first.Error == nil || !strings.Contains(*first.Error, "asked for 3 ranks: run again") {
		t.Errorf("the first run's record says what was asked, and where it was run again: %+v", first.Record)
	}
	if second.Status != "PASS" || kit.Num(second.Params["again"], 0) != 1 || len(second.Asked) != 0 {
		t.Errorf("the run made again: %+v", second.Record)
	}
	if left := p.jobsLeft(p.cache()); len(left) != 0 {
		t.Errorf("the agent kept jobs that were looked at: %q", left)
	}

	// an entry of a file that asks for ranks is skipped, the others pass, and it alone is run again, with ranks
	// ( a file that imports errand is one of errand's own entries, which this program does not run yet: it asks through
	// the binding without importing it where a runner would see )
	p.write("tests/test_r.py", `def test_plain(): pass
def test_ranks():
    __import__( "errand" ).require( ranks = 2 )
def test_other(): pass
`)
	before, ranked := len(askedOf(t, asked, 0)), count(said(), "mpiexec")
	o = p.errand("", "run", "tests/test_r.py::*")
	got := askedOf(t, asked, before)
	if o.code != 0 || len(got) != 3 || strings.HasSuffix(got[0], "::test_ranks") || !strings.HasSuffix(got[1], " tests/test_r.py::test_ranks") || !strings.HasSuffix(got[2], " tests/test_r.py::test_ranks") {
		t.Fatalf("a file's entries: the whole file, then the one that asked, by its two ranks: exit %d, asked %q\n%s%s", o.code, got, o.out, o.err)
	}
	if count(said(), "mpiexec -n 2") != 1 || count(said(), "mpiexec") != ranked+1 {
		t.Errorf("run again under the launcher, with two ranks, once:\n%s", said())
	}
	made = p.ran("tests/test_r.py")
	if len(made) != 2 || made[0].Status != "PASS" || len(made[0].Entries) != 3 || made[0].Entries[1].Name != "test_ranks" || made[0].Entries[1].Status != "SKIP" ||
		!strings.Contains(made[0].Entries[1].Message, "asks for 2 ranks") || made[0].Entries[0].Status != "PASS" || made[0].Entries[2].Status != "PASS" {
		t.Fatalf("the first run: the others passed, the one that asked is skipped, saying what it asked: %+v", made)
	}
	if len(made[0].Asked) != 1 || made[0].Asked[0].Entry != "tests/test_r.py::test_ranks" || len(made[0].Again) != 1 {
		t.Errorf("its record says who asked: %+v", made[0].Record)
	}
	if made[1].Status != "PASS" || len(made[1].Entries) != 1 || made[1].Entries[0].Name != "test_ranks" || made[1].Entries[0].Status != "PASS" {
		t.Errorf("the entry that asked, run again and passing: %+v", made[1].Record)
	}

	// entries that ask for the same thing in one invocation go together; one that asks for another thing goes apart
	p.write("tests/test_g.py", `def test_a(): __import__( "errand" ).require( ranks = 2 )
def test_b(): __import__( "errand" ).require( ranks = 2 )
def test_c(): __import__( "errand" ).require( ranks = 3 )
def test_d(): pass
`)
	before, ranked = len(askedOf(t, asked, 0)), count(said(), "mpiexec")
	twos, threes := count(said(), "mpiexec -n 2"), count(said(), "mpiexec -n 3")
	o = p.errand("", "run", "tests/test_g.py::*")
	got = askedOf(t, asked, before)
	together := 0
	for _, line := range got {
		if strings.Contains(line, "tests/test_g.py::test_a tests/test_g.py::test_b") {
			together++
		}
	}
	if o.code != 0 || together != 2 || count(said(), "mpiexec -n 2") != twos+1 || count(said(), "mpiexec -n 3") != threes+1 || count(said(), "mpiexec") != ranked+2 {
		t.Fatalf("two entries asking the same go together, the third alone: exit %d, asked %q\n%s%s", o.code, got, said(), o.err)
	}
	if made = p.ran("tests/test_g.py"); len(made) != 3 || len(made[0].Again) != 2 || len(made[0].Asked) != 3 {
		t.Errorf("the first run says all three asked, and both runs made after it: %+v", made)
	}

	// what can be given where the run is stays given there: the claim grows, the call waits, nothing is run again
	cpus := 0
	if m := regexp.MustCompile(`cpus=([0-9]+)`).FindStringSubmatch(p.errand("", "queue").out); m != nil {
		cpus, _ = strconv.Atoi(m[1])
	}
	if cpus < 2 {
		t.Log("a machine of one core: nothing can be made to wait for another core")
	} else {
		gate := filepath.Join(filepath.Dir(p.root), "gate")
		p.write("hold.sh", "#!/bin/sh\nwhile [ ! -e "+gate+" ]; do sleep 0.1; done\n")
		p.write("grow.sh", "#!/bin/sh\n[ -s \"$ERRAND_ASKS\" ] && echo \"asked before\"; [ -f \"$ERRAND_ASKS\" ] && echo \"the file of requests is there\"\n\"$ERRAND\" require --cpus "+strconv.Itoa(cpus)+"\necho granted\n")
		p.errand("", "run", "--batch", "-q", "--cpus", "1", "hold.sh")
		cmd := p.command("run", "-q", "--cpus", "1", "grow.sh")
		var out, errs lockedBuffer
		cmd.Stdout, cmd.Stderr = &out, &errs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		within(t, "the run that grows waits for the core another holds", func() bool { return strings.Contains(errs.String(), "waiting for") })
		_ = os.WriteFile(gate, nil, 0o644)
		err := cmd.Wait()
		if err != nil || !strings.Contains(out.String(), "granted") || !strings.Contains(out.String(), "the file of requests is there") || strings.Contains(out.String(), "asked before") {
			t.Errorf("a request that can be granted in place waits there: %v %q %q", err, out.String(), errs.String())
		}
		if made = p.ran("grow.sh"); len(made) != 1 || made[0].Status != "PASS" || len(made[0].Asked) != 0 || len(made[0].Again) != 0 {
			t.Errorf("and nothing is run again: %+v", made)
		}
	}

	// asking again for what it was just given fails, and says so
	p.write("always.sh", "#!/bin/sh\necho '{\"ranks\":2,\"entry\":\"\"}' >> \"$ERRAND_ASKS\"\nexit 75\n")
	ranked = count(said(), "mpiexec")
	o = p.errand("", "run", "-q", "always.sh")
	if o.code == 0 || o.code == 75 || !strings.Contains(o.err, "asked for 2 ranks, which it was given") || count(said(), "mpiexec") != ranked+1 {
		t.Errorf("run again once, and not for ever: exit %d, %q\n%s", o.code, o.err, said())
	}
	if made = p.ran("always.sh"); len(made) != 2 || made[0].Status != "SKIP" || made[1].Status != "FAIL" || made[1].Error == nil || !strings.Contains(*made[1].Error, "which it was given") {
		t.Errorf("the one that asks again is the one that fails: %+v", made)
	}

	// a run that passed lacked nothing: a script that asked and went on, an entry that asked and passed, are not run again
	p.write("swallowed.sh", "#!/bin/sh\n\"$ERRAND\" require --ranks 2\necho \"swallowed $ERRAND_RANKS\"\n")
	p.write("tests/test_s.py", `import subprocess, os
def test_swallowed():
    subprocess.run( [ os.environ[ "ERRAND" ], "require", "--ranks", "2" ] )
def test_asks():
    __import__( "errand" ).require( ranks = 2 )
`)
	ranked, before = count(said(), "mpiexec"), len(askedOf(t, asked, 0))
	o = p.errand("", "run", "-q", "swallowed.sh")
	if made = p.ran("swallowed.sh"); o.code != 0 || o.out != "swallowed 1\n" || len(made) != 1 || made[0].Status != "PASS" || len(made[0].Again) != 0 || count(said(), "mpiexec") != ranked {
		t.Errorf("asked, and went on to pass: exit %d, %q\n%+v\n%s", o.code, o.out, made, said())
	}
	o = p.errand("", "run", "tests/test_s.py::*")
	got = askedOf(t, asked, before)
	if made = p.ran("tests/test_s.py"); o.code != 0 || len(got) != 3 || !strings.HasSuffix(got[1], " tests/test_s.py::test_asks") || strings.Contains(strings.Join(got[1:], ""), "test_swallowed") || len(made) != 2 {
		t.Errorf("only the entry that was skipped is run again, not the one that asked and passed: exit %d, asked %q\n%+v", o.code, got, made)
	}

	// the same, with nobody following: the agent that was asked to launch it launches it again
	p.write("later.sh", "#!/bin/sh\n\"$ERRAND\" require --ranks 2 || exit $?\necho \"later $PMI_RANK of $ERRAND_RANKS\"\n")
	ranked = count(said(), "mpiexec")
	id := strings.Fields(p.errand("", "run", "--batch", "-q", "later.sh").out + " x")[0]
	within(t, "the run that asked is made again with nobody to follow it", func() bool {
		made = p.ran("later.sh")
		return len(made) == 2 && made[1].Status == "PASS"
	})
	if count(said(), "mpiexec") != ranked+1 || made[0].Status != "SKIP" || len(made[0].Again) != 1 {
		t.Errorf("run again, once, with its ranks: %+v\n%s", made, said())
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "again, 2 ranks") {
		t.Errorf("`errand status` finds the run made again, as it finds the first ( %s ): %q", id[:8], o.out)
	}
	if o = p.errand("", "attach", id); o.code != 0 || !strings.Contains(o.out, "later 0 of 2") || !strings.Contains(o.out, "later 1 of 2") {
		t.Errorf("`errand attach` carries to the run made again: exit %d, %q %q", o.code, o.out, o.err)
	}

	// inside a container, the run made again is routed as the first: the ranks start inside it, where its layer says
	p.write("errand-envs.py", `import errand
errand.envs[ "in" ] = errand.Env( [ errand.Apptainer( image = "images/a.sif" ), errand.Mpi( "mpirun" ) ] )
`)
	p.write("images/a.sif", "an image\n")
	p.write("boxed.sh", "#!/bin/sh\n\"$ERRAND\" require --ranks 2 || exit $?\necho \"$IN_CONTAINER r$PMI_RANK of $ERRAND_RANKS\"\n")
	o = p.errand("", "run", "-q", "--env", "in", "boxed.sh")
	if o.code != 0 || sortedLines(o.out) != "a.sif r0 of 2 ; a.sif r1 of 2" || count(said(), "mpirun -n 2") != 1 {
		t.Errorf("asked inside a container: exit %d, %q %q\n%s", o.code, o.out, o.err, said())
	}
}

// A request is written down by who asks, and what cannot be given where the run is is not waited for.
func TestWhatIsWrittenDown(t *testing.T) {
	p := newPlace(t)
	asks := filepath.Join(filepath.Dir(p.root), "asks.jsonl")
	_ = os.WriteFile(asks, nil, 0o644)
	call := func(env []string, args ...string) (int, string) {
		cmd := exec.Command(built, append([]string{"require"}, args...)...)
		cmd.Env = append(append([]string{}, p.env...), env...)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return code, string(out)
	}
	// outside errand it does nothing
	if code, out := call(nil, "--ranks", "4", "--gpus", "1"); code != 0 || out != "" {
		t.Errorf("by hand: exit %d, %q", code, out)
	}
	if code, _ := call([]string{"ERRAND_ASKS=" + asks, "ERRAND_RANKS=4"}, "--ranks", "4"); code != 0 {
		t.Errorf("ranks it has are not asked for: exit %d", code)
	}
	// pytest says who asks; --entry says it for what does not
	code, out := call([]string{"ERRAND_ASKS=" + asks, "PYTEST_CURRENT_TEST=tests/test_a.py::TestK::test_m[1] (call)"}, "--ranks", "4")
	if code != 75 || !strings.Contains(out, "4 ranks asked, this run has 1") {
		t.Errorf("asked of pytest's test: exit %d, %q", code, out)
	}
	if code, _ = call([]string{"ERRAND_ASKS=" + asks, "PYTEST_CURRENT_TEST=tests/test_a.py::test_x (call)"}, "--ranks", "2", "--entry", "suite/case one"); code != 75 {
		t.Errorf("--entry: exit %d", code)
	}
	var lines []runinfo.Ask
	for _, a := range runinfo.ReadAsks(asks) {
		lines = append(lines, a)
	}
	if len(lines) != 2 || lines[0].Entry != "tests/test_a.py::TestK::test_m[1]" || lines[0].Ranks != 4 || lines[1].Entry != "suite/case one" || lines[1].Ranks != 2 {
		t.Errorf("who asked, and what: %+v", lines)
	}
	if code, out = call([]string{"ERRAND_ASKS=" + asks}, "--ranks", "0"); code != 2 || !strings.Contains(out, "1 or more") {
		t.Errorf("a number of ranks that is not one: exit %d, %q", code, out)
	}
}

// lockedBuffer: what a command wrote, read while it still writes
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// What asks is run again by the agent that was asked to launch it: handed to a batch system, it is another allocation
// when the one kept does not fit; on another machine, the agent there; and whoever follows is carried to it.
func TestAskedOfABatchSystem(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.env = append(p.env, "CUDA_VISIBLE_DEVICES=") // no card, whatever runs this: `require` would print those the run holds
	slurm := p.slurm()
	asked := p.launchers()
	p.write("errand-envs.py", `import errand
errand.envs[ "hpc" ] = errand.Env( [ errand.Slurm( partition = "short", time = "00:10:00", keep_alive = 30 ), errand.Vars( { "IN": "alloc" } ) ] )
errand.envs[ "far" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	submitted := func() []string {
		return strings.Split(strings.TrimSpace(read(t, filepath.Join(slurm, "submitted"))), "\n")
	}
	launched := func() string { return read(t, asked) }
	p.write("ranks.sh", "#!/bin/sh\n\"$ERRAND\" require --ranks 3 || exit $?\necho \"$IN r$PMI_RANK of $ERRAND_RANKS\"\n")

	// the allocation that was asked for has room for one rank: the run asks for three, and gets another allocation
	o := p.errand("", "run", "-q", "--env", "hpc", "ranks.sh")
	sent := submitted()
	if o.code != 0 || sortedLines(o.out) != "alloc r0 of 3 ; alloc r1 of 3 ; alloc r2 of 3" || len(sent) != 2 || !strings.Contains(sent[1], "--ntasks 3") ||
		strings.Contains(sent[0], "--ntasks 3") || !strings.Contains(launched(), "srun --overlap") || !strings.Contains(launched(), "--ntasks 3") {
		t.Fatalf("asked inside an allocation: exit %d, %q %q\nsubmitted %q\n%s", o.code, o.out, o.err, sent, launched())
	}
	if !strings.Contains(read(t, filepath.Join(slurm, "cancelled")), "101") {
		t.Errorf("the allocation that did not fit, with nothing in it, is given back")
	}
	made := p.ran("ranks.sh")
	if len(made) != 2 || made[0].Status != "SKIP" || len(made[0].Again) != 1 || made[0].Again[0].Leaf != made[1].dir || made[1].Status != "PASS" || made[0].Env != "hpc" {
		t.Errorf("the record of each: %+v", made)
	}

	// what the allocation does not have is asked the same way, and is not asked for for ever ( the allocation kept is sized
	// for whatever the batch system gives by default: the run made again goes to it, and finds it has no more )
	p.write("big.sh", "#!/bin/sh\n\"$ERRAND\" require --cpus 100000 || exit $?\necho big\n")
	was := len(submitted())
	o = p.errand("", "run", "-q", "--env", "hpc", "big.sh")
	if o.code == 0 || !strings.Contains(o.err, "100000 cpus asked, the allocation has") || !strings.Contains(o.err, "which it was given") || len(submitted()) != was+1 {
		t.Errorf("asked for cores an allocation has not: exit %d, %q %q\nsubmitted %q", o.code, o.out, o.err, submitted())
	}
	if made = p.ran("big.sh"); len(made) != 2 || made[0].Status != "SKIP" || made[1].Status != "FAIL" {
		t.Errorf("run again once, and the second asking is the failure: %+v", made)
	}

	// with nobody following: the agent that submitted it launches it again, and the run is found from the first
	p.write("later.sh", "#!/bin/sh\n\"$ERRAND\" require --ranks 2 || exit $?\necho \"$IN later $PMI_RANK\"\n")
	was = len(submitted())
	id := strings.Fields(p.errand("", "run", "--batch", "-q", "--env", "hpc", "later.sh").out + " x")[0]
	within(t, "the run that asked is made again, by the agent that submitted it", func() bool {
		made = p.ran("later.sh")
		return len(made) == 2 && made[1].Status == "PASS"
	})
	if sent = submitted(); len(sent) != was+1 || !strings.Contains(sent[len(sent)-1], "--ntasks 2") || made[0].Status != "SKIP" {
		t.Errorf("another allocation, for the run made again, the one kept having room for one: %q %+v", sent, made)
	}
	if o = p.errand("", "attach", id); o.code != 0 || !strings.Contains(o.out, "alloc later 1") {
		t.Errorf("attached to the first, carried to the second: exit %d, %q %q", o.code, o.out, o.err)
	}

	// on another machine: the agent there runs it again, and whoever follows is carried to it, its record brought back
	was2 := count(launched(), "mpiexec -n 3")
	o = p.errand("", "run", "-q", "--env", "far", "ranks.sh")
	if o.code != 0 || count(o.out, " r0 of 3") != 1 || count(o.out, " r1 of 3") != 1 || count(o.out, " r2 of 3") != 1 || count(launched(), "mpiexec -n 3") != was2+1 {
		t.Fatalf("asked on another machine: exit %d, %q %q\n%s", o.code, o.out, o.err, launched())
	}
	var there []ran
	for _, r := range p.ran("ranks.sh") {
		if r.Env == "far" {
			there = append(there, r)
		}
	}
	if len(there) != 2 || there[0].Status != "SKIP" || there[1].Status != "PASS" || len(there[0].Again) != 1 || kit.Num(there[1].Params["again"], 0) != 1 {
		t.Errorf("both records came back: %+v", there)
	}
}

func TestAGlobOfFilesRunWhole(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'v' ] = errand.Env( [ errand.Vars( { 'W': 'v' } ) ] )\nerrand.envs[ 'w' ] = errand.Env( [ errand.Vars( { 'W': 'w' } ) ] )\n")
	p.write("jobs/a.sh", "#!/bin/sh\necho a $W $@\n")
	p.write("jobs/b.sh", "#!/bin/sh\necho b $W $@\n")
	p.write("jobs/c.txt", "no\n")
	_ = os.Chmod(filepath.Join(p.root, "jobs", "c.txt"), 0o644)
	// with no environment named, the first declared
	if o := p.errand("", "run", "-q", "jobs/*", "x"); o.code != 0 || o.out != "a v x\nb v x\n" {
		t.Errorf("a glob: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("", "run", "-q", "jobs/{b,a}.sh"); o.code != 0 || sortedLines(o.out) != "a v ; b v" {
		t.Errorf("braces: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("", "run", "-q", "-j", "2", "--grid=,", "--env", "v,w", "jobs/a.sh"); o.code != 0 || sortedLines(o.out) != "a v ; a w" {
		t.Errorf("a grid said after an option that takes a value: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("", "run", "jobs"); o.code != 1 || !strings.Contains(o.err, "is a directory") {
		t.Errorf("a directory: %d %q", o.code, o.err)
	}
	if o := p.errand("", "run", "jobs/z*"); o.code != 1 || !strings.Contains(o.err, "no file that can be run") {
		t.Errorf("a glob of nothing: %d %q", o.code, o.err)
	}
	if o := p.errand("", "shell", "--batch"); o.code != 2 || !strings.Contains(o.err, "--batch is not an option of `errand shell`") {
		t.Errorf("another verb's option: %d %q", o.code, o.err)
	}
	if o := p.errand("", "frobnicate"); o.code != 2 || !strings.Contains(o.err, "unknown verb") {
		t.Errorf("no verb: %d %q", o.code, o.err)
	}
}

func TestAProjectReachedThroughALink(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy/proj' ) ] )\n")
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	link := filepath.Join(filepath.Dir(p.root), "link")
	_ = os.Symlink(p.root, link)
	cmd := p.command("run", "-q", "--env", "box", "job.sh")
	cmd.Dir, cmd.Env = link, append(append([]string{}, p.env...), "PWD="+link)
	if out, err := cmd.Output(); err != nil || string(out) != "ran\n" {
		t.Errorf("%v %q", err, out)
	}
}

func TestAUvVenvIsMadeAnew(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	tools := p.tools()
	// a uv as the recent ones are: a venv that is there is not replaced unless it is told to
	_ = os.WriteFile(filepath.Join(tools, "uv"), []byte(`#!/bin/sh
if [ "$1" = venv ]; then
  if [ -e "$2" ] && [ "$UV_VENV_CLEAR" != 1 ]; then echo "already exists" >&2; exit 2; fi
  mkdir -p "$2/bin"; echo x > "$2/pyvenv.cfg"; ln -s /usr/bin/python3 "$2/bin/python"; chmod +x "$2/bin/python"
fi
echo "$*" >> `+tools+`/asked
`), 0o755)
	p.write("req.txt", "a\n")
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'u' ] = errand.Env( [ errand.Uv( requirements = 'req.txt' ) ] )\n")
	if o := p.errand("", "run", "-q", "python", "-c", "print( 1 )"); o.code != 0 || o.out != "1\n" {
		t.Fatalf("made: %d %q %q", o.code, o.out, o.err)
	}
	p.write("req.txt", "b\n")
	if o := p.errand("", "run", "-q", "python", "-c", "print( 2 )"); o.code != 0 || o.out != "2\n" {
		t.Errorf("made anew: %d %q %q", o.code, o.out, o.err)
	}
	if got := read(t, filepath.Join(tools, "asked")); strings.Count(got, "venv .venv") != 2 {
		t.Errorf("uv was asked: %s", got)
	}
	// what is there and is no venv is not replaced
	p.write("mine/kept.txt", "mine\n")
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'u' ] = errand.Env( [ errand.Uv( path = 'mine' ) ] )\n")
	if o := p.errand("", "run", "-q", "python", "-c", "print( 3 )"); o.code == 0 || read(t, filepath.Join(p.root, "mine", "kept.txt")) != "mine\n" {
		t.Errorf("a directory that is no venv: %d %q", o.code, o.out)
	}
}
