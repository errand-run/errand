//go:build !windows

package system

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNobodyReadsAnyMore(t *testing.T) {
	told := func(f *os.File) chan bool {
		c := make(chan bool, 1)
		go func() { c <- AwaitNoReader(f) }()
		return c
	}
	soon := func(what string, c chan bool, want bool) {
		t.Helper()
		select {
		case got := <-c:
			if !want || !got {
				t.Errorf("%s: %v", what, got)
			}
		case <-time.After(1200 * time.Millisecond):
			if want {
				t.Errorf("%s: not seen", what)
			}
		}
	}
	r, w, _ := os.Pipe()
	c := told(w)
	soon("a pipe that is read", c, false)
	r.Close()
	soon("a pipe nobody reads", c, true)

	// what sshd gives a forced command: one socket, both ways. Its input may end while its output is read.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	ours, theirs := os.NewFile(uintptr(fds[0]), "ours"), os.NewFile(uintptr(fds[1]), "theirs")
	c = told(ours)
	_ = syscall.Shutdown(fds[1], syscall.SHUT_WR)
	soon("a socket whose input ended", c, false)
	theirs.Close()
	soon("a socket that was closed", c, true)

	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	soon("a file", told(f), false)
}
