//go:build sqlite_fts5

package storage

// RequireFTS5 enforces the backend's mandatory native SQLite profile. Actual
// virtual-table operations remain covered separately by integration tests.
func RequireFTS5() error { return nil }
