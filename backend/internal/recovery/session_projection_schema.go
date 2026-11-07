package recovery

import (
	"errors"
	"github.com/mattn/go-sqlite3"
)

const projectionOutboxDDL = `CREATE TABLE IF NOT EXISTS memory_projection_outbox (
 id INTEGER PRIMARY KEY, assertion_id TEXT NOT NULL,
 version INTEGER NOT NULL, deletion_epoch INTEGER NOT NULL, kind TEXT NOT NULL,
 state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 available_at INTEGER NOT NULL, lease_token TEXT, lease_until INTEGER,
 updated_at INTEGER NOT NULL, reason TEXT,
 UNIQUE(assertion_id,version,deletion_epoch,kind),
 CHECK(kind IN ('lexical','person')),
 CHECK(state IN ('pending','leased','done','invalid','failed'))
);
CREATE INDEX IF NOT EXISTS memory_projection_outbox_ready ON memory_projection_outbox(state,available_at,id);
CREATE INDEX IF NOT EXISTS memory_projection_outbox_assertion ON memory_projection_outbox(assertion_id,state);`

const projectionProcessorDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
 assertion_id UNINDEXED, scope_key UNINDEXED, person_id UNINDEXED, content
);
CREATE TABLE IF NOT EXISTS memory_person_projection_state (
 assertion_id TEXT PRIMARY KEY, version INTEGER NOT NULL, deletion_epoch INTEGER NOT NULL,
 scope_key TEXT NOT NULL, person_id TEXT, refreshed_at INTEGER NOT NULL
);`

// These capabilities execute fixed schema only. No caller SQL or callback is
// run with schema privileges. Construction never backfills or drains work.
func (s *WriteSession) EnsureProjectionOutboxSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.WithSavepoint(func(child *WriteSession) error { return child.schema(projectionOutboxDDL) })
}
func (s *WriteSession) EnsureProjectionProcessorSchema() error {
	if e := s.AssertReady(); e != nil {
		return e
	}
	return s.WithSavepoint(func(child *WriteSession) error {
		rows, e := child.Query("SELECT sqlite_compileoption_used('ENABLE_FTS5')")
		if e != nil {
			return e
		}
		var enabled bool
		if rows.Next() {
			e = rows.Scan(&enabled)
		} else {
			e = rows.Err()
		}
		e = errors.Join(e, rows.Close())
		if e != nil {
			return e
		}
		if !enabled {
			return errors.New("projection_fts5_required: build backend with -tags sqlite_fts5")
		}
		return child.schema(projectionProcessorDDL)
	})
}

// FTS5 prepares PRAGMA main.data_version while executing ordinary shadow-table
// INSERT/DELETE/MATCH. This intrinsic read-only counter grants no setting, DDL,
// attached-database access, or authorization cache. The driver's string API
// maps both NULL and an empty-string argument to ""; SQLite marks data_version
// ReadOnly, so an empty-string spelling also only reads and cannot set a cookie.
// Native savepoint segment flushes use the same intrinsic read-only exception.
func mainDataVersionRead(op int, name, argument, database string) bool {
	return op == sqlite3.SQLITE_PRAGMA && name == "data_version" && argument == "" && database == "main"
}

// SQLite xSavepoint/xRelease flush pending FTS segments, including automatic
// merges. This is only used while savepointControl holds the root mutex and
// executes its fixed SAVEPOINT/RELEASE/ROLLBACK TO statement. No caller SQL or
// callback executes in this mode. Content/docsize and unrelated tables are
// excluded. Segment flushes write only data/idx and read config keys/values.
func projectionSavepointIO(op int, table, argument, database string) bool {
	if mainDataVersionRead(op, table, argument, database) {
		return true
	}
	if op == sqlite3.SQLITE_SELECT {
		return table == "" && argument == "" && database == ""
	}
	if op == sqlite3.SQLITE_READ && database == "main" && table == "memory_fts_config" && (argument == "k" || argument == "v") {
		return true
	}
	if database != "main" || (table != "memory_fts_data" && table != "memory_fts_idx") {
		return false
	}
	switch op {
	case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_DELETE:
		return argument == ""
	case sqlite3.SQLITE_READ:
		return true
	}
	return false
}
