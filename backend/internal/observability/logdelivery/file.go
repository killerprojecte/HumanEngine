package logdelivery

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// fileSink is confined to one channel worker, including cleanup. Neither
// cancellation nor another lease can touch its descriptor.
type fileSink struct {
	directory    string
	rotateBytes  int64
	file         *os.File
	size         int64
	recordOffset int
	trace        func([]any)
	// Test hooks wrap actual fd operations without replacing the file lifecycle.
	writeFile func(*os.File, []byte) (int, error)
	syncFile  func(*os.File) error
	closeFile func(*os.File) error
}

func (f *fileSink) prepare() error {
	f.recordOffset = 0
	if err := f.open(); err != nil {
		return err
	}
	if f.size > f.rotateBytes {
		return f.rotate()
	}
	return nil
}
func (f *fileSink) open() error {
	if f.file != nil {
		return nil
	}
	f.operation("mkdir", f.directory)
	if err := os.MkdirAll(f.directory, 0700); err != nil {
		return err
	}
	f.operation("open", filepath.Join(f.directory, "runtime.jsonl"), "a+", 0600)
	file, err := os.OpenFile(filepath.Join(f.directory, "runtime.jsonl"), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.file = file
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	f.size = stat.Size()
	if f.size > 0 {
		var tail [1]byte
		if _, err = file.ReadAt(tail[:], f.size-1); err != nil {
			return err
		}
		if tail[0] != '\n' {
			return f.rotate()
		}
	}
	return nil
}
func (f *fileSink) rotate() error {
	if err := f.sync(); err != nil {
		return err
	}
	if err := f.close(); err != nil {
		return err
	}
	path := filepath.Join(f.directory, "runtime.jsonl")
	for i := 2; i >= 1; i-- {
		from := path
		if i > 1 {
			from += "." + strconv.Itoa(i-1)
		}
		to := path + "." + strconv.Itoa(i)
		if err := os.Remove(to); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			f.operation("unlink", to)
		}
		if err := os.Rename(from, to); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			f.operation("rename", from, to)
		}
	}
	return f.open()
}
func (f *fileSink) write(bytes []byte) (int, error) {
	f.operation("write", filepath.Join(f.directory, "runtime.jsonl"), f.recordOffset, len(bytes))
	var n int
	var err error
	if f.writeFile != nil {
		n, err = f.writeFile(f.file, bytes)
	} else {
		n, err = writeFileDescriptor(f.file, bytes)
	}
	// This counter is private and only used before the next complete record.
	// On terminal partial failure no next record reaches prepare().
	f.size += int64(n)
	f.recordOffset += n
	return n, err
}
func (f *fileSink) sync() error {
	if f.file == nil {
		return nil
	}
	f.operation("fsync", filepath.Join(f.directory, "runtime.jsonl"))
	if f.syncFile != nil {
		return f.syncFile(f.file)
	}
	return f.file.Sync()
}
func (f *fileSink) close() error {
	if f.file == nil {
		return nil
	}
	f.operation("close", filepath.Join(f.directory, "runtime.jsonl"))
	file := f.file
	f.file = nil
	if f.closeFile != nil {
		return f.closeFile(file)
	}
	return file.Close()
}

func (f *fileSink) operation(args ...any) {
	if f.trace != nil {
		f.trace(args)
	}
}
