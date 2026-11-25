package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A place may say where errand keeps what is its own there
func TestAPlaceHasItsOwnCache(t *testing.T) {
	p := newPlace(t)
	p.write("errand-envs.py", `import errand
def box( **kw ): return errand.Env( [ errand.Ssh( host = "box", root = "copy/proj", **kw ) ] )
errand.envs[ "work" ] = box( cache = "$HOME/../scratch/my errand" )
errand.envs[ "tilde" ] = box( cache = "~/kept/errand" )
errand.envs[ "plain" ] = box()
errand.envs[ "unset" ] = box( cache = "$NOT_A_VARIABLE_THERE/errand" )
errand.envs[ "sealed" ] = box( cache = "/dev/null/errand" )
errand.envs[ "odd" ] = box( cache = "$( touch pwned )/errand" )
`)
	p.write("job.sh", "#!/bin/sh\necho \"cache $ERRAND_CACHE_DIR\"\n")
	scratch := filepath.Join(p.far, "scratch", "my errand")
	o := p.errand("", "run", "--env", "work", "./job.sh")
	if real, _ := filepath.EvalSymlinks(scratch); o.code != 0 || !strings.Contains(o.out, "cache "+real) {
		t.Fatalf("in its own directory: exit %d, %q %q", o.code, o.out, o.err)
	}
	for _, d := range []string{"bin", "place", "copies"} {
		if _, err := os.Stat(filepath.Join(scratch, d)); err != nil {
			t.Errorf("not in the directory declared: %v", err)
		}
	}
	if left, _ := os.ReadDir(filepath.Join(p.far, "cache")); len(left) != 0 {
		t.Errorf("errand wrote in the machine's own directory as well: %v", left)
	}
	// what is asked of the machine afterwards is asked of that agent
	if o = p.errand("", "run", "--env", "work", "--batch", "-q", "sh", "-c", "sleep 3"); o.code != 0 {
		t.Fatalf("--batch: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "sleep 3") || !strings.Contains(o.out, "running") {
		t.Errorf("errand status: %q %q", o.out, o.err)
	}
	// other directories on the same machine are other places, and one does not hide the other
	if o = p.errand("", "run", "--env", "tilde", "./job.sh"); o.code != 0 || !strings.Contains(o.out, "/home/kept/errand") {
		t.Errorf("~: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "run", "--env", "plain", "./job.sh"); o.code != 0 || !strings.Contains(o.out, "far/cache") {
		t.Errorf("as the machine says: exit %d, %q %q", o.code, o.out, o.err)
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, "sleep 3") {
		t.Errorf("errand status, after work sent to the same machine's other places: %q %q", o.out, o.err)
	}
	if o = p.errand("", "run", "--env", "unset", "./job.sh"); o.code == 0 || !strings.Contains(o.err, "NOT_A_VARIABLE_THERE") {
		t.Errorf("a variable that is not there: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run", "--env", "sealed", "./job.sh"); o.code == 0 || !strings.Contains(o.err, "cannot be made or written") {
		t.Errorf("a directory that cannot be: exit %d, %q", o.code, o.err)
	}
	if o = p.errand("", "run", "--env", "odd", "./job.sh"); o.code == 0 || !strings.Contains(o.err, "nothing else begins with") {
		t.Errorf("a command in a path: exit %d, %q", o.code, o.err)
	}
}
