package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/errand-run/errand/internal/agent"
	"github.com/errand-run/errand/internal/filesync"
	"github.com/errand-run/errand/internal/kit"
	"github.com/errand-run/errand/internal/reach"
)

func TestAnAgentStartedAgain(t *testing.T) {
	p := newPlace(t)
	p.errand("", "run", "-q", "true")
	version := func() string {
		files, _ := filepath.Glob(filepath.Join(p.cache(), "place", "*", "*", "version"))
		if len(files) != 1 {
			return ""
		}
		data, _ := os.ReadFile(files[0])
		return string(data)
	}
	was := version()
	if o := p.errand("", "agents", "restart"); o.code != 0 || !strings.Contains(o.out, "stopped") {
		t.Fatalf("restart: %d %q %q", o.code, o.out, o.err)
	}
	for until := time.Now().Add(10 * time.Second); version() == was || version() == ""; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("no other agent came: %q", version())
		}
	}
}

func TestWhatErrandLeavesIsTidied(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ERRAND_CACHE_DIR", dir)
	base := filepath.Join(dir, "main") // ( the instance in use )
	long := time.Now().Add(-60 * 24 * time.Hour)
	make := func(rel string, at time.Time) string {
		path := filepath.Join(base, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte("x"), 0o644)
		_ = os.Chtimes(path, at, at)
		for d := filepath.Dir(path); d != base; d = filepath.Dir(d) {
			_ = os.Chtimes(d, at, at)
		}
		return path
	}
	old, used := make("bin/aaaa/errand", long), make("bin/bbbb/errand", time.Now())
	theirs := make("bin/launchers/errand-x", long)
	at := filepath.Join(base, "place", "here")
	gone := make("place/here/cccc/version", long)
	waits := make("place/here/dddd/jobs/1/meta", long)
	a := &agent.Place{Dir: filepath.Join(at, "mine"), Root: at}
	a.Tidy(time.Now())
	for path, there := range map[string]bool{old: false, used: true, theirs: true, gone: false, waits: true} {
		if kit.Exists(path) != there {
			t.Errorf("%s: there: %v", path, !there)
		}
	}
}

func TestTheCopiesOfAResult(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	o := p.errand("", "run", "-q", "-o", "sh", "-c", "mkdir -p $ERRAND_OUT_DIR/v.sviz; echo hi > $ERRAND_OUT_DIR/v.sviz/a")
	if o.code != 0 {
		t.Fatalf("%q", o.err)
	}
	ask := func(req map[string]any) (map[string]any, error) {
		dirs, _ := filepath.Glob(filepath.Join(p.cache(), "place", "*", "*"))
		return reach.AskAgent(dirs[0], req)
	}
	copied := func() []string {
		out, _ := filepath.Glob(filepath.Join(p.farCache(), "views", "v1*"))
		return out
	}
	notes := func() []string {
		out, _ := filepath.Glob(filepath.Join(p.cache(), "copied", "*.json"))
		return out
	}
	view := filepath.Join(o.leaf, "v.sviz")
	if _, err := ask(map[string]any{"op": "copy_view", "host": map[string]any{"host": "box"}, "dir": view, "vid": "v1"}); err != nil {
		t.Fatal(err)
	}
	var c filesync.CopyNote
	if len(notes()) == 1 {
		_ = json.Unmarshal([]byte(read(t, notes()[0])), &c)
	}
	if len(copied()) == 0 || c.State != "there" || c.Path != view {
		t.Fatalf("copied: %v, noted: %+v", copied(), c)
	}
	// removed with the result
	if _, err := ask(map[string]any{"op": "runs_remove", "leaves": []string{o.leaf}}); err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(15 * time.Second); len(copied()) > 0 || len(notes()) > 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("left: %v %v", copied(), notes())
		}
	}
}

func TestAMachineSharedWithOthers(t *testing.T) {
	p := newPlace(t)
	p.env = append(p.env, python(t)...)
	p.write("errand-envs.py", "import errand\nerrand.envs[ 'box' ] = errand.Env( [ errand.Ssh( host = 'box', root = 'copy', shared = True ) ] )\n")
	if o := p.errand("", "run", "-q", "--env", "box", "sh", "-c", "echo $ERRAND_SHARED"); o.code != 0 || o.out != "1\n" {
		t.Errorf("a machine said shared: %d %q %q", o.code, o.out, o.err)
	}
}

// An update replaces the file that is typed while what it started runs: what runs goes on, from a copy of its own, and the
// next command finds the other version's agent, to which the place is relayed.
func TestTheProgramIsReplacedWhileItRuns(t *testing.T) {
	p := newPlace(t)
	base := filepath.Dir(p.root)
	typed := filepath.Join(base, "bin", "errand")
	_ = os.MkdirAll(filepath.Dir(typed), 0o755)
	install := func(from string) { // as an installer does: another file, under the same name
		data, _ := os.ReadFile(from)
		_ = os.WriteFile(typed+".new", data, 0o755)
		if err := os.Rename(typed+".new", typed); err != nil {
			t.Fatal(err)
		}
	}
	install(built)
	p.program = typed
	gate := filepath.Join(base, "gate")
	p.write("long.sh", "#!/bin/sh\necho began; while [ ! -e "+gate+" ]; do sleep 0.1; done; \"$ERRAND\" say step late && \"$ERRAND\" --version; echo ended\n")
	o := p.errand("", "run", "--batch", "long.sh")
	id := strings.Fields(o.out + " x")[0]
	if o.code != 0 {
		t.Fatalf("launched: %d %q", o.code, o.err)
	}
	copies := func() []string {
		out, _ := filepath.Glob(filepath.Join(base, "cache", "main", "bin", "*", "errand"))
		return out
	}
	if len(copies()) != 1 {
		t.Fatalf("the agent has no copy of its own: %v", copies())
	}
	install(builtNext)
	if o = p.errand("", "run", "-q", "sh", "-c", "echo new"); o.code != 0 || o.out != "new\n" {
		t.Fatalf("after the update: %d %q %q", o.code, o.out, o.err)
	}
	if len(copies()) != 2 {
		t.Errorf("a copy for each version: %v", copies())
	}
	if o = p.errand("", "status"); !strings.Contains(o.out, id[:8]) || !strings.Contains(o.out, "running") {
		t.Errorf("what the version before holds is listed: %q", o.out)
	}
	_ = os.WriteFile(gate, nil, 0o644)
	if o = p.errand("", "attach", id); o.code != 0 || !strings.Contains(o.out, "began") || !strings.Contains(o.out, "ended") || !strings.Contains(o.out, "errand 0+source") {
		t.Errorf("followed to its end by the next version: %d %q %q", o.code, o.out, o.err)
	}
}
