package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func schemaTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestSessionAssertionHistoryParentMigrationAndNoReplay(t *testing.T) {
	f := newWriterFixture(t)
	// A retained legacy revision can contain duplicate parents, nonstrings and
	// multiple proof alternatives. Only text parents become reverse links.
	if _, err := f.main.Exec(assertionHistoryDDL + `
DROP TABLE memory_assertion_revision_parents;
INSERT INTO memory_assertion_state VALUES('a',2,0,0);
INSERT INTO memory_assertion_revisions VALUES('a',1,'scene:s',NULL,'key','hash','{"supports":[{"parents":["z","a","z",2,null,{"id":"object"}]},{"parents":["b"]}]}');
INSERT INTO memory_assertion_revisions VALUES('a',2,'scene:s',NULL,NULL,NULL,NULL);`); err != nil {
		t.Fatal(err)
	}
	initialize := func(s *WriteSession) error { return s.EnsureAssertionHistorySchema() }
	if err := f.authority.WithWriteSession(context.Background(), f.main, initialize); err != nil {
		t.Fatal(err)
	}
	var parents string
	if err := f.main.QueryRow("SELECT group_concat(parent_id,',') FROM (SELECT parent_id FROM memory_assertion_revision_parents ORDER BY parent_id)").Scan(&parents); err != nil || parents != "a,b,z" {
		t.Fatal(parents, err)
	}
	// Existing parent table is the migration marker. A later malformed body
	// does not trigger replay, and intentionally removed links stay removed.
	if _, err := f.main.Exec("DELETE FROM memory_assertion_revision_parents WHERE parent_id='b'; UPDATE memory_assertion_revisions SET value='malformed' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.WithWriteSession(context.Background(), f.main, initialize); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.main.QueryRow("SELECT count(*) FROM memory_assertion_revision_parents").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	assertMain(t, f, "original", 0, 1)
}

func TestSessionAssertionHistoryMigrationFailureIsAtomic(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec(assertionHistoryDDL + `
DROP TABLE memory_assertion_revision_parents;
DROP TABLE memory_assertion_revision_sources;
INSERT INTO memory_assertion_state VALUES('a',1,0,0);
INSERT INTO memory_assertion_revisions VALUES('a',1,'scene:s',NULL,'key','hash','malformed');`); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		if err := s.EnsureAssertionHistorySchema(); err == nil || !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatal("backfill should fail", err)
		}
		_, err := s.Exec("UPDATE data SET value='continued'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_assertion_revision_parents", "memory_assertion_revision_sources"} {
		if schemaTableExists(t, f.main, table) {
			t.Fatal("caught failed history migration retained DDL", table)
		}
	}
	var value string
	if err := f.main.QueryRow("SELECT value FROM memory_assertion_revisions").Scan(&value); err != nil || value != "malformed" {
		t.Fatal(value, err)
	}
	assertMain(t, f, "continued", 0, 1)
}

func TestSessionMemoryStorageMigrationPreservesLegacyMaterial(t *testing.T) {
	f := newWriterFixture(t)
	if _, err := f.main.Exec(memoryStorageDDL + memorySupportsDDL); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ id, value, want string }{
		{"learned-missing", `{"origin":"learned","unknown":{"n":3},"sources":[{"id":"retained"}]}`, `{"origin":"learned","unknown":{"n":3},"sources":[{"id":"retained"}],"lineageStatus":"unverified"}`},
		{"learned-null", `{"lineageStatus":null,"origin":"learned"}`, `{"lineageStatus":"unverified","origin":"learned"}`},
		{"learned-verified", ` { "origin":"learned", "lineageStatus":"verified", "opaque":"keep" } `, ` { "origin":"learned", "lineageStatus":"verified", "opaque":"keep" } `},
		{"manual", ` { "origin":"manual" } `, ` { "origin":"manual" } `},
		{"learned-empty", `{"origin":"learned","lineageStatus":""}`, `{"origin":"learned","lineageStatus":""}`},
	}
	for _, tc := range cases {
		if _, err := f.main.Exec("INSERT INTO memories VALUES(?,'scene:s',NULL,'content',?,?)", tc.id, tc.id, tc.value); err != nil {
			t.Fatal(err)
		}
	}
	const support = `[{"parents":["p2","p1"],"opaque":{"future":true}},{"sources":[]}]`
	if _, err := f.main.Exec("INSERT INTO memory_supports VALUES('learned-missing',?)", support); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error { return s.EnsureMemoryStorageSchema() }); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			var value string
			if err := f.main.QueryRow("SELECT value FROM memories WHERE id=?", tc.id).Scan(&value); err != nil || value != tc.want {
				t.Fatal(tc.id, value, err)
			}
		}
	}
	var value string
	if err := f.main.QueryRow("SELECT value FROM memory_supports").Scan(&value); err != nil || value != support {
		t.Fatal(value, err)
	}
	if schemaTableExists(t, f.main, "memory_assertion_state") || schemaTableExists(t, f.main, "source_event_heads") {
		t.Fatal("storage migration created unrelated history or replayed sources")
	}
	assertMain(t, f, "original", 0, 1)
}

func TestSessionMemoryStorageCaughtMigrationFailureRetainsOnlyBaseDDL(t *testing.T) {
	for _, caught := range []bool{false, true} {
		t.Run(map[bool]string{false: "outer-rollback", true: "caught"}[caught], func(t *testing.T) {
			f := newWriterFixture(t)
			// Deliberately only the preexisting current table, no base auxiliaries.
			if _, err := f.main.Exec(`CREATE TABLE memories (id TEXT PRIMARY KEY,scope_key TEXT NOT NULL,person_id TEXT,content TEXT NOT NULL,fingerprint TEXT NOT NULL UNIQUE,value TEXT NOT NULL);
INSERT INTO memories VALUES('a','scene:s',NULL,'legacy','fp','malformed');`); err != nil {
				t.Fatal(err)
			}
			err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
				err := s.EnsureMemoryStorageSchema()
				if err == nil || !strings.Contains(err.Error(), "malformed JSON") {
					t.Fatal("expected source JSON failure", err)
				}
				if caught {
					return nil
				}
				return err
			})
			if (err == nil) != caught {
				t.Fatal(err)
			}
			for _, table := range []string{"memory_sources", "memory_deleted", "vector_spaces", "memory_vectors"} {
				if schemaTableExists(t, f.main, table) != caught {
					t.Fatal("base DDL boundary", table, caught)
				}
			}
			if schemaTableExists(t, f.main, "memory_supports") {
				t.Fatal("failed supports/lineage savepoint retained support table")
			}
			var value string
			if err := f.main.QueryRow("SELECT value FROM memories").Scan(&value); err != nil || value != "malformed" {
				t.Fatal(value, err)
			}
		})
	}
}

func TestSessionAssertionSchemasAreExplicitAndTransactional(t *testing.T) {
	f := newWriterFixture(t)
	wantErr := errors.New("synthetic-after-initialization")
	var retained *WriteSession
	err := f.authority.WithWriteSession(context.Background(), f.main, func(s *WriteSession) error {
		retained = s
		if rows, err := s.Query("SELECT version FROM memory_assertion_state"); err == nil {
			rows.Close()
			t.Fatal("query implicitly initialized history")
		}
		if err := s.EnsureMemoryStorageSchema(); err != nil {
			return err
		}
		if err := s.EnsureAssertionHistorySchema(); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatal(err)
	}
	for _, table := range []string{"memories", "memory_supports", "memory_vectors", "memory_assertion_state", "memory_assertion_revision_parents"} {
		if schemaTableExists(t, f.main, table) {
			t.Fatal("root rollback retained schema", table)
		}
	}
	if err := retained.EnsureAssertionHistorySchema(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
	if err := retained.EnsureMemoryStorageSchema(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
}
