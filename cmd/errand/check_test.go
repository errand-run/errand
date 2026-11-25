package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
errand.envs[ "same" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ), errand.Vars( { "A": "1" } ) ] )
`)
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	p.write("a.txt", "a\n")
	there := filepath.Join(p.far, "home", "copy", "proj")
	at := func(rel string) string { return filepath.Join(there, rel) }
	far := filepath.Join(p.far, "cache")
	check := func(code int, words ...string) string {
		t.Helper()
		o := p.errand("", append([]string{"check"}, words...)...)
		if o.code != code {
			t.Fatalf("errand check %v: exit %d, expected %d\n%s\n%s", words, o.code, code, o.out, o.err)
		}
		return o.out
	}
	says := func(out string, want bool, text string) {
		t.Helper()
		if strings.Contains(out, text) != want {
			t.Errorf("%q said: %v, in\n%s", text, !want, out)
		}
	}
	var leaves []string
	for i := 0; i < 2; i++ {
		if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 {
			t.Fatalf("exit %d %q", o.code, o.err)
		} else {
			leaves = append(leaves, o.leaf)
		}
		time.Sleep(1100 * time.Millisecond)
	}
	// all is well: said, and nothing touched
	before := read(t, at(".errand-copy"))
	out := check(0)
	says(out, true, "the next push has nothing to do")
	says(out, false, "✗")
	if strings.Count(out, "the next push") != 1 {
		t.Errorf("a copy two environments share, said twice:\n%s", out)
	}

	// what the next push would do, and would not
	p.write("a.txt", "changed\n")
	p.write("new.txt", "new\n")
	says(check(0, "box"), true, "sends 2 files")
	_ = os.WriteFile(at("a.txt"), []byte("theirs\n"), 0o644)
	_ = os.WriteFile(at("stray.txt"), []byte("x\n"), 0o644)
	out = check(1, "box")
	says(out, true, "the next push stops, 2 files there would be lost")
	says(out, true, "a.txt: changed there since errand wrote it")
	says(out, true, "stray.txt: not written by errand")
	if read(t, at(".errand-copy")) != before || read(t, at("a.txt")) != "theirs\n" {
		t.Error("looking changed something")
	}
	says(check(1, "box", "--fix"), true, "stray.txt") // and fixing touches no file
	if read(t, at("stray.txt")) != "x\n" || read(t, at("a.txt")) != "theirs\n" {
		t.Error("--fix touched a file errand did not write")
	}
	_ = os.Remove(at("stray.txt"))
	_ = os.WriteFile(at("a.txt"), []byte("changed\n"), 0o644)

	// a lock its process died over: seen, and no push waits for it
	lock := at(".errand-copy.lock")
	dead := exec.Command("sleep", "0")
	_ = dead.Run()
	hold := func() {
		name, _ := os.ReadDir(filepath.Join(far, "place"))
		_ = os.Mkdir(lock, 0o755)
		_ = os.WriteFile(filepath.Join(lock, "owner.json"), []byte(fmt.Sprintf(`{"host": %q, "pid": %d, "since": "1"}`, name[0].Name(), dead.Process.Pid)), 0o644)
	}
	hold()
	says(check(1, "box"), true, "a push's lock that is nobody's")
	says(check(0, "box", "--fix"), true, "-- removed")
	if _, err := os.Stat(lock); err == nil {
		t.Error("the lock is still there")
	}
	hold()
	start := time.Now()
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 || time.Since(start) > 10*time.Second {
		t.Fatalf("a push, past a lock that is nobody's: exit %d after %v, %q", o.code, time.Since(start), o.err)
	}

	// a run removed here: there only, brought back when it is asked
	if err := os.RemoveAll(leaves[0]); err != nil {
		t.Fatal(err)
	}
	out = check(0, "box", "--fix")
	says(out, true, "1 run there, not here")
	if _, err := os.Stat(leaves[0]); err == nil {
		t.Error("--fix brought a run back")
	}
	says(check(0, "box", "--bring"), true, "1 brought back")
	if _, err := os.Stat(filepath.Join(leaves[0], "result.json")); err != nil {
		t.Errorf("not brought back: %v", err)
	}
	says(check(0, "box"), false, "not here")

	// what a batch run is to bring back is not said to be lost
	o := p.errand("", "run", "--env", "box", "--batch", "-q", "./job.sh")
	time.Sleep(1500 * time.Millisecond)
	says(check(0, "box"), false, "not here")
	p.errand("", "attach", strings.Fields(o.out)[0])

	// a run's directory removed there by hand
	rel, _ := filepath.Rel(filepath.Join(p.root, "errand-outputs"), leaves[1])
	_ = os.RemoveAll(at(filepath.Join("errand-outputs", rel)))
	says(check(1, "box"), true, "1 run here said to be there as well")
	says(check(0, "box", "--fix"), true, "said to be here only")
	says(check(0, "box"), false, "said to be there as well")

	// the whole copy removed by hand
	_ = os.RemoveAll(there)
	out = check(1, "box")
	says(out, true, "a copy that is there no more")
	says(out, true, "the next push sends")
	if _, err := os.Stat(there); err == nil {
		t.Error("looking made the copy again")
	}
	says(check(0, "box", "--fix"), true, "its record removed")
	says(check(0, "box"), false, "✗")

	// a manifest of before
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 {
		t.Fatalf("exit %d %q", o.code, o.err)
	}
	_ = os.WriteFile(at(".errand-copy"), []byte(strings.Replace(read(t, at(".errand-copy")), `"foreign":[],`, "", 1)), 0o644)
	says(check(1, "box"), true, "does not say whose its files are")

	says(check(1, "box", "--json"), true, `"kind": "copy"`)
	if o := p.errand("", "check", "nobody"); o.code != 2 {
		t.Errorf("a machine that is none: %d", o.code)
	}
	// from elsewhere than the project: the copy says where it comes from
	p.root = t.TempDir()
	says(p.errand("", "check", "box").out, true, "/proj → copy/proj")
}
