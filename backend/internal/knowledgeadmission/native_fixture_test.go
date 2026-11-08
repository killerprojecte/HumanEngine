package knowledgeadmission

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
)

type Object = jsjson.Object

type scopedFixture struct {
	main     *sql.DB
	recovery *recovery.Authority
	store    *assertions.Store
	events   *sourceevents.Store
}

func scopedSetup(t *testing.T) scopedFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if e := os.Mkdir(authorityPath, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(root, "live"), 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite")+"?_foreign_keys=on")
	if e != nil {
		t.Fatal(e)
	}
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));INSERT INTO scenes VALUES('s','{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":true}')`)
	if e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, _ := json.Marshal(gate)
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	ra, e := recovery.OpenWriter(ctx, authorityPath)
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(main, ra, assertions.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	events, e := sourceevents.New(main, ra, sourceevents.Options{ID: func() string { return "source-event" }, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ra.Close(); main.Close() })
	return scopedFixture{main, ra, store, events}
}
func parseObject(t *testing.T, raw string) *Object {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*Object)
}
func testOptions(t *testing.T) assertionauthority.Options {
	t.Helper()
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	return assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, NowMillis: func() int64 { return 1793188800000 }}
}
