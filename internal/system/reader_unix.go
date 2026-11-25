//go:build !windows

package system

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// AwaitNoReader: until nobody reads what is written to `f` any more ( a pipe or a socket, whose other end was closed ) -> true;
// false at once when that cannot be told of it ( a device, on macOS ). Looked at now and then: asked to wait for nothing but
// that, macOS never says it.
func AwaitNoReader(f *os.File) bool {
	for ; ; time.Sleep(500 * time.Millisecond) {
		fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLOUT}}
		n, err := unix.Poll(fds, 0)
		switch {
		case err == unix.EINTR:
		case err != nil || (n > 0 && fds[0].Revents&unix.POLLNVAL != 0):
			return false
		case n > 0 && fds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0:
			return true
		}
	}
}
