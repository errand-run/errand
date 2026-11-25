package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/errand-run/errand/internal/caches"
)

// Where a project builds: one place per environment and name, found again, seen and tidied with the caches ( ADR 0039 )
func TestBuildDirectories(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "here" ] = errand.Env( [ ] )
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
errand.default_env = "here"
`)
	p.write("b.py", "import errand, os, sys\nd = errand.build_dir( *sys.argv[ 1: ] )\n( d / 'made' ).write_text( 'x' )\nprint( d, os.environ[ 'ERRAND_BUILD_DIR' ], sep = '\\n' )\n")
	builds := filepath.Join(p.root, "errand-outputs", "caches", "build")
	line := func(o outcome) string {
		t.Helper()
		if o.code != 0 {
			t.Fatalf("exit %d %q %q", o.code, o.out, o.err)
		}
		return strings.SplitN(o.out, "\n", 2)[0]
	}
	// the same by hand and in a run
	main := filepath.Join(builds, "here", "main")
	if got := line(p.errand("", "build-dir")); got != main {
		t.Errorf("by hand: %s, expected %s", got, main)
	}
	if o := p.errand("", "run", "b.py"); line(o) != main || !strings.HasSuffix(o.out, main+"\n") {
		t.Errorf("in a run: %q", o.out)
	}
	if got := line(p.errand("", "run", "b.py", "cmake")); got != filepath.Join(builds, "here", "cmake") || line(p.errand("", "build-dir", "cmake")) != got {
		t.Errorf("by its name: %s", got)
	}
	if got := line(p.errand("", "run", "sh", "-c", `"$ERRAND" build-dir sh`)); got != filepath.Join(builds, "here", "sh") {
		t.Errorf("from a shell: %s", got)
	}
	// elsewhere, its own
	far := filepath.Join(p.far, "home", "copy", "proj", "errand-outputs", "caches", "build", "box")
	if got := line(p.errand("", "run", "--env", "box", "b.py")); got != filepath.Join(far, "main") || line(p.errand("", "build-dir", "--env", "box")) != got {
		t.Errorf("elsewhere: %s", got)
	}
	// nothing of it crosses, and it is no obstacle to a push
	if _, err := os.Stat(filepath.Join(builds, "box")); err == nil {
		t.Error("what was built elsewhere came here")
	}
	if o := p.errand("", "run", "--env", "box", "true"); o.code != 0 || strings.Contains(o.err, "made") {
		t.Errorf("the next push: %d %q", o.code, o.err)
	}
	for _, bad := range []string{"..", "../../up"} {
		if got := line(p.errand("", "build-dir", bad)); filepath.Dir(got) != filepath.Join(builds, "here") {
			t.Errorf("%s: %s", bad, got)
		}
	}
	// seen with the caches
	if o := p.errand("", "caches"); !strings.Contains(o.out, "build: ") {
		t.Errorf("errand caches: %q", o.out)
	}

	if o := p.errand("", "check", "here"); o.code != 0 || !strings.Contains(o.out, "build director") {
		t.Errorf("errand check: %d %q %q", o.code, o.out, o.err)
	}

	// what goes first: what is made again by itself, after a while; the rest stays
	dir := filepath.Join(builds, "here", "cmake")
	object := "\x7fELF\x02\x01" + strings.Repeat("o", 1000)
	for name, text := range map[string]string{"lib.so": "\x7fELF\x02\x01so", "a.o": object, "sub/deep/b.o": object, "mesh.obj": "v 0 0 0\n", "c.gch": "gch", "notes.o": "not an object"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	there := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	purge := func(days float64) map[string]any {
		t.Helper()
		r := caches.PurgeCache(builds, caches.Clock()+days*86400, false, 1, "", nil)
		if r == nil {
			t.Fatal("not purged")
		}
		return r
	}
	if r := purge(2); r["thinned"] != 0 || !there("a.o") {
		t.Errorf("too soon: %v", r)
	}
	if r := purge(20); r["thinned"] != 1 || there("a.o") || there("sub/deep/b.o") || there("c.gch") || !there("lib.so") || !there("mesh.obj") || !there("notes.o") || !there("made") {
		t.Errorf("after a while: %v, a.o %v b.o %v lib.so %v mesh.obj %v notes.o %v", r, there("a.o"), there("sub/deep/b.o"), there("lib.so"), there("mesh.obj"), there("notes.o"))
	}
	if r := purge(21); r["thinned"] != 0 || r["freed"] != int64(0) {
		t.Errorf("and again: %v", r)
	}
	// and nothing goes whole by age alone
	if r := purge(400); len(r["removed"].([]string)) != 0 || !there("lib.so") {
		t.Errorf("by age alone: %v", r)
	}
	unused := 30.0
	if r := caches.PurgeCache(builds, caches.Clock()+100*86400, false, 1, "", &unused); len(r["removed"].([]string)) == 0 || there("lib.so") {
		t.Errorf("by hand: %v", r)
	}
}

// What a library declares goes the same way
func TestIntermediatesOfACache(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for _, f := range []string{"k/lib.so", "k/kernel.o", "other.o"} {
		_ = os.MkdirAll(filepath.Dir(at(f)), 0o755)
		_ = os.WriteFile(at(f), []byte("x"), 0o644)
	}
	_ = os.MkdirAll(at("errand-cache-journal"), 0o755)
	write := func(line string) {
		f, _ := os.OpenFile(at("errand-cache-journal/h-1-1.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		_, _ = f.WriteString(line + "\n")
		f.Close()
	}
	now := caches.NowIso(caches.Clock())
	write(`{"op": "create", "entry": "k", "product": ["k"], "intermediate": ["k/kernel.o", "../other.o"], "size": 2, "at": "` + now + `"}`)
	purge := func(days float64) { caches.PurgeCache(root, caches.Clock()+days*86400, false, 1, "", nil) }
	there := func(rel string) bool { _, err := os.Stat(at(rel)); return err == nil }
	if purge(1); !there("k/kernel.o") {
		t.Error("too soon")
	}
	if purge(20); there("k/kernel.o") || !there("k/lib.so") || !there("other.o") {
		t.Errorf("after a while: %v %v %v", there("k/kernel.o"), there("k/lib.so"), there("other.o"))
	}
}
