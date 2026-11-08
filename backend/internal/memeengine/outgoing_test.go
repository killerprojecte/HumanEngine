package memeengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type outgoingFixture struct {
	main      *sql.DB
	authority *recovery.Authority
	events    *sourceevents.Store
}

func newOutgoingFixture(t *testing.T) outgoingFixture {
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
	return outgoingFixture{main, a, events}
}
func TestOutgoingGateAndUsageUseActualScopedOwner(t *testing.T) {
	f := newOutgoingFixture(t)
	ctx := context.Background()
	dates, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	assertions, e := assertions.New(f.main, f.authority, assertions.Options{DateParser: dates})
	if e != nil {
		t.Fatal(e)
	}
	memes, e := memeservice.New(ctx, f.main, f.authority, assertions, assertionauthority.Options{DateParser: dates, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{main: f.main, authority: f.authority, Memes: memes}
	id := strings.Repeat("a", 64)
	if _, e = f.main.Exec(`INSERT INTO scenes VALUES('s','{"id":"s","adapterId":"a","accountId":"c","learningEnabled":true}'); INSERT INTO scenes VALUES('other','{"id":"other","adapterId":"a","accountId":"c","learningEnabled":true}')`); e != nil {
		t.Fatal(e)
	}
	asset := `{"id":"` + id + `","contentHash":"` + id + `","format":"png","bytes":4,"available":true,"usageCount":0,"sources":[{"sceneId":"s","messageId":"m","senderId":"u"}],"tags":{"appearance":[],"style":[],"subject":[],"emotion":[],"intent":[],"tone":[],"intensity":[],"memeMeaning":[],"ocr":""}}`
	if _, e = f.main.Exec("INSERT INTO meme_assets VALUES(?,?)", id, asset); e != nil {
		t.Fatal(e)
	}
	parts := []json.RawMessage{json.RawMessage(`{"type":"media","mediaType":"image","assetId":"` + id + `"}`)}
	segment := dispatch.Segment{Parts: parts}
	hook := s.outgoingHook()
	if hook.ID != "media-scope" || hook.Priority != -100 {
		t.Fatal(hook)
	}
	gate := func(scene, mode string) error {
		var result error
		calls := 0
		hook.Run(ctx, []dispatch.Segment{segment}, &botruntime.TurnContext{Scene: botruntime.Scene{SceneRef: dispatch.SceneRef{ID: scene}}, Mode: mode}, func(_ botruntime.HookResult, e error) { calls++; result = e })
		if calls != 1 {
			t.Fatal("hook not settled exactly once")
		}
		return result
	}
	if e = gate("s", "real"); e != nil {
		t.Fatal(e)
	}
	if e = gate("other", "real"); e == nil || e.Error() != "meme_not_available" {
		t.Fatal("cross-scene original admitted", e)
	}
	if e = gate("s", "simulation"); e == nil || e.Error() != "meme_not_available" {
		t.Fatal("private simulation original admitted", e)
	}
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s"}}
	s.recordOutgoing(scene, "turn", dispatch.Segment{Parts: append(parts, parts...)}, dispatch.Receipt{Status: dispatch.Sent})
	usage := func() int {
		var n int
		if e := f.main.QueryRow("SELECT json_extract(value,'$.usageCount') FROM meme_assets WHERE id=?", id).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if n := usage(); n != 2 {
		t.Fatalf("duplicate source parts usage=%d", n)
	}
	if _, e = f.main.Exec("UPDATE scenes SET value=json_set(value,'$.learningEnabled',json('false')) WHERE id='s'"); e != nil {
		t.Fatal(e)
	}
	if e = gate("s", "real"); e != nil {
		t.Fatal("disabled learning incorrectly blocked reading", e)
	}
	s.recordOutgoing(scene, "turn", segment, dispatch.Receipt{Status: dispatch.Sent})
	if n := usage(); n != 2 {
		t.Fatal("disabled learning persisted usage", n)
	}
	if _, e = f.main.Exec("UPDATE meme_assets SET value=json_set(value,'$.disabled',json('true')) WHERE id=?", id); e != nil {
		t.Fatal(e)
	}
	if e = gate("s", "real"); e == nil || e.Error() != "meme_not_available" {
		t.Fatal("disabled asset admitted", e)
	}
}
