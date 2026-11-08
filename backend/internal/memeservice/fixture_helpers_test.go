package memeservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
)

type memeFixture struct {
	main      *sql.DB
	authority *recovery.Authority
	events    *sourceevents.Store
}

func newMemeFixture(t *testing.T) memeFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	if e := os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL); CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	live := filepath.Join(root, "live")
	if e := os.Mkdir(live, 0700); e != nil {
		t.Fatal(e)
	}
	database := filepath.Join(live, "main.sqlite")
	if actual, e := recovery.AuthorityPath(database, path); e != nil || actual != path {
		t.Fatalf("independent authority fixture: %q %v", actual, e)
	}
	main, e := sql.Open("sqlite3", database)
	if e != nil {
		t.Fatal(e)
	}
	// One connection is a deadlock probe for scoped composition, not a production cap.
	main.SetMaxOpenConns(1)
	t.Cleanup(func() { main.Close() })
	_, e = main.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL); CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))`)
	if e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: path, MinimumReader: 1, State: "ready"}
	raw, _ := json.Marshal(gate)
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	a, e := recovery.OpenWriter(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	serial := 0
	events, e := sourceevents.New(main, a, sourceevents.Options{ID: func() string { serial++; return fmt.Sprintf("event-%d", serial) }, Now: func() string { return "2026-11-02T00:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	return memeFixture{main, a, events}
}
func mustObject(t *testing.T, raw string) *Object {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	o, ok := v.(*Object)
	if !ok {
		t.Fatalf("not an object: %s", raw)
	}
	return o
}
func saveMemeScene(t *testing.T, f memeFixture, o *Object) {
	t.Helper()
	s := scenestore.Store{Main: f.main, Authority: f.authority}
	if _, e := s.Save(context.Background(), o); e != nil {
		t.Fatal(e)
	}
}
