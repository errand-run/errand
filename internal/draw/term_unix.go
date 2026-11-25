//go:build !windows

package draw

import (
	"os"
	"syscall"
)

// ReadsReturn: reading a terminal comes back within a tenth of a second, typed or not
func ReadsReturn(f *os.File) {
	if t, err := getTermios(f); err == nil {
		t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 0, 1
		_ = setTermios(f, t)
	}
}
