package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/runinfo"
)

func TestTheCommandsThatLook(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.pytest()
	p.suite()
	p.write("errand-project.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "me@box", root = "copy/proj" ) ], far = True )
errand.envs[ "small" ] = errand.Env( [ errand.Vars( { "A": "1" } ) ], needs = { "cpus": 1, "ram": "16M" } )
errand.default_env = "small"
errand.default_run = [ "tests/sub/**::*" ]
`)
	p.write("w.py", "from errand import test, Param\nif p := test( 'one', n = Param( 3 ) ):\n    pass\n")
	// help, the options everybody takes before the verb
	if o := p.errand("", "--help"); o.code != 0 || !strings.Contains(o.out, "usage: errand [options] <verb>") || !strings.Contains(o.out, "\n  shell ") {
		t.Errorf("the help: %d %q", o.code, o.out)
	}
	if o := p.errand("", "run", "--help"); o.code != 0 || !strings.Contains(o.out, "--gpu-ram") {
		t.Errorf("a verb's: %d %q", o.code, o.out)
	}
	if o := p.errand(""); o.code != 0 || !strings.Contains(o.out, "verbs:") {
		t.Errorf("nothing typed: %d %q", o.code, o.out)
	}
	if o := p.errand("", "-V"); o.code != 0 || !strings.HasPrefix(o.out, "errand ") {
		t.Errorf("the version: %q", o.out)
	}
	if o := p.errand("", "--env", "small", "run", "-q", "sh", "-c", "echo $A"); o.out != "1\n" {
		t.Errorf("an option before the verb: %q %q", o.out, o.err)
	}
	// list
	o := p.errand("", "list")
	if o.code != 0 || !strings.Contains(o.out, "tests/sub/test_b.py  ( pytest )") || !strings.Contains(o.out, "tests/sub/test_b.py::test_skip") || strings.Contains(o.out, "test_a") {
		t.Errorf("list: %d\n%s%s", o.code, o.out, o.err)
	}
	if o = p.errand("", "list", "w.py::*"); !strings.Contains(o.out, "w.py::one") || !strings.Contains(o.out, "parameters: --n") {
		t.Errorf("list, with parameters: %q %q", o.out, o.err)
	}
	// envs
	if o = p.errand("", "envs"); !strings.Contains(o.out, "far") || !strings.Contains(o.out, "<- default") {
		t.Errorf("envs: %q", o.out)
	}
	var doc map[string]any
	if o = p.errand("", "envs", "--json"); json.Unmarshal([]byte(o.out), &doc) != nil || doc["envs"] == nil {
		t.Errorf("envs --json: %q", o.out)
	}
	// where the output goes too, another tree, a pattern, a ceiling on memory
	if o = p.errand("", "run", "-q", "-l", "said.txt", "echo", "x"); o.out != "x\n" || read(t, filepath.Join(p.root, "said.txt")) != "x\n" {
		t.Errorf("-l: %q", o.out)
	}
	if o = p.errand("", "run", "-q", "--out", "res/{env}/{name}-{params:n}", "sh", "-c", "touch $ERRAND_OUT_DIR/made"); o.code != 0 {
		t.Errorf("--out: %d %q", o.code, o.err)
	}
	if made, _ := filepath.Glob(filepath.Join(p.root, "res", "small", "sh-*", "made")); len(made) != 1 {
		t.Errorf("--out, a pattern: %q", made)
	}
	if o = p.errand("", "run", "-q", "--out", "tree", "-m", "1G", "true"); o.code != 0 {
		t.Errorf("--out, -m: %d %q", o.code, o.err)
	}
	if made, _ := filepath.Glob(filepath.Join(p.root, "tree", "true", "*", runinfo.ResultName)); len(made) == 0 {
		t.Errorf("--out, another tree")
	}
	// the end of several runs
	if o = p.errand("", "run", "tests/**::*"); !strings.Contains(o.err, "FAILED tests/test_a.py::*  1 failed: test_bad") {
		t.Errorf("the end: %s", o.err)
	}
	// the machines work was sent to
	if o = p.errand("", "machines"); !strings.Contains(o.out, "no machine") {
		t.Errorf("no machine yet: %q", o.out)
	}
	if o = p.errand("", "run", "-q", "--env", "box", "--batch", "sleep", "30"); o.code != 0 {
		t.Fatalf("over there: %q", o.err)
	}
	id := strings.Fields(o.out)[0]
	if o = p.errand("", "machines"); !strings.Contains(o.out, "me@box") || !strings.Contains(o.out, "work sent ") {
		t.Errorf("machines: %q", o.out)
	}
	p.errand("", "machines", "rename", "me@box", "lab")
	if o = p.errand("", "status"); !strings.Contains(o.out, "running   ( on ") || !strings.Contains(o.out, "lab  "+id[:8]+"  sleep 30  --env box") {
		t.Errorf("status, over every machine: %q %q", o.out, o.err)
	}
	if o = p.errand("", "agents"); strings.Count(o.out, " machine ") != 2 || !strings.Contains(o.out, "1 running") {
		t.Errorf("agents: %q", o.out)
	}
	if o = p.errand("", "agents", "stop", "lab"); !strings.Contains(o.out, "kept: 1 running ( --force stops them )") {
		t.Errorf("an agent that holds something stays: %q %q", o.out, o.err)
	}
	if o = p.errand("", "forget", id[:8]); o.code == 0 && strings.Contains(o.err, "it runs") {
		t.Errorf("forget: %q", o.err)
	}
	if o = p.errand("", "stop", "--mine"); o.code != 0 || !strings.Contains(o.out, id[:8]+"  stopped") {
		t.Errorf("stop --mine: %d %q %q", o.code, o.out, o.err)
	}
	p.errand("", "machines", "watch", "lab", "off")
	if o = p.errand("", "status"); !strings.Contains(o.out, "dormant, not asked: lab") {
		t.Errorf("a machine not watched: %q", o.out)
	}
	if o = p.errand("", "agents", "stop"); strings.Count(o.out, "stopped") != 2 {
		t.Errorf("agents stop: %q %q", o.out, o.err)
	}
	time.Sleep(700 * time.Millisecond)
	if o = p.errand("", "machines", "forget", "lab"); o.code != 0 {
		t.Errorf("machines forget: %q", o.err)
	}
	// caches
	cache := filepath.Join(p.root, "errand-outputs", "caches", "lib")
	_ = os.MkdirAll(filepath.Join(cache, "journals"), 0o755)
	if o = p.errand("", "caches"); o.code != 0 || !strings.Contains(o.out, "errand-outputs/caches") {
		t.Errorf("caches: %d %q %q", o.code, o.out, o.err)
	}
	// init
	other := filepath.Join(filepath.Dir(p.root), "fresh")
	_ = os.MkdirAll(filepath.Join(other, "tests"), 0o755)
	_ = os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(other, "tests", "test_x.py"), []byte("def test_x(): pass\n"), 0o644)
	cmd := p.command("init")
	cmd.Dir = other
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "wrote errand-project.py") {
		t.Errorf("init: %v %s", err, out)
	}
	if text := read(t, filepath.Join(other, "errand-project.py")); !strings.Contains(text, "# errand.runners.replace( errand.Pytest( dirs = [ 'tests' ] ) )") || !strings.Contains(text, "# errand.default_run = [ 'tests/**::*' ]") {
		t.Errorf("init wrote: %s", text)
	}
	if !strings.Contains(read(t, filepath.Join(other, ".gitignore")), "errand-outputs/") {
		t.Errorf("init said nothing to git")
	}
}

func TestAShell(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'v' ] = errand.Env( [ errand.Vars( { 'INSIDE': 'yes' } ) ] )\nerrand.default_env = 'v'\n")
	if o := p.errand("echo in $INSIDE\npwd\n", "shell"); o.code != 0 || o.out != "in yes\n"+p.root+"\n" {
		t.Fatalf("a shell that is given a script: %d %q %q", o.code, o.out, o.err)
	}
	if o := p.errand("", "shell", "ls"); o.code != 2 || !strings.Contains(o.err, "errand run --env NAME ls") {
		t.Errorf("it takes no command: %d %q", o.code, o.err)
	}
	s := p.onTerminal(90, 30, []string{"SHELL=/bin/sh", "TERM=xterm"}, "shell")
	s.waitFor("$ ")
	s.typeIn("echo got-$INSIDE; stty size\r")
	s.waitFor("got-yes")
	s.waitFor("30 90")
	s.typeIn("sleep 30\r")
	time.Sleep(300 * time.Millisecond)
	s.typeIn("\x03") // it stops what runs in it, not the shell
	s.typeIn("echo st''ill\r")
	s.waitFor("still")
	s.typeIn("exit\r")
	if code, _ := s.finish(); code != 0 {
		t.Errorf("a shell that was left: exit %d\n%s", code, s.said())
	}
	if runs, _ := filepath.Glob(filepath.Join(p.root, "errand-outputs", "runs", "*")); len(runs) != 0 {
		t.Errorf("a shell is not recorded: %q", runs)
	}
}

func TestAShellInAnAllocation(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	slurm := p.slurm()
	_ = os.WriteFile(filepath.Join(p.tools(), "srun"), []byte("#!/bin/sh\necho \"$*\" > "+slurm+"/sruned\nwhile [ \"$1\" != --pty ]; do shift; done; shift; SLURM_JOB_ID=77 exec \"$@\"\n"), 0o755)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'hpc' ] = errand.Env( [ errand.Slurm( partition = 'short' ), errand.Vars( { 'IN': 'there' } ) ] )\n")
	s := p.onTerminal(90, 30, []string{"SHELL=/bin/sh", "TERM=xterm"}, "shell", "--env", "hpc")
	s.waitFor("$ ")
	s.typeIn("echo job-$SLURM_JOB_ID $IN\r")
	s.waitFor("job-77 there")
	s.typeIn("exit\r")
	if code, _ := s.finish(); code != 0 {
		t.Errorf("exit %d\n%s", code, s.said())
	}
	if got := read(t, filepath.Join(slurm, "sruned")); !strings.Contains(got, "--partition short") || !strings.Contains(got, "--pty env IN=there") {
		t.Errorf("what Slurm was asked: %s", got)
	}
}
