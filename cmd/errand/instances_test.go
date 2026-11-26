package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Errands side by side: ERRAND_INSTANCE gives one everything of its own, here and on the machines it reaches; it is listed,
// and removed with all of it.
func TestInstancesSideBySide(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy' ) ] )\n")
	base := filepath.Dir(p.cache())
	if o := p.errand("", "run", "-q", "--env", "box", "echo", "main"); o.code != 0 || o.out != "main\n" {
		t.Fatalf("main: %d %q %q", o.code, o.out, o.err)
	}
	main := p.env
	p.env = append(append([]string{}, main...), "ERRAND_INSTANCE=try")
	if o := p.errand("", "run", "-q", "--env", "box", "echo", "try"); o.code != 0 || o.out != "try\n" {
		t.Fatalf("try, on the other machine: %d %q %q", o.code, o.out, o.err)
	}
	for _, d := range []string{filepath.Join(base, "try", "place"), filepath.Join(base, "main", "place"), filepath.Join(p.far, "cache", "try", "bin"), filepath.Join(p.far, "cache", "try", "place")} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.far, "cache", "main", "place")); err != nil {
		t.Errorf("main's own, over there: %v", err)
	}
	o := p.errand("", "instances")
	if o.code != 0 || !strings.Contains(o.out, "* try") || !strings.Contains(o.out, "  main") {
		t.Errorf("listed: %q %q", o.out, o.err)
	}
	if o = p.errand("", "instances", "remove", "main", "--yes"); o.code != 2 {
		t.Errorf("main is not removed: %d %q", o.code, o.err)
	}
	p.env = main
	if o = p.errand("", "instances", "remove", "try", "--yes"); o.code != 0 || !strings.Contains(o.out, "try removed") || !strings.Contains(o.out, "box") {
		t.Fatalf("removed: %d %q %q", o.code, o.out, o.err)
	}
	time.Sleep(200 * time.Millisecond)
	for _, d := range []string{filepath.Join(base, "try"), filepath.Join(p.far, "cache", "try")} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s is still there", d)
		}
	}
	for _, d := range []string{filepath.Join(base, "main", "place"), filepath.Join(p.far, "cache", "main", "place")} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("main is untouched: %v", err)
		}
	}
	if o = p.errand("", "run", "-q", "echo", "still"); o.code != 0 || o.out != "still\n" {
		t.Errorf("main goes on: %d %q %q", o.code, o.out, o.err)
	}
}
