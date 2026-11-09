//go:build unix

package logdelivery

import "syscall"

func writeDescriptor(fd uintptr, bytes []byte) (int, error) { return syscall.Write(int(fd), bytes) }
