//go:build !sqlite_fts5

package storage

import "errors"

func RequireFTS5() error {
	return errors.New("sqlite_fts5_required: build backend with -tags sqlite_fts5")
}
