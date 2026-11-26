//go:build windows

package system

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
	generateCtrl = kernel32.NewProc("GenerateConsoleCtrlEvent")
	createJob    = kernel32.NewProc("CreateJobObjectW")
	assignJob    = kernel32.NewProc("AssignProcessToJobObject")
	terminateJob = kernel32.NewProc("TerminateJobObject")
	setJobInfo   = kernel32.NewProc("SetInformationJobObject")
	openProcess  = kernel32.NewProc("OpenProcess")
	getExitCode  = kernel32.NewProc("GetExitCodeProcess")
)

func lockWith(f *os.File, flags uintptr) error {
	var o syscall.Overlapped
	r, _, err := lockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&o)))
	if r == 0 {
		return err
	}
	return nil
}

// TryLock: the file locked for this process alone, if nobody holds it
func TryLock(f *os.File) error { return lockWith(f, 2|1) }

// Lock: the file locked for this process alone, waited for
func Lock(f *os.File) error { return lockWith(f, 2) }

func Unlock(f *os.File) error {
	var o syscall.Overlapped
	r, _, err := unlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&o)))
	if r == 0 {
		return err
	}
	return nil
}

// Alive: whether a process is there
func Alive(pid int) bool {
	h, _, _ := openProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	var code uint32
	getExitCode.Call(h, uintptr(unsafe.Pointer(&code)))
	return code == 259
}

// Apart: how a process is started on a console of its own, that nobody sees: where a Ctrl-C reaches it, and what it starts
func Apart() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// Started: when a process started, as the system counts; nil when it is not there
func Started(pid int) (any, bool) {
	if !Alive(pid) {
		return nil, true
	}
	h, _, _ := openProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return nil, true
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	var began, ended, inKernel, inUser syscall.Filetime
	if syscall.GetProcessTimes(syscall.Handle(h), &began, &ended, &inKernel, &inUser) != nil {
		return nil, true
	}
	return itoa(int(began.Nanoseconds() / 100000)), true
}

// Detached: how what outlives whoever starts it is started -- out of the job of an ssh session, which ends with it
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x8 | 0x01000000} // DETACHED_PROCESS, CREATE_BREAKAWAY_FROM_JOB
}

// Lent: the same, in that job, where it cannot be left
func Lent() *syscall.SysProcAttr { return &syscall.SysProcAttr{CreationFlags: 0x8} }

// HearCtrlC: what is started from here hears Ctrl-C -- what an ssh session starts is told not to, and passes that on
func HearCtrlC() { kernel32.NewProc("SetConsoleCtrlHandler").Call(0, 0) }

// CtrlC: Ctrl-C on the console of a process -- from a process that has nothing else to do, which loses its own
func CtrlC(pid int) error {
	kernel32.NewProc("FreeConsole").Call()
	if r, _, err := kernel32.NewProc("AttachConsole").Call(uintptr(pid)); r == 0 {
		return err
	}
	kernel32.NewProc("SetConsoleCtrlHandler").Call(0, 1)
	if r, _, err := generateCtrl.Call(0, 0); r == 0 {
		return err
	}
	return nil
}

// SignalGroup: Ctrl-C to a process started apart, and to those on its console; a kill ends its whole tree
func SignalGroup(pid int, sig syscall.Signal) error {
	if sig == syscall.SIGKILL || sig == syscall.SIGTERM {
		return exec.Command("taskkill", "/T", "/F", "/PID", itoa(pid)).Run()
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := exec.Command(self, "ctrl-c", itoa(pid)).CombinedOutput()
	if err != nil {
		os.Stderr.WriteString("errand: Ctrl-C to " + itoa(pid) + ": " + err.Error() + " " + string(out) + "\n")
	}
	return err
}

func Signal(pid int, sig syscall.Signal) error {
	if sig == 0 {
		if Alive(pid) {
			return nil
		}
		return os.ErrProcessDone
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// Writable: whether this account may write there
func Writable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().Perm()&0o200 != 0
}

// Owner: the account a file is of: the system does not say it as a number
func Owner(st os.FileInfo) (int, bool) { return 0, false }

// Inode: what names a file whatever its path
func Inode(st os.FileInfo) uint64 { return 0 }

// Links: how many names a file has
func Links(st os.FileInfo) uint64 { return 1 }

// Exec: another program in this one's place -- started, waited for, its end this one's
func Exec(path string, argv, env []string) error {
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	signal.Ignore(os.Interrupt)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

// OnTerminal: there is no pseudo-terminal here: as `Apart`
func OnTerminal(fd int) *syscall.SysProcAttr { return Apart() }

// Resized, Poked: signals that never come here
const (
	Resized = syscall.Signal(28)
	Poked   = syscall.Signal(30)
)

func Umask() int { return 0 }

func Device(path string) (uint64, bool) { return 0, false }

func Rights(fds ...int) []byte { return nil }

// MaxRSS: not said here
func MaxRSS(u *syscall.Rusage) float64 { return 0 }

// AwaitNoReader: it cannot be told here: what is written says it, when it is refused
func AwaitNoReader(f *os.File) bool { return false }

// Terminate: a process made to end
func Terminate(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
