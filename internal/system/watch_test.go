package system

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	told := make(chan struct{}, 100)
	stop := Watch(dir, "steps.jsonl", func() { told <- struct{}{} })
	defer stop()
	wait := func(what string, want bool) {
		t.Helper()
		select {
		case <-told:
			if !want {
				t.Errorf("%s: told", what)
			}
			for quiet := false; !quiet; { // what came with it
				select {
				case <-told:
				case <-time.After(100 * time.Millisecond):
					quiet = true
				}
			}
		case <-time.After(1500 * time.Millisecond):
			if want {
				t.Errorf("%s: not told", what)
			}
		}
	}
	journal := filepath.Join(dir, "steps.jsonl")
	say := func(path string) {
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		_, _ = f.WriteString("{}\n")
		f.Close()
	}
	say(journal)
	wait("the journal, made", true)
	say(journal)
	wait("the journal, written", true)
	say(journal)
	wait("the journal, written again", true)
	say(filepath.Join(dir, "other.txt"))
	wait("another file, made", true)
	say(filepath.Join(dir, "other.txt"))
	wait("another file, written", false)
	_ = os.Mkdir(filepath.Join(dir, "frames"), 0o755)
	wait("a directory, made", true)
	stop()
	say(journal)
	wait("once stopped", false)
}
