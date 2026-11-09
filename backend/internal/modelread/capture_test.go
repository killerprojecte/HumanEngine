package modelread

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
)

func TestCaptureOptionalTicketUsesActualPolicy(t *testing.T) {
	dates, err := jsdate.New("UTC")
	if err != nil {
		if errors.Is(err, nodeicu.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	live := filepath.Join(root, "live")
	if err := os.Mkdir(live, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(live, "main.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	authority, err := recovery.OpenInstallationGate(ctx, db, path, filepath.Join(root, "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if _, err = db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	st, err := assertions.New(db, authority, assertions.Options{DateParser: dates})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := sourceevents.New(db, authority, sourceevents.Options{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewRuntime(st, sources, db, authority)
	if err != nil {
		t.Fatal(err)
	}
	set := func(raw string) {
		t.Helper()
		if _, e := db.Exec("INSERT OR REPLACE INTO scenes VALUES('s',?)", raw); e != nil {
			t.Fatal(e)
		}
	}

	set(`{"id":"s","memoryReadEnabled":false,"memoryReadRevision":1}`)
	ticket, err := policy.Capture(ctx, "s")
	if err != nil || ticket != nil {
		t.Fatalf("disabled Capture = %v, %v", ticket, err)
	}
	if err = policy.Assert(ctx, ticket); err == nil || err.Error() != "memory_read_revoked" {
		t.Fatalf("nil ticket fence: %v", err)
	}
	if _, err = policy.Require(ctx, "s"); err == nil || err.Error() != "memory_read_disabled" {
		t.Fatalf("Require changed: %v", err)
	}
	set(`{"id":"s","memoryReadEnabled":true,"memoryReadRevision":1}`)
	ticket, err = policy.Capture(ctx, "s")
	if err != nil || ticket == nil || !policy.Valid(ctx, ticket) {
		t.Fatalf("actual issued capture invalid: %v", err)
	}
	copied := *ticket
	if policy.Valid(ctx, &copied) {
		t.Fatal("copied ticket gained authority")
	}
	other, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	if other.Valid(ctx, ticket) {
		t.Fatal("foreign owner ticket gained authority")
	}
	set(`{"id":"s","memoryReadEnabled":true,"memoryReadRevision":2}`)
	if policy.Valid(ctx, ticket) {
		t.Fatal("old ticket survived revision")
	}
	set(`{"id":"s","memoryReadEnabled":"invalid","memoryReadRevision":2}`)
	if _, err = policy.Capture(ctx, "s"); err == nil {
		t.Fatal("invalid policy captured")
	}
}
