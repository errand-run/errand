// A terminal's settings and size, for the command line ( raw mode while a run reads what is typed, the window's
// size to hand over ) and for the agent ( the output of a run's pseudo-terminal left as it is written ): the
// system's own calls, since nothing outside the standard library is carried for it.

package draw

import (
	"os"
	"syscall"
	"unsafe"
)

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

func getTermios(f *os.File) (*syscall.Termios, error) {
	var t syscall.Termios
	if err := ioctl(f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return nil, err
	}
	return &t, nil
}

func setTermios(f *os.File, t *syscall.Termios) error {
	return ioctl(f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(t)))
}

// TermSize: the window a terminal shows, in columns and rows
func TermSize(f *os.File) (cols, rows int, ok bool) {
	var ws [4]uint16
	if err := ioctl(f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); err != nil || ws[1] == 0 {
		return 0, 0, false
	}
	return int(ws[1]), int(ws[0]), true
}

// RawKeys: the terminal made to pass on every key as it is typed -- no echo, no line editing, no translation of the
// enter key --, and Ctrl-C still a signal to this command, which stops the run as it always did. What is written to
// it is still translated ( a line ends with a carriage return ). Ctrl-\ and Ctrl-Z go to the run, as to a program
// on a terminal of its own. -> what puts it back
func RawKeys(f *os.File, all bool) (func(), error) {
	old, err := getTermios(f)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.IEXTEN
	if all { // every key is the program's, Ctrl-C included: a shell
		raw.Lflag &^= syscall.ISIG
	}
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	raw.Cc[syscall.VSUSP], raw.Cc[syscall.VQUIT] = 0, 0
	if err := setTermios(f, &raw); err != nil {
		return nil, err
	}
	return func() { _ = setTermios(f, old) }, nil
}

// AsWritten: what is written to a pseudo-terminal comes out as it was -- no carriage return added before a line feed
func AsWritten(f *os.File) {
	if t, err := getTermios(f); err == nil {
		t.Oflag &^= syscall.OPOST
		_ = setTermios(f, t)
	}
}

func SetWinsize(f *os.File, cols, rows int) {
	ws := [4]uint16{uint16(rows), uint16(cols), 0, 0}
	_ = ioctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}
