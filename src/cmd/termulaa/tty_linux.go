package main

import (
	"syscall"
	"unsafe"
)

const (
	ioctlGetTermios = syscall.TCGETS
	ioctlSetTermios = syscall.TCSETS
)

func ioctlTermios(fd uintptr, req uint, t *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(unsafe.Pointer(t))); errno != 0 {
		return errno
	}
	return nil
}
