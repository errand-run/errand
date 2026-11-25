package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/kit"
)

// A machine errand was sent to has it under a name that does not change: what a key of `authorized_keys` forces there goes
// on working from one version to the next, with nobody doing anything on that machine.
func TestAStableNameWhereErrandWasSent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no link there")
	}
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy' ) ] )\n")
	far := filepath.Join(p.far, "cache")
	name := filepath.Join(far, "bin", "errand")
	leads := func(program string) {
		t.Helper()
		hash, _ := kit.HashOfFile(program)
		for until := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if to, _ := os.Readlink(name); to == filepath.Join(hash, "errand") {
				return
			}
			if time.Now().After(until) {
				to, err := os.Readlink(name)
				t.Fatalf("the name leads to %q ( %v ), not to %s", to, err, hash)
			}
		}
	}
	forced := func() { // as sshd starts it: nothing of errand's in the surroundings but where its directory is
		t.Helper()
		c := exec.Command(name, "serve")
		c.Env = []string{"HOME=" + filepath.Join(p.far, "home"), "ERRAND_CACHE_DIR=" + far, "XDG_RUNTIME_DIR=" + filepath.Join(p.far, "queue"), "PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh"}
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("by its name: %v %s", err, out)
		}
	}
	if o := p.errand("", "run", "-q", "--env", "box", "true"); o.code != 0 {
		t.Fatalf("%d %q", o.code, o.err)
	}
	leads(built)
	forced()
	p.program = builtNext // a client of another version comes: the name follows
	if o := p.errand("", "run", "-q", "--env", "box", "true"); o.code != 0 {
		t.Fatalf("%d %q", o.code, o.err)
	}
	leads(builtNext)
	forced()
	// the copy it leads to is not one nothing uses, however long ago it served; the other is
	t.Setenv("ERRAND_CACHE_DIR", far)
	_ = exec.Command("pkill", "-f", filepath.Join(far, "place")).Run()
	time.Sleep(500 * time.Millisecond)
	long := time.Now().Add(-60 * 24 * time.Hour)
	for _, program := range []string{built, builtNext} {
		hash, _ := kit.HashOfFile(program)
		for _, f := range []string{"errand", ""} {
			_ = os.Chtimes(filepath.Join(far, "bin", hash, f), long, long)
		}
	}
	places, _ := filepath.Glob(filepath.Join(far, "place", "*"))
	a := &agent.Place{Dir: filepath.Join(places[0], "mine"), Root: places[0]}
	gone := strings.Join(a.Tidy(time.Now()), "\n")
	was, _ := kit.HashOfFile(built)
	if !strings.Contains(gone, filepath.Join("bin", was)) || !kit.Exists(name) {
		t.Errorf("tidied: %q; the name still leads somewhere: %v", gone, kit.Exists(name))
	}
	forced()
}
