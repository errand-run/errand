package system

import (
	"os"
	"path/filepath"
	"syscall"
)

const evtOnly = 0x8000 // O_EVTONLY

// watchDir ( kqueue )
func watchDir(dir, name string, on func()) (func(), error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(kq)
	quitR, quitW, err := os.Pipe()
	if err != nil {
		syscall.Close(kq)
		return nil, err
	}
	add := func(fd int, filter int, fflags uint32) error {
		var e syscall.Kevent_t
		syscall.SetKevent(&e, fd, filter, syscall.EV_ADD|syscall.EV_CLEAR)
		e.Fflags = fflags
		_, err := syscall.Kevent(kq, []syscall.Kevent_t{e}, nil, nil)
		return err
	}
	top, err := syscall.Open(dir, evtOnly|syscall.O_CLOEXEC, 0)
	if err == nil {
		err = add(top, syscall.EVFILT_VNODE, syscall.NOTE_WRITE|syscall.NOTE_DELETE)
	}
	if err == nil {
		err = add(int(quitR.Fd()), syscall.EVFILT_READ, 0)
	}
	file := -1
	journal := func() {
		if file >= 0 {
			return
		}
		if fd, err := syscall.Open(filepath.Join(dir, name), evtOnly|syscall.O_CLOEXEC, 0); err == nil {
			if add(fd, syscall.EVFILT_VNODE, syscall.NOTE_WRITE|syscall.NOTE_EXTEND) != nil {
				syscall.Close(fd)
				return
			}
			file = fd
		}
	}
	end := func() {
		for _, fd := range []int{top, file, kq} {
			if fd >= 0 {
				syscall.Close(fd)
			}
		}
		quitR.Close()
	}
	if err != nil {
		end()
		quitW.Close()
		return nil, err
	}
	journal()
	quit := int(quitR.Fd())
	go func() {
		defer end()
		got := make([]syscall.Kevent_t, 4)
		for {
			n, err := syscall.Kevent(kq, nil, got, nil)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return
			}
			for _, e := range got[:n] {
				if int(e.Ident) == quit {
					return
				}
				if int(e.Ident) == top && e.Fflags&syscall.NOTE_DELETE != 0 {
					return
				}
			}
			journal()
			on()
		}
	}()
	return func() { quitW.Close() }, nil
}
