package credentials

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LoadKey never repairs or replaces an existing key. hasCiphertext is evaluated
// by the caller on the same installation before permitting first-use creation.
func LoadKey(path string, hasCiphertext bool) ([]byte, error) {
	file, err := openKey(path)
	if errors.Is(err, os.ErrNotExist) {
		if hasCiphertext {
			return nil, errors.New("secret_key_missing")
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		fd, e := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if e == nil {
			created := os.NewFile(uintptr(fd), path)
			_, e = created.Write(key)
			if e == nil {
				e = created.Sync()
			}
			closeErr := created.Close()
			if e == nil {
				e = closeErr
			}
			if e != nil {
				return nil, errors.New("secret_key_unavailable")
			}
			// Persist the new directory entry as well as the key bytes.
			dir, e := os.Open(filepath.Dir(path))
			if e != nil {
				return nil, e
			}
			e = dir.Sync()
			_ = dir.Close()
			if e != nil {
				return nil, e
			}
		} else if !errors.Is(e, os.ErrExist) {
			return nil, errors.New("secret_key_unavailable")
		}
		file, err = openKey(path)
	}
	if err != nil {
		return nil, errors.New("secret_key_unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.New("secret_key_unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("secret_key_permissions")
	}
	if info.Size() != 32 {
		return nil, errors.New("secret_key_invalid")
	}
	key := make([]byte, 32)
	if _, err = io.ReadFull(file, key); err != nil {
		return nil, errors.New("secret_key_invalid")
	}
	return key, nil
}
func openKey(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
