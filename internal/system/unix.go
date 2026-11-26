//go:build !windows

// Package system is what differs from one system to another in what errand asks of it: locks on files, processes and their
// groups, who owns a file. On Windows, see windows.go.
package system

import (
	"os"
	"runtime"
	"syscall"
)

// TryLock: the file locked for this process alone, if nobody holds it
func TryLock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }

// Lock: the file locked for this process alone, waited for
func Lock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func Unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// Alive: whether a process is there
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Apart: how a process is started in a session, and a group, of its own
func Apart() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// HearCtrlC: what is started from here hears Ctrl-C: it does
func HearCtrlC() {}

// CtrlC: Ctrl-C to a process and its group
func CtrlC(pid int) error { return syscall.Kill(-pid, syscall.SIGINT) }

// Started: when a process started, where the system says it in a call; asked otherwise here
func Started(pid int) (any, bool) { return nil, false }

// Detached: how what outlives whoever starts it is started
func Detached() *syscall.SysProcAttr { return Apart() }

// Lent: another way, where that is refused
func Lent() *syscall.SysProcAttr { return nil }

// SignalGroup: a signal to a process started apart, and to its group
func SignalGroup(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }

func Signal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// Writable: whether this account may write there
func Writable(path string) bool { return syscall.Access(path, 2) == nil }

// Owner: the account a file is of, when the system says
func Owner(st os.FileInfo) (int, bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(s.Uid), true
}

// Inode: what names a file whatever its path
func Inode(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Ino)
	}
	return 0
}

// Links: how many names a file has
func Links(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Nlink)
	}
	return 1
}

// Exec: this process becomes another program
func Exec(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }

// OnTerminal: how a process is started in a session of its own, the terminal `fd` of its descriptors its controlling one
func OnTerminal(fd int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: fd}
}

// Resized: the signal that says a window changed; Poked: the one an agent of an allocation is woken with
const (
	Resized = syscall.SIGWINCH
	Poked   = syscall.SIGUSR1
)

// Umask: what is left out of the modes of the files this process makes
func Umask() int {
	mask := syscall.Umask(0)
	syscall.Umask(mask)
	return mask
}

// Device: the file system a path is on, when the system says it as a number
func Device(path string) (uint64, bool) {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// Rights: descriptors, as they are passed over a socket
func Rights(fds ...int) []byte { return syscall.UnixRights(fds...) }

// MaxRSS: the most memory a process had, in MB
func MaxRSS(u *syscall.Rusage) float64 {
	mb := float64(u.Maxrss) / 1024
	if runtime.GOOS == "darwin" {
		mb /= 1024 // ( bytes there, kilobytes on Linux )
	}
	return mb
}

// Terminate: a process asked to end ( SIGTERM )
func Terminate(pid int) { _ = syscall.Kill(pid, syscall.SIGTERM) }
