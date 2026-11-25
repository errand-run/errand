package draw

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	tiocptygrant = 0x20007454
	tiocptyunlk  = 0x20007452
	tiocptygname = 0x40807453
)

func OpenPty() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := ioctl(master.Fd(), tiocptygrant, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	if err := ioctl(master.Fd(), tiocptyunlk, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	var name [128]byte
	if err := ioctl(master.Fd(), tiocptygname, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		master.Close()
		return nil, nil, err
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	slave, err := os.OpenFile(string(name[:n]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
