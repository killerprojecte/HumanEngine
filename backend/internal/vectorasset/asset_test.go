//go:build bundled_sqlite_vec && linux && amd64

package vectorasset

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExtractSealedVerifiedAssetAndCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	a, err := Extract()
	if err != nil {
		t.Fatal(err)
	}
	var path string
	err = a.Use(func(p string) error {
		path = p
		data, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != SHA256 {
			return fmt.Errorf("extracted digest mismatch")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/proc/self/fd/") {
		t.Fatalf("unexpected library path: %s", path)
	}
	const wantSeals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if seals, err := unix.FcntlInt(a.file.Fd(), unix.F_GET_SEALS, 0); err != nil || seals != wantSeals {
		t.Fatalf("library is not sealed: %#x %v", seals, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("library wrote a filesystem asset: %v %v", entries, err)
	}
	if License == "" || Manifest == "" {
		t.Fatal("missing embedded attribution")
	}
	// A second owner never borrows the first descriptor.
	b, err := Extract()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.Use(func(p string) error {
		if p == path {
			return fmt.Errorf("shared extraction")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("asset survived cleanup", err)
	}
	if err = a.Use(func(string) error { t.Fatal("closed asset callback called"); return nil }); err == nil {
		t.Fatal("closed asset used")
	}
	if err = b.Use(func(p string) error { _, e := os.Stat(p); return e }); err != nil {
		t.Fatal("other owner removed", err)
	}
}
func TestCleanupWaitsForNativeLoadLease(t *testing.T) {
	a, err := Extract()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	used := make(chan error, 1)
	closed := make(chan error, 1)
	go func() {
		used <- a.Use(func(p string) error { close(entered); <-release; _, e := os.Stat(p); return e })
	}()
	<-entered
	go func() { closed <- a.Close() }()
	select {
	case e := <-closed:
		t.Fatal("cleanup escaped active lease", e)
	default:
	}
	close(release)
	if e := <-used; e != nil {
		t.Fatal(e)
	}
	if e := <-closed; e != nil {
		t.Fatal(e)
	}
}
