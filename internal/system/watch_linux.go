package system

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// watchDir ( inotify )
func watchDir(dir, name string, on func()) (func(), error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "inotify") // ( closed, it ends a read that waits )
	top, err := syscall.InotifyAddWatch(fd, dir, syscall.IN_CREATE|syscall.IN_DELETE|syscall.IN_MOVED_TO|syscall.IN_MOVED_FROM)
	if err != nil {
		f.Close()
		return nil, err
	}
	journal := func() { _, _ = syscall.InotifyAddWatch(fd, filepath.Join(dir, name), syscall.IN_MODIFY) }
	journal()
	go func() {
		defer f.Close()
		buf := make([]byte, 1<<14)
		for {
			n, err := f.Read(buf)
			if err != nil || n < syscall.SizeofInotifyEvent {
				return
			}
			for at := 0; at+syscall.SizeofInotifyEvent <= n; {
				e := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[at]))
				if int(e.Wd) == top && e.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
					journal() // ( the same watch again when it is there already )
				}
				if int(e.Wd) == top && e.Mask&syscall.IN_IGNORED != 0 { // the directory itself went
					return
				}
				at += syscall.SizeofInotifyEvent + int(e.Len)
			}
			on()
		}
	}()
	return func() { f.Close() }, nil
}
