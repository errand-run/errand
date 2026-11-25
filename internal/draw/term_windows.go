// Windows has no pseudo-terminal of this kind: a run's streams are pipes there, and the command line draws for nobody. The
// size of a console is asked of it.

package draw

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

type coord struct{ x, y int16 }

type smallRect struct{ left, top, right, bottom int16 }

type consoleInfo struct {
	size, cursor coord
	attributes   uint16
	window       smallRect
	maximum      coord
}

var (
	kernel     = syscall.NewLazyDLL("kernel32.dll")
	screenInfo = kernel.NewProc("GetConsoleScreenBufferInfo")
	getMode    = kernel.NewProc("GetConsoleMode")
	setMode    = kernel.NewProc("SetConsoleMode")
	errNoTerm  = errors.New("no pseudo-terminal on this system")
)

type termios struct{ mode uint32 }

func getTermios(f *os.File) (*termios, error) {
	var t termios
	if r, _, err := getMode.Call(f.Fd(), uintptr(unsafe.Pointer(&t.mode))); r == 0 {
		return nil, err
	}
	return &t, nil
}

func setTermios(f *os.File, t *termios) error {
	if r, _, err := setMode.Call(f.Fd(), uintptr(t.mode)); r == 0 {
		return err
	}
	return nil
}

// RawKeys: every key passed on as it is typed -- no line, no echo; `all`: Ctrl-C too
func RawKeys(f *os.File, all bool) (func(), error) {
	old, err := getTermios(f)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.mode &^= 0x2 | 0x4 // ENABLE_LINE_INPUT, ENABLE_ECHO_INPUT
	raw.mode |= 0x200      // ENABLE_VIRTUAL_TERMINAL_INPUT
	if all {
		raw.mode &^= 0x1 // ENABLE_PROCESSED_INPUT
	}
	if err := setTermios(f, &raw); err != nil {
		return nil, err
	}
	return func() { _ = setTermios(f, old) }, nil
}

func TermSize(f *os.File) (cols, rows int, ok bool) {
	var i consoleInfo
	if r, _, _ := screenInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&i))); r == 0 {
		return 0, 0, false
	}
	return int(i.window.right-i.window.left) + 1, int(i.window.bottom-i.window.top) + 1, true
}

func AsWritten(f *os.File) {}

func SetWinsize(f *os.File, cols, rows int) {}

func OpenPty() (*os.File, *os.File, error) { return nil, nil, errNoTerm }

func ReadsReturn(f *os.File) {}
