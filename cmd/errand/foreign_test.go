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

// What errand did not write over there is never removed nor replaced by a push ( ADR 0038 )
func TestAPushNeverLosesWhatIsNotErrands(t *testing.T) {
	p := newPlace(t)
	p.write("errand-envs.py", `import errand
errand.envs[ "box" ] = errand.Env( [ errand.Ssh( host = "box", root = "copy/proj" ) ] )
`)
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	p.write("old.txt", "mine\n")
	p.write("dir/a.txt", "a\n")
	there := filepath.Join(p.far, "home", "copy", "proj")
	at := func(rel string) string { return filepath.Join(there, rel) }
	put := func(rel, text string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(at(rel)), 0o755)
		if err := os.WriteFile(at(rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gone := func(rel string) bool { _, err := os.Lstat(at(rel)); return err != nil }
	walked := func() { // the agent over there gone: the next one looks at the whole copy
		_ = exec.Command("pkill", "-f", filepath.Join(p.farCache(), "place")).Run()
		time.Sleep(300 * time.Millisecond)
	}
	run := func(more ...string) outcome {
		t.Helper()
		return p.errand("", append(append([]string{"run", "--env", "box"}, more...), "./job.sh")...)
	}

	// a first push, where there is something already: nothing is touched, and what is in the way is named
	put("old.txt", "somebody's\n")
	put("notes/theirs.txt", "somebody's too\n")
	o := run()
	if o.code == 0 || !strings.Contains(o.err, "old.txt") || !strings.Contains(o.err, "notes/theirs.txt") || !strings.Contains(o.err, "--force") || strings.Contains(o.out, "ran") {
		t.Fatalf("a first push onto files that are there: exit %d, %q %q", o.code, o.out, o.err)
	}
	if read(t, at("old.txt")) != "somebody's\n" || read(t, at("notes/theirs.txt")) != "somebody's too\n" || !gone("job.sh") || !gone("dir") {
		t.Fatalf("a push that stopped touched something")
	}
	// removed by hand, or the same as here: nothing would be lost, it goes on
	_ = os.RemoveAll(at("notes"))
	put("old.txt", "mine\n")
	if o = run(); o.code != 0 || !strings.Contains(o.out, "ran") {
		t.Fatalf("once nothing is in the way: exit %d, %q %q", o.code, o.out, o.err)
	}
	// what was there before errand is not errand's for being the same: not to replace, not to remove, walked again or not
	for _, again := range []bool{false, true} {
		if again {
			walked()
		}
		p.write("old.txt", "mine, changed\n")
		if o = run(); o.code == 0 || !strings.Contains(o.err, "old.txt: not written by errand") || read(t, at("old.txt")) != "mine\n" {
			t.Fatalf("what was there before, replaced: exit %d, %q", o.code, o.err)
		}
		_ = os.Remove(filepath.Join(p.root, "old.txt"))
		if o = run(); o.code == 0 || !strings.Contains(o.err, "old.txt: not written by errand") || gone("old.txt") {
			t.Fatalf("what was there before, removed: exit %d, %q", o.code, o.err)
		}
		p.write("old.txt", "mine\n")
		if o = run(); o.code != 0 {
			t.Fatalf("the same again: exit %d, %q", o.code, o.err)
		}
	}
	_ = os.Remove(at("old.txt"))
	_ = os.Remove(filepath.Join(p.root, "old.txt"))

	// what errand wrote goes without a word
	_ = os.RemoveAll(filepath.Join(p.root, "dir"))
	if o = run(); o.code != 0 || !gone("dir") || strings.Contains(o.err, "a.txt") {
		t.Fatalf("what errand had written there: exit %d, %q", o.code, o.err)
	}

	// a file that appeared
	walked()
	put("appeared.txt", "a run's\n")
	if o = run(); o.code == 0 || !strings.Contains(o.err, "appeared.txt: not written by errand") || gone("appeared.txt") {
		t.Fatalf("a file that appeared: exit %d, %q", o.code, o.err)
	}
	if o = run(); o.code == 0 || gone("appeared.txt") { // and again
		t.Fatalf("asked again: exit %d, %q", o.code, o.err)
	}
	_ = os.Remove(at("appeared.txt"))

	// a file errand wrote, changed there since; seen without a walk when the push replaces it
	p.write("data.txt", "one\n")
	if o = run(); o.code != 0 {
		t.Fatalf("exit %d, %q", o.code, o.err)
	}
	put("data.txt", "theirs, changed\n")
	p.write("data.txt", "two two\n")
	if o = run(); o.code == 0 || !strings.Contains(o.err, "data.txt: changed there") || read(t, at("data.txt")) != "theirs, changed\n" {
		t.Fatalf("a file changed there: exit %d, %q", o.code, o.err)
	}
	// a file that is new here, where one is there the agent did not know of
	put("fresh.txt", "theirs\n")
	p.write("fresh.txt", "ours\n")
	if o = run(); o.code == 0 || !strings.Contains(o.err, "fresh.txt: not written") || read(t, at("fresh.txt")) != "theirs\n" {
		t.Fatalf("a new file, there already: exit %d, %q", o.code, o.err)
	}
	// forced: moved aside, said, and the run goes
	if o = run("--force"); o.code != 0 || !strings.Contains(o.err, "moved aside") || read(t, at("fresh.txt")) != "ours\n" || read(t, at("data.txt")) != "two two\n" {
		t.Fatalf("forced: exit %d, %q", o.code, o.err)
	}
	kept, _ := filepath.Glob(filepath.Join(there, ".errand-copy-kept", "*", "data.txt"))
	if len(kept) != 1 || read(t, kept[0]) != "theirs, changed\n" {
		t.Errorf("what was moved aside: %v", kept)
	}
	if o = run(); o.code != 0 || strings.Contains(o.err, "aside") {
		t.Errorf("afterwards: exit %d, %q", o.code, o.err)
	}
}

// A copy in a copy: what the one's push wrote is errand's for the other's
func TestACopyInACopyIsErrands(t *testing.T) {
	p := newPlace(t)
	env := func(root string) string {
		return "import errand\nerrand.envs[ \"box\" ] = errand.Env( [ errand.Ssh( host = \"box\", root = \"" + root + "\" ) ] )\n"
	}
	whole := p.root
	p.write("errand-envs.py", env("copy"))
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	p.write("sub/errand-envs.py", env("copy/sub"))
	p.write("sub/job.sh", "#!/bin/sh\necho sub\n")
	p.write("sub/extra.txt", "x\n")
	p.root = filepath.Join(whole, "sub")
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 {
		t.Fatalf("the inner one: exit %d, %q", o.code, o.err)
	}
	_ = os.Remove(filepath.Join(p.root, "extra.txt")) // errand wrote it there, by the other's push
	p.root = whole
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 || strings.Contains(o.err, "errand-copy") {
		t.Fatalf("the outer one: exit %d, %q", o.code, o.err)
	}
	if _, err := os.Stat(filepath.Join(p.far, "home", "copy", "sub", ".errand-copy")); err != nil {
		t.Errorf("the inner copy's records are not there any more")
	}
	if _, err := os.Stat(filepath.Join(p.far, "home", "copy", "sub", "extra.txt")); err == nil {
		t.Errorf("what errand wrote and the project has no more is still there")
	}
}

// What errand wrote, changed over there and brought back, is no change behind its back any more
func TestWhatIsBroughtBackIsNoObstacle(t *testing.T) {
	p := newPlace(t)
	p.write("errand-envs.py", "import errand\nerrand.envs[ \"box\" ] = errand.Env( [ errand.Ssh( host = \"box\", root = \"copy/proj\" ) ] )\n")
	p.write("job.sh", "#!/bin/sh\n[ -n \"$1\" ] && echo \"$1\" > data/a.txt; echo ran\n")
	p.write("data/a.txt", "one\n")
	run := func(pull bool, arg string) outcome {
		t.Helper()
		words := []string{"run", "--env", "box"}
		if pull {
			words = append(words, "--pull", "data")
		}
		return p.errand("", append(words, "./job.sh", arg)...)
	}
	if o := run(true, "changed there"); o.code != 0 || read(t, filepath.Join(p.root, "data/a.txt")) != "changed there\n" {
		t.Fatalf("exit %d, %q", o.code, o.err)
	}
	p.write("data/a.txt", "changed here since\n")
	if o := run(false, ""); o.code != 0 {
		t.Fatalf("after it was brought back: exit %d, %q", o.code, o.err)
	}
	if o := run(false, "changed again"); o.code != 0 {
		t.Fatalf("exit %d, %q", o.code, o.err)
	}
	p.write("data/a.txt", "here too\n")
	if o := run(false, ""); o.code == 0 || !strings.Contains(o.err, "data/a.txt: changed there") {
		t.Fatalf("and when it was not: exit %d, %q", o.code, o.err)
	}
}

// A push that is cut, its agent killed, leaves what it wrote errand's
func TestAPushThatIsCut(t *testing.T) {
	p := newPlace(t)
	p.write("errand-envs.py", "import errand\nerrand.envs[ \"box\" ] = errand.Env( [ errand.Ssh( host = \"box\", root = \"copy/proj\" ) ] )\n")
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	big := strings.Repeat("0123456789abcdef", 1<<12)
	for i := 0; i < 3000; i++ {
		p.write(fmt.Sprintf("many/%04d.txt", i), fmt.Sprint(i, big))
	}
	there := filepath.Join(p.far, "home", "copy", "proj")
	cmd := p.command("run", "--env", "box", "./job.sh")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(there, "many", "0200.txt")); err == nil {
			break
		}
	}
	_ = exec.Command("pkill", "-9", "-f", filepath.Join(p.farCache(), "place")).Run()
	_ = cmd.Wait()
	_ = os.Remove(filepath.Join(there, ".errand-copy.lock"))
	if _, err := os.Stat(filepath.Join(there, ".errand-copy")); err == nil {
		t.Skip("it was through before it was cut")
	}
	if _, err := os.Stat(filepath.Join(there, "many", "0200.txt")); err != nil {
		t.Fatal("nothing was written before it was cut")
	}
	p.write("many/0100.txt", "changed here\n")
	_ = os.Remove(filepath.Join(p.root, "many", "0101.txt"))
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 {
		t.Fatalf("the next push: exit %d, %q", o.code, o.err)
	}
	if _, err := os.Stat(filepath.Join(there, "many", "0101.txt")); err == nil || read(t, filepath.Join(there, "many", "0100.txt")) != "changed here\n" {
		t.Errorf("what errand wrote was not errand's")
	}
}
