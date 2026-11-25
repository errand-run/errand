// The other tools a file's entries are run by ( ADR 0036, steps 6 and 7 ): Catch2, cargo, errand's own entries; what is
// built first, what a tool needs.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/project"
	"github.com/errand-run/errand/internal/runinfo"
)

func entryNames(r runinfo.Record) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Name+" "+e.Status)
	}
	sort.Strings(out)
	return out
}

// A file declares entries when it imports errand, however it writes it -- and not when it says the word.
func TestWhatDeclaresEntries(t *testing.T) {
	pattern := regexp.MustCompile(project.Builtin()["errand"].Takes.Holds)
	for _, c := range []struct {
		head string
		is   bool
	}{{"import errand", true}, {"import time, errand", true}, {"import time,errand, os", true}, {"import os.path as p , errand as e", true}, {"import errand.local", true},
		{"from errand import test", true}, {"from  errand.entries import test", true}, {"try:\n    import time, errand\nexcept ImportError: pass", true},
		{"import errands", false}, {"import time, my_errand", false}, {"import time # , errand", false}, {"import time; x = 'errand'", false}, {"# import time, errand", false},
		{"import errand_tools, time", false}, {"from time import errand", false}} {
		if pattern.MatchString(c.head+"\n") != c.is {
			t.Errorf("%q: taken %v", c.head, !c.is)
		}
	}
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("both.py", "import time, errand\nif errand.test( 'one' ):\n    time.sleep( 0 )\n")
	if o := p.errand("", "run", "both.py::*"); o.code != 0 || !reflect.DeepEqual(entryNames(o.result), []string{"one PASS"}) {
		t.Errorf("run: exit %d %q\n%s%s", o.code, entryNames(o.result), o.out, o.err)
	}
}

func TestErrandsOwnEntries(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-project.py", `import errand
errand.envs[ 'local' ] = errand.Env( [ ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	p.write("lib.py", "def double( x ): return 2 * x\n")
	p.write("bench/w.py", `from errand import test, bench, Param, skip, step
from lib import double
import sys, os
if test( "small one" ):
    assert double( 1 ) == 2
if p := bench( "sieve", n = Param( 10, help = "upper bound" ), mode = Param( "fast", choices = [ "fast", "slow" ] ) ):
    step( "sieving", done = 0.5 )
    p.results[ "twice" ] = double( p.n )
    ( p.out_dir / "made.txt" ).write_text( p.mode + " " + " ".join( sys.argv[ 1 : ] ) )
if test( "slow bad", tags = [ "slow" ] ):
    assert 1 == 2, "one is not two"
if test( "elsewhere" ):
    skip( "only elsewhere" )
`)
	o := p.errand("", "run", "bench/w.py::*")
	if o.code != 1 || !reflect.DeepEqual(entryNames(o.result), []string{"elsewhere SKIP", "sieve PASS", "slow bad FAIL", "small one PASS"}) {
		t.Fatalf("every entry of a file, in one run: exit %d, %q\n%s%s", o.code, entryNames(o.result), o.out, o.err)
	}
	if kit.Num(o.result.Results["sieve.twice"], 0) != 20 || o.result.Error == nil || !strings.Contains(*o.result.Error, "1 failed: slow bad -- AssertionError: one is not two") {
		t.Errorf("its numbers and what failed: %+v", o.result)
	}
	if !strings.Contains(o.out+o.err, "only elsewhere") || strings.Contains(o.out+o.err, "held.py\", line") {
		t.Errorf("what each entry came to is said, and a failure without errand's own lines:\n%s%s", o.out, o.err)
	}
	// one entry, its parameters, what is left of the arguments
	o = p.errand("", "run", "bench/w.py::sieve", "--n=21", "--mode", "slow", "--other")
	if o.code != 0 || kit.Num(o.result.Results["twice"], 0) != 42 {
		t.Fatalf("one entry with its parameters: exit %d, %+v\n%s%s", o.code, o.result, o.out, o.err)
	}
	if got := read(t, filepath.Join(o.leaf, "made.txt")); got != "slow --n=21 --mode=slow --other" {
		t.Errorf("what it was given, in a directory of its own: %q", got)
	}
	if !strings.Contains(read(t, filepath.Join(o.leaf, "steps.jsonl")), "sieving") {
		t.Errorf("its steps are the run's")
	}
	if o = p.errand("", "run", "bench/w.py::sieve", "--mode=wrong"); o.code != 1 || o.result.Error == nil || !strings.Contains(*o.result.Error, "not in ['fast', 'slow']") {
		t.Errorf("a value that is not a choice: exit %d, %+v", o.code, o.result)
	}
	// a pattern, the tags written on entries, a grid of a declared parameter
	if o = p.errand("", "run", "bench/w.py::s*e*", "-q"); !reflect.DeepEqual(entryNames(o.result), []string{"sieve PASS", "small one PASS"}) && len(o.result.Entries) == 0 {
		t.Errorf("a pattern over names: %q\n%s", entryNames(o.result), o.err)
	}
	if o = p.errand("", "run", "-e", "!slow", "bench/w.py::*"); o.code != 0 || !reflect.DeepEqual(entryNames(o.result), []string{"elsewhere SKIP", "sieve PASS", "small one PASS"}) {
		t.Errorf("-e over the tags of entries: exit %d %q\n%s", o.code, entryNames(o.result), o.err)
	}
	o = p.errand("", "run", "--grid=,", "bench/w.py::sieve", "--n=1,2", "--other=a,b")
	if o.code != 0 || strings.Count(o.out+o.err, "PASS bench/w.py::sieve") != 2 || !strings.Contains(o.err, "--n=1 --other=a,b") {
		t.Errorf("a grid of a declared parameter, and of nothing else: exit %d\n%s%s", o.code, o.out, o.err)
	}
	// elsewhere: errand's Python is sent there, once
	p.env = append(p.env, "ERRAND_SAY=1")
	for i := 0; i < 2; i++ {
		if o = p.errand("", "run", "--env", "box", "bench/w.py::small one"); o.code != 0 || o.result.Env != "box" || !reflect.DeepEqual(entryNames(o.result), []string{"small one PASS"}) {
			t.Fatalf("on another machine: exit %d %+v\n%s%s", o.code, o.result, o.out, o.err)
		}
	}
	if made, _ := filepath.Glob(filepath.Join(p.far, "cache", "binding", "*", "errand", "held.py")); len(made) != 1 {
		t.Errorf("errand's Python over there, once: %q", made)
	}
	// a file that will not be read costs itself
	p.write("bench/broken.py", "from errand import test\nraise RuntimeError( 'no way' )\n")
	if o = p.errand("", "run", "bench/broken.py::*"); o.code != 1 || o.result.Error == nil || !strings.Contains(*o.result.Error, "no way") {
		t.Errorf("a file that cannot be read: exit %d %+v", o.code, o.result)
	}
	// an entry that needs several ranks asks for them, and is run again with them
	p.launchers()
	p.write("bench/mpi.py", "from errand import test\nimport os\nif test( 'many', ranks = 2 ):\n    open( 'ranks-' + os.environ[ 'ERRAND_RANKS' ], 'a' ).write( 'x' )\nif test( 'alone' ):\n    pass\n")
	o = p.errand("", "run", "bench/mpi.py::*")
	if _, err := os.Stat(filepath.Join(p.root, "ranks-2")); err != nil {
		t.Errorf("an entry of several ranks is run again with them: exit %d\n%s%s", o.code, o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(p.root, "ranks-1")); err == nil {
		t.Errorf("its body ran with too few")
	}
}

func TestACatch2Suite(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-project.py", `import errand
errand.envs[ 'local' ] = errand.Env( [ ] )
errand.runners.replace( errand.Catch2( dir = "cpp", build = [ "sh cpp/make.sh {build_dir}" ], binary = "{build_dir}/{stem}", args = [ "--durations=no" ] ) )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
errand.envs[ "carried" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ], prepare = [ ] or [ errand.Vars( { "WHERE": "here" } ) ] )
`)
	p.write("cpp/test_geo.cpp", "#include <catch2/catch_test_macros.hpp>\nTEST_CASE( \"area, of [things]\" ) {}\n")
	p.write("cpp/lib.cpp", "int x;\n")
	// what a build makes: a "binary" that lists and reports as Catch2 does, and says what it was asked
	p.write("cpp/make.sh", `set -e
mkdir -p "$1"; echo built >> times; echo "$ERRAND_BUILD_DIR" > where
[ -f cpp/fail ] && { echo "cpp/test_geo.cpp:1: no good" >&2; exit 2; }
cat > "$1/test_geo" <<'EOS'
#!/bin/sh
case "$*" in *--list-tests*) printf 'area, of [things]\nvolume\n'; exit 0;; esac
out=; for a in "$@"; do case "$a" in xml::out=*) out="${a#xml::out=}";; esac; done
echo "asked: $*"
cat > "$out" <<'X'
<Catch2TestRun><TestCase name="area, of [things]"><Expression success="false" type="REQUIRE"><Original>a == b</Original><Expanded>1 == 2</Expanded></Expression><OverallResult success="false" skips="0" durationInSeconds="0.5"/></TestCase>
<TestCase name="volume"><BenchmarkResults name="fill" samples="1"><mean value="12.5" lowerBound="1"/></BenchmarkResults><OverallResult success="true" skips="0"/></TestCase><OverallResults successes="3" failures="1" expectedFailures="0"/></Catch2TestRun>
X
exit 1
EOS
chmod +x "$1/test_geo"
`)
	o := p.errand("", "run", "cpp/**::*")
	if o.code != 1 || !reflect.DeepEqual(entryNames(o.result), []string{"area, of [things] FAIL", "volume PASS"}) {
		t.Fatalf("a suite: exit %d %+v\n%s%s", o.code, o.result, o.out, o.err)
	}
	if kit.Num(o.result.Results["fill_ns"], 0) != 12.5 || kit.Num(o.result.Results["assertions"], 0) != 4 || !strings.Contains(*o.result.Error, "a == b -- expanded: 1 == 2") {
		t.Errorf("its benchmarks, its assertions, what failed: %+v %s", o.result.Results, *o.result.Error)
	}
	if !strings.Contains(o.err, "build: sh cpp/make.sh errand-outputs/caches/build/local/main") || read(t, filepath.Join(p.root, "where")) != p.buildAt("local", "main")+"\n" {
		t.Errorf("the build is said, and knows its directory:\n%s", o.err)
	}
	// a name is a test spec, in Catch2's own words: handed as typed, patterns included
	o = p.errand("", "run", "cpp/test_geo.cpp::[math]~[slow]", "-s")
	if !strings.Contains(o.out, "asked: --reporter xml::out=") || !strings.Contains(o.out, "--durations=no [math]~[slow] -s") {
		t.Errorf("a test spec: %q %q", o.out, o.err)
	}
	// spread: names of a listing, escaped
	o = p.errand("", "run", "-j", "2", "cpp/test_geo.cpp::*")
	if !strings.Contains(o.out, `area\, of \[things\]`) || !strings.Contains(o.out, " volume") {
		t.Errorf("its entries spread over two runs:\n%s%s", o.out, o.err)
	}
	if got := strings.Count(read(t, filepath.Join(p.root, "times")), "built"); got != 3 {
		t.Errorf("built once per command, whatever its runs: %d times for 3 commands", got)
	}
	// on another machine: built there
	if o = p.errand("", "run", "--env", "box", "cpp/test_geo.cpp::volume"); len(o.result.Entries) != 2 || o.result.Env != "box" {
		t.Fatalf("elsewhere: exit %d %+v\n%s%s", o.code, o.result, o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(p.far, "home", "copy", "proj", "errand-outputs", "caches", "build", "box", "main", "test_geo")); err != nil {
		t.Errorf("built where it runs: %v", err)
	}
	// prepared here, run there: what was built is carried
	if o = p.errand("", "run", "--env", "carried", "cpp/test_geo.cpp::volume"); len(o.result.Entries) != 2 {
		t.Fatalf("prepared in another place: exit %d %+v\n%s%s", o.code, o.result, o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(p.buildAt("carried", "main"), "test_geo")); err != nil {
		t.Errorf("built where it is prepared: %v", err)
	}
	// a build that fails fails the command, before anything runs
	p.write("cpp/fail", "")
	before := o.leaf
	if o = p.errand("", "run", "cpp/**::*"); o.code != 1 || !strings.Contains(o.err, "the build failed ( sh cpp/make.sh errand-outputs/caches/build/local/main )") || !strings.Contains(o.err, "no good") || o.leaf != before {
		t.Errorf("a build that fails: exit %d\n%s", o.code, o.err)
	}
	// no binary: what to say to the runner
	p.write("errand-project.py", "import errand\nerrand.runners.replace( errand.Catch2( dir = 'cpp' ) )\n")
	if o = p.errand("", "run", "cpp/test_geo.cpp::volume"); o.code == 0 || !strings.Contains(o.out+o.err, "no binary at cpp/test_geo: tell Catch2 how to build it") {
		t.Errorf("no binary: exit %d\n%s%s", o.code, o.out, o.err)
	}
}

func TestACargoCrate(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("no cargo here")
	}
	p := newPlace(t)
	p.write("rust/Cargo.toml", "[package]\nname = \"tiny\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	p.write("rust/src/lib.rs", "#[cfg(test)]\nmod tests {\n    #[test] fn alpha() {}\n    #[test] fn alpha_two() {}\n    #[test] #[ignore] fn beta() {}\n}\n")
	p.write("rust/src/other.rs", "pub fn x() {}\n")
	p.write("rust/tests/whole.rs", "#[test] fn bad() { assert_eq!(1, 2); }\n")
	o := p.errand("", "run", "rust/src/**::*")
	if o.code != 0 || !reflect.DeepEqual(entryNames(o.result), []string{"tests::alpha PASS", "tests::alpha_two PASS", "tests::beta SKIP"}) {
		t.Fatalf("the library's tests: exit %d %q\n%s%s", o.code, entryNames(o.result), o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(p.buildAt("local", "main"), "target")); err != nil {
		t.Errorf("built in this environment's own directory: %v", err)
	}
	if o = p.errand("", "run", "rust/tests/whole.rs::*"); o.code == 0 || !reflect.DeepEqual(entryNames(o.result), []string{"bad FAIL"}) {
		t.Errorf("an integration test: exit %d %q\n%s", o.code, entryNames(o.result), o.err)
	}
	// a name is cargo's filter; a pattern is what the listing matches, exactly
	if o = p.errand("", "run", "rust/src/lib.rs::alpha"); !reflect.DeepEqual(entryNames(o.result), []string{"tests::alpha PASS", "tests::alpha_two PASS"}) {
		t.Errorf("a filter: %q\n%s", entryNames(o.result), o.err)
	}
	if o = p.errand("", "run", "rust/src/lib.rs::al?ha"); !reflect.DeepEqual(entryNames(o.result), []string{"tests::alpha PASS"}) {
		t.Errorf("a pattern: %q\n%s", entryNames(o.result), o.err)
	}
	if o = p.errand("", "run", "rust/src/other.rs::*"); o.code != 1 || !strings.Contains(o.err, "no entry runner takes it") {
		t.Errorf("a source that is no target's root: exit %d %s", o.code, o.err)
	}
}

func TestWhatAToolNeeds(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	dir := filepath.Join(filepath.Dir(p.root), "snake")
	_ = os.MkdirAll(dir, 0o755)
	// an interpreter that has no pytest until pip is asked for it
	_ = os.WriteFile(filepath.Join(dir, "snake"), []byte(`#!/bin/sh
here="`+dir+`"
case "$*" in
"-c import pytest") [ -f "$here/has" ];;
"-c import os, sys"*) cat "$here/own";;
"-m pip install pytest<9") echo asked >> "$here/pip"; touch "$here/has";;
*"-m pytest"*) [ -f "$here/has" ] && echo "ran" || { echo "No module named pytest" >&2; exit 1; };;
esac
`), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "own"), []byte("False\n"), 0o644)
	p.write("errand-project.py", `import errand
errand.runners.replace( errand.Pytest( python = "`+filepath.Join(dir, "snake")+`", spec = "pytest<9" ) )
`)
	p.write("test_a.py", "def test_x(): pass\n")
	o := p.errand("", "run", "test_a.py::*")
	if o.code != 1 || !strings.Contains(o.out+o.err, "it is the system interpreter") {
		t.Fatalf("the system's interpreter is not installed into: exit %d\n%s%s", o.code, o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pip")); err == nil {
		t.Fatalf("it was installed into")
	}
	_ = os.WriteFile(filepath.Join(dir, "own"), []byte("True\n"), 0o644)
	for i := 0; i < 2; i++ {
		if o = p.errand("", "run", "test_a.py::*"); o.code != 0 || !strings.Contains(o.out, "ran") {
			t.Fatalf("installed, then run: exit %d\n%s%s", o.code, o.out, o.err)
		}
	}
	if strings.Count(read(t, filepath.Join(dir, "pip")), "asked") != 1 {
		t.Errorf("installed more than once")
	}
}

func TestWhatARunFindsAroundIt(t *testing.T) {
	p := newPlace(t)
	p.write("src/mine.py", "X = 1\n")
	p.write("say.sh", "#!/bin/sh\neval \"$ERRAND_STEPS_SH\"\nstep here done=0.5\nresult n=3\necho \"caches $ERRAND_CACHES\"\necho \"build $ERRAND_BUILD_DIR\"\npython3 -c 'import mine, errand; print( \"errand\", errand.__file__ )'\n")
	_ = os.Chmod(filepath.Join(p.root, "say.sh"), 0o755)
	o := p.errand("", "run", "-q", "./say.sh")
	caches := filepath.Join(p.root, "errand-outputs", "caches")
	if o.code != 0 || !strings.Contains(o.out, "caches "+caches+"\n") || !strings.Contains(o.out, "build "+p.buildAt("local", "main")+"\n") {
		t.Fatalf("what a run is handed: exit %d\n%s%s", o.code, o.out, o.err)
	}
	if !strings.Contains(o.out, "errand "+filepath.Join(p.cache(), "binding")) {
		t.Errorf("errand's Python, and the project's sources, can be imported:\n%s%s", o.out, o.err)
	}
	if kit.Num(o.result.Results["n"], 0) != 3 || !strings.Contains(read(t, filepath.Join(o.leaf, "steps.jsonl")), `"text": "here"`) {
		t.Errorf("the shell's functions: %+v", o.result.Results)
	}
	if _, err := os.Stat(filepath.Join(caches, "CACHEDIR.TAG")); err != nil {
		t.Errorf("the caches' directory is marked as one: %v", err)
	}
	if found, _ := filepath.Glob(filepath.Join(p.cache(), "caches", "*.json")); len(found) != 1 {
		t.Errorf("and written down in the machine's registry: %q", found)
	}
}

func (p *scene) buildAt(env, name string) string {
	return filepath.Join(p.root, "errand-outputs", "caches", "build", env, name)
}
