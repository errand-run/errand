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

func TestAManifestThatDoesNotSayWhose(t *testing.T) {
	p := newPlace(t)
	p.write("errand-envs.py", "import errand\nerrand.envs[ \"box\" ] = errand.Env( [ errand.Ssh( host = \"box\", root = \"copy/proj\" ) ] )\n")
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	p.write("a.txt", "a\n")
	there := filepath.Join(p.far, "home", "copy", "proj")
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 {
		t.Fatalf("exit %d %q", o.code, o.err)
	}
	// as the errand of before wrote it
	m := read(t, filepath.Join(there, ".errand-copy"))
	old := strings.Replace(m, `"foreign":[],`, "", 1)
	if old == m {
		t.Fatal("no `foreign` in the manifest")
	}
	_ = os.WriteFile(filepath.Join(there, ".errand-copy"), []byte(old), 0o644)
	p.write("a.txt", "changed\n")
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code == 0 || !strings.Contains(o.err, "a.txt: not written by errand") {
		t.Errorf("exit %d %q", o.code, o.err)
	}
}

func TestACopyRemovedByHand(t *testing.T) {
	for _, alive := range []bool{true, false} {
		t.Run(fmt.Sprint("agent alive ", alive), func(t *testing.T) {
			p := newPlace(t)
			p.write("errand-envs.py", "import errand\nerrand.envs[ \"box\" ] = errand.Env( [ errand.Ssh( host = \"box\", root = \"copy/proj\" ) ] )\n")
			p.write("job.sh", "#!/bin/sh\necho ran\n")
			there := filepath.Join(p.far, "home", "copy", "proj")
			if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 {
				t.Fatalf("exit %d %q", o.code, o.err)
			}
			o := p.errand("", "run", "--env", "box", "--batch", "-q", "./job.sh")
			id := strings.Fields(o.out)[0]
			time.Sleep(1500 * time.Millisecond)
			if !alive {
				_ = exec.Command("pkill", "-f", filepath.Join(p.farCache(), "place")).Run()
				time.Sleep(300 * time.Millisecond)
			}
			if err := os.RemoveAll(there); err != nil {
				t.Fatal(err)
			}
			// the run that was not brought back yet is lost, and said so; nothing else is in the way
			if o := p.errand("", "status"); o.code != 0 || !strings.Contains(o.out, id[:8]+"  gone") {
				t.Errorf("errand status: exit %d %q %q", o.code, o.out, o.err)
			}
			if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 || o.out != "ran\n" || strings.Contains(o.err, "removed on") {
				t.Errorf("the next run: exit %d %q %q", o.code, o.out, o.err)
			}
			if _, err := os.Stat(filepath.Join(there, ".errand-copy")); err != nil {
				t.Errorf("the copy was not made again: %v", err)
			}
		})
	}
}

// ~/proj leads to a disk, and the directory there was removed: made again where the link leads
func TestACopyRemovedBehindALink(t *testing.T) {
	p := newPlace(t)
	p.write("errand-envs.py", "import errand\nerrand.envs[ \"box\" ] = errand.Env( [ errand.Ssh( host = \"box\", root = \"~/proj\" ) ] )\n")
	p.write("job.sh", "#!/bin/sh\necho ran\n")
	disk := filepath.Join(p.far, "data", "proj")
	if err := os.Symlink(disk, filepath.Join(p.far, "home", "proj")); err != nil {
		t.Fatal(err)
	}
	if o := p.errand("", "run", "--env", "box", "./job.sh"); o.code != 0 || o.out != "ran\n" {
		t.Fatalf("exit %d %q %q", o.code, o.out, o.err)
	}
	if _, err := os.Stat(filepath.Join(disk, "job.sh")); err != nil {
		t.Error(err)
	}
}
