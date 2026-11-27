package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/layer"
)

// An environment of errand's ( ADR 0044 ), for real: resolved, written down in the project's lock, built in the store,
// the packages one works on in an overlay, refused once changed by hand, built again from the lock on another machine.
// It fetches micromamba and some 100 MB of packages: only with ERRAND_TEST_CONDA=1.
func TestAnEnvironmentOfErrands(t *testing.T) {
	if os.Getenv("ERRAND_TEST_CONDA") == "" {
		t.Skip("fetches micromamba and packages: ERRAND_TEST_CONDA=1")
	}
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
demo = errand.Micromamba( name = "demo", python = "3.12", packages = [ "numpy" ], pip = [ "rich" ], develop = [ "libs/mylib" ] )
errand.envs[ "here" ] = errand.Env( [ demo ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), demo ] )
errand.default_env = "here"
`)
	p.write("libs/mylib/pyproject.toml", "[build-system]\nrequires = [ \"setuptools\" ]\nbuild-backend = \"setuptools.build_meta\"\n\n"+
		"[project]\nname = \"mylib\"\nversion = \"0.1\"\ndependencies = [ \"tomli-w\" ]\n")
	p.write("libs/mylib/mylib/__init__.py", "WHO = 'mine'\n")
	must := func(o outcome, what string) outcome {
		t.Helper()
		if o.code != 0 {
			t.Fatalf("%s: exit %d\n%s\n%s", what, o.code, o.out, o.err)
		}
		return o
	}
	all := func(o outcome) string { return o.out + o.err }

	o := must(p.errand("", "setup"), "the first setup")
	if !strings.Contains(all(o), "resolving on") || !strings.Contains(all(o), "written into "+layer.LockName) {
		t.Errorf("resolved, and written down: %s", all(o))
	}
	lock, err := layer.ReadLock(p.root)
	if err != nil || len(lock.Envs) != 1 {
		t.Fatalf("the lock: %v %+v", err, lock)
	}
	var res layer.Resolution
	for _, e := range lock.Envs {
		res = e.Resolutions[0]
	}
	for _, c := range res.Conda {
		if !strings.Contains(c, "#sha256:") {
			t.Errorf("a conda package not locked by its sha256: %s", c)
		}
	}
	pins := ""
	for _, pin := range res.Pip {
		pins += pin.Pin + " "
	}
	if !strings.Contains(pins, "rich==") || !strings.Contains(pins, "tomli-w==") || strings.Contains(pins, "numpy") || strings.Contains(pins, "mylib") {
		t.Errorf("the pip part: what conda gives is not pinned again, the package one works on is not fetched: %s", pins)
	}
	store := filepath.Join(p.root, "..", "cache", "main", "envs", "mamba", res.ID)
	if _, err := os.Stat(filepath.Join(store, "errand-built.json")); err != nil {
		t.Fatalf("not in the store: %v", err)
	}

	o = must(p.errand("", "run", "-q", "python", "-c", "import numpy, rich, tomli_w, mylib, sys; print( mylib.WHO, mylib.__file__, sys.prefix )"), "a run")
	if !strings.HasPrefix(o.out, "mine "+filepath.Join(p.root, "libs", "mylib")) {
		t.Errorf("the package one works on, from the project: %q", o.out)
	}
	py := strings.TrimSpace(must(p.errand("", "python"), "errand python").out)
	if !strings.Contains(py, filepath.Join("caches", "build")) || !strings.HasSuffix(py, "develop/bin/python") {
		t.Errorf("errand python: the overlay's: %q", py)
	}

	o = must(p.errand("", "setup"), "a second setup")
	if strings.Contains(all(o), "building") || strings.Contains(all(o), "resolving") {
		t.Errorf("nothing to do, done: %s", all(o))
	}

	// changed by hand: refused, and said what to declare
	if out, err := exec.Command(filepath.Join(store, "bin", "python"), "-m", "pip", "install", "-q", "six").CombinedOutput(); err != nil {
		t.Fatalf("pip install by hand: %v %s", err, out)
	}
	o = p.errand("", "run", "-q", "python", "-c", "print( 1 )")
	if o.code == 0 || !strings.Contains(all(o), "changed by hand") || !strings.Contains(all(o), `add "six==`) {
		t.Errorf("refused: exit %d %s", o.code, all(o))
	}
	o = must(p.errand("", "setup", "--force"), "built again from the lock")
	if strings.Contains(all(o), "resolving") || !strings.Contains(all(o), res.ID+"-2") {
		t.Errorf("from the lock, beside the one changed: %s", all(o))
	}
	must(p.errand("", "run", "-q", "python", "-c", "import rich"), "a run in the one built again")

	// another machine: built from the lock, nothing resolved, the lock as it was
	before := read(t, filepath.Join(p.root, layer.LockName))
	o = must(p.errand("", "setup", "--env", "box"), "on another machine")
	if strings.Contains(all(o), "resolving") || read(t, filepath.Join(p.root, layer.LockName)) != before {
		t.Errorf("built from the lock: %s", all(o))
	}
	if _, err := os.Stat(filepath.Join(p.far, "cache", "main", "envs", "mamba", res.ID, "errand-built.json")); err != nil {
		t.Errorf("not in that machine's store: %v", err)
	}

	// another declaration: another environment, and the old one out of the lock
	p.write("errand-envs.py", strings.Replace(read(t, filepath.Join(p.root, "errand-envs.py")), `pip = [ "rich" ]`, `pip = [ "rich", "six" ]`, 1))
	must(p.errand("", "run", "-q", "python", "-c", "import six"), "another declaration")
	lock, _ = layer.ReadLock(p.root)
	if len(lock.Envs) != 1 {
		t.Errorf("the declaration no longer declared stays in the lock: %d", len(lock.Envs))
	}
}
