//go:build windows

package logdelivery

import "syscall"

func writeDescriptor(fd uintptr, bytes []byte) (int, error) {
	return syscall.Write(syscall.Handle(fd), bytes)
}
