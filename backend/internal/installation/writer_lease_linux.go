//go:build linux

package installation

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// secureLeaseFile walks from / with search-only O_PATH directory descriptors
// and O_NOFOLLOW. Directory listing permission is intentionally unnecessary.
// The inode is locked, not a hash of a spelling; dot/absolute aliases converge.
// Hard links are rejected because SQLite WAL sidecars are pathname-based.
// This is cooperative ownership on local Linux filesystems. An operator must
// not rename/unlink live database/authority directories or files.
func secureLeaseFile(path string) (*os.File, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, errWriterLeasePath
	}
	parent, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errWriterLeasePath
	}
	defer func() { unix.Close(parent) }()
	parts := splitLeasePath(filepath.Dir(path))
	for _, part := range parts {
		next, e := unix.Openat(parent, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, errWriterLeasePath
		}
		unix.Close(parent)
		parent = next
	}
	fd, err := unix.Openat(parent, filepath.Base(path), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errWriterLeasePath
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(e error) (*os.File, error) { return nil, errors.Join(e, f.Close()) }
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return fail(errWriterLeasePath)
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fail(ErrWriterBusy)
		}
		return fail(errors.New("installation_writer_lease_unavailable"))
	}
	// Catch replacement during acquisition. Never repair, chmod or unlink it.
	named, err := os.Lstat(path)
	held, statErr := f.Stat()
	if err != nil || statErr != nil || !os.SameFile(named, held) {
		return fail(errWriterLeasePath)
	}
	return f, nil
}

func splitLeasePath(path string) []string {
	if path == "/" {
		return nil
	}
	parent, base := filepath.Split(path)
	return append(splitLeasePath(filepath.Clean(parent)), base)
}

func acquireDatabaseLease(path string) (*writerLease, error) {
	f, err := secureLeaseFile(path)
	if err != nil {
		return nil, err
	}
	return &writerLease{database: f}, nil
}

func (l *writerLease) acquireAuthority(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return errWriterLeasePath
	}
	f, err := secureLeaseFile(filepath.Join(path, ".writer.lock"))
	if err != nil {
		return err
	}
	l.authority = f
	return nil
}
