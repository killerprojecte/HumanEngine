package logdelivery

import (
	"io"
	"os"
)

// stdoutSink borrows its output. Actual *os.File output uses exactly one native
// write syscall, exposing partial progress/EAGAIN to the channel's source loop.
// os.File.Write and RawConn.Write hide retries, so neither is used here.
// A non-file io.Writer is an explicit injected adaptation, not a raw fd claim.
type stdoutSink struct {
	writer io.Writer
	file   *os.File
}

func newStdoutSink(writer io.Writer) *stdoutSink {
	s := &stdoutSink{writer: writer}
	s.file, _ = writer.(*os.File)
	return s
}
func (s *stdoutSink) prepare() error { return nil }
func (s *stdoutSink) write(bytes []byte) (int, error) {
	if s.file != nil {
		return writeFileDescriptor(s.file, bytes)
	}
	return s.writer.Write(bytes)
}
func (s *stdoutSink) sync() error  { return nil }
func (s *stdoutSink) close() error { return nil }

// SyscallConn preserves the descriptor's existing mode and pins its lifetime
// during Control. In particular, do not call File.Fd(), which can implicitly
// switch a pollable descriptor back to blocking mode.
func writeFileDescriptor(file *os.File, bytes []byte) (n int, err error) {
	raw, e := file.SyscallConn()
	if e != nil {
		return 0, e
	}
	e = raw.Control(func(fd uintptr) { n, err = writeDescriptor(fd, bytes) })
	if e != nil {
		return 0, e
	}
	// Native write(2) reports -1 on a zero-byte syscall error. Preserve
	// positive partial progress, but present zero-byte errors to the source loop.
	if n < 0 && err != nil {
		n = 0
	}
	return n, err
}
