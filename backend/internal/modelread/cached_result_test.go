package modelread

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
)

func TestCachedResultGuardUsesSameReadScopeAndOriginalRevision(t *testing.T) {
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
	set(`{"id":"s","memoryReadEnabled":true,"memoryReadRevision":1}`)
	ticket, err := policy.Require(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	turn := &botruntime.TurnContext{Scene: botruntime.Scene{ID: "s"}, Mode: "real"}
	// Begin stores this exact Policy-issued immutable ticket. Establishing its
	// state directly avoids substituting a permissive fake Runtime authority.
	policy.turn(turn).ticket = ticket
	guard, ok := policy.CaptureScopedGuard(turn)
	if !ok {
		t.Fatal("missing issued guard")
	}
	check := func(want bool) {
		t.Helper()
		err := sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			got, e := guard(reads)
			if e == nil && got != want {
				t.Fatalf("guard=%v want%v", got, want)
			}
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(true)
	set(`{"id":"s","memoryReadEnabled":false,"memoryReadRevision":2}`)
	check(false)
	set(`{"id":"s","memoryReadEnabled":true,"memoryReadRevision":2}`)
	check(false)
	// A new turn can obtain a new ticket, but must never refresh the old cache's
	// snapshot as a side effect of returning to the same scene or enabled state.
	fresh, err := policy.Require(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Valid(ctx, fresh) {
		t.Fatal("new scene policy not usable")
	}
	check(false)
}
