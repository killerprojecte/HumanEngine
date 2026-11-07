// Package storage owns connection policy, not bot state or model work.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"path/filepath"
)

func OpenExisting(path string, writable bool) (*sql.DB, error) {
	if err := RequireFTS5(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	mode := "ro"
	if writable {
		mode = "rw"
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Set("mode", mode)
	q.Set("_busy_timeout", "5000")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	if err = db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	return db, nil
}
