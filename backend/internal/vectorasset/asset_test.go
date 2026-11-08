//go:build bundled_sqlite_vec && linux && amd64

package vectorasset

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPrivateVerifiedAssetAndCleanup(t *testing.T) {
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
	for _, entry := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, e := os.Stat(entry.path)
		if e != nil || info.Mode().Perm() != entry.mode {
			t.Fatalf("private mode: %v %v", info, e)
		}
	}
	for name, want := range map[string]string{"LICENSE-MIT": License, "manifest.json": Manifest} {
		got, err := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if err != nil || string(got) != want || len(got) == 0 {
			t.Fatalf("attribution %s: %v", name, err)
		}
	}
	// A second owner never borrows the first extraction or removes its files.
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
