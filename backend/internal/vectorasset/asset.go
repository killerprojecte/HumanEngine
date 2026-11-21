// Package vectorasset owns the pinned genuine sqlite-vec binary in bundled
// builds. It never downloads assets or searches development dependency folders.
package vectorasset

import (
	_ "embed"
	"errors"
	"os"
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

// Asset owns the verified library for one installation. Use keeps its
// descriptor open until dlopen returns; SQLite owns the loaded mappings.
type Asset struct {
	mu   sync.RWMutex
	file *os.File
	path string
}

func (a *Asset) Use(load func(string) error) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.file == nil {
		return errors.New("sqlite_vec_bundle_closed")
	}
	return load(a.path)
}
func (a *Asset) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}
