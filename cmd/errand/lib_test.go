package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/lib"
)

// The tests of errand's Python, written with errand ( `lib/tests` ), run by errand.
func TestErrandsPython(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	src, _ := filepath.Abs("../../lib/tests")
	if out, err := exec.Command("cp", "-R", src, p.root).CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	o := p.errand("", "run", "*.py::*")
	if o.code != 0 || !strings.Contains(o.out, "PASS a project's declarations are one document") || !strings.Contains(o.out, "PASS computed once, then read back") {
		t.Errorf("exit %d\n%s\n%s", o.code, o.out, o.err)
	}
}

// What a machine needs of Python for errand: an interpreter, and nothing installed in it.
func TestThePythonIsCarried(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'e' ] = errand.Env( [ errand.Vars( { 'E': errand.__file__ } ) ] )\n")
	p.write("job.py", "import os, sys, errand\nerrand.step( 'here' )\nprint( os.environ[ 'E' ], errand.__file__, sep = '\\n' )\n")
	for i := 0; i < 2; i++ {
		o := p.errand("", "run", "-q", "job.py")
		lines := strings.Split(strings.TrimSpace(o.out), "\n")
		if o.code != 0 || len(lines) != 2 || lines[0] != lines[1] || !strings.HasPrefix(lines[0], filepath.Join(p.cache(), "binding")+"/") {
			t.Fatalf("exit %d %q %q", o.code, o.out, o.err)
		}
	}
	if made, _ := filepath.Glob(filepath.Join(p.cache(), "binding", "*")); len(made) != 1 {
		t.Errorf("%q", made)
	}
	// another package of that name on the way does not read the project
	other := filepath.Join(filepath.Dir(p.root), "other")
	_ = os.MkdirAll(filepath.Join(other, "errand"), 0o755)
	_ = os.WriteFile(filepath.Join(other, "errand", "__init__.py"), []byte("raise SystemExit( 'another errand' )\n"), 0o644)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'f' ] = errand.Env( [ errand.Vars( { 'E': '1' } ) ] )\n")
	p.env = append(p.env, "PYTHONPATH="+other)
	if o := p.errand("", "envs"); o.code != 0 || !strings.Contains(o.out, "f") {
		t.Errorf("envs: exit %d %q %q", o.code, o.out, o.err)
	}
	t.Setenv("ERRAND_CACHE_DIR", t.TempDir())
	if dir, err := lib.Dir(); err != nil || !kit.Exists(filepath.Join(dir, "errand", "include", "errand", "say.hpp")) || !kit.Exists(filepath.Join(dir, "errand", "__init__.py")) {
		t.Errorf("%q %v", dir, err)
	}
}
