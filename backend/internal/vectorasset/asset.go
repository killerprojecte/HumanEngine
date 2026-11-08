// Package vectorasset owns the pinned genuine sqlite-vec binary in bundled
// builds. It never downloads assets or searches development dependency folders.
package vectorasset

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const Version = "0.1.9"
const SHA256 = "5923730861b86c707cca5602b5f91092f9e52a46706dbc6e269fd4bb9c4498e8"
const BinarySize = 159816

// License and provenance are retained in the executable, not only its source.
//
//go:embed LICENSE-MIT
var License string

//go:embed manifest.json
var Manifest string

// Asset is a private per-owner extraction. Use holds the read lock throughout
// dlopen so cleanup cannot remove a path while a physical connection loads it.
// Linux permits unlink after already loaded mappings; their connection lifetime
// remains SQLite's responsibility. No external process/temp-file cache is used.
type Asset struct {
	mu              sync.RWMutex
	directory, path string
	closed          bool
}

func Extract() (*Asset, error) {
	data, err := bundledBytes()
	if err != nil {
		return nil, err
	}
	if len(data) != BinarySize || fmt.Sprintf("%x", sha256.Sum256(data)) != SHA256 {
		return nil, errors.New("sqlite_vec_bundle_integrity_failed")
	}
	directory, err := os.MkdirTemp("", "humanengine-sqlite-vec-")
	if err != nil {
		return nil, err
	}
	a := &Asset{directory: directory, path: filepath.Join(directory, "vec0.so")}
	// Reachable runtime attribution is distributed alongside each extraction.
	for name, content := range map[string]string{"LICENSE-MIT": License, "manifest.json": Manifest} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			return nil, errors.Join(err, a.Close())
		}
	}
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		cleanup := a.Close()
		return nil, errors.Join(err, cleanup)
	}
	return a, nil
}
func (a *Asset) Use(load func(string) error) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return errors.New("sqlite_vec_bundle_closed")
	}
	return load(a.path)
}
func (a *Asset) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	// Do not scan/reap any sibling directory or reuse another process's asset.
	// Remove only this extraction; a crash can leave it for the OS temp policy.
	err := os.RemoveAll(a.directory)
	if err == nil {
		a.closed = true
	}
	return err
}
