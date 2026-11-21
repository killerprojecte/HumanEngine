//go:build bundled_sqlite_vec && linux && amd64

package vectorasset

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

//go:embed data/linux-amd64/vec0.so
var library []byte

func Extract() (*Asset, error) {
	if len(library) != BinarySize || fmt.Sprintf("%x", sha256.Sum256(library)) != SHA256 {
		return nil, errors.New("sqlite_vec_bundle_integrity_failed")
	}
	fd, err := unix.MemfdCreate("humanengine-sqlite-vec", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "humanengine-sqlite-vec")
	if _, err = file.Write(library); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	const seals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &Asset{file: file, path: fmt.Sprintf("/proc/self/fd/%d", fd)}, nil
}
