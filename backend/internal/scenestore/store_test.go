package scenestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
)

func fixtureStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if err := os.Mkdir(authorityPath, 0700); err != nil {
		t.Fatal(err)
	}
	authorityDB, err := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorityDB.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1); CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	authorityDB.Close()
	if err = os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	main, err := sql.Open("sqlite3", filepath.Join(root, "data", "main.sqlite")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	_, err = main.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	gate, _ := json.Marshal(recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"})
	if _, err = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(gate)); err != nil {
		t.Fatal(err)
	}
	authority, err := recovery.OpenWriter(context.Background(), authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authority.Close(); main.Close() })
	return &Store{main, authority}
}
func sceneInput(t *testing.T, account string) *jsjson.Object {
	t.Helper()
	raw := `{"id":"scene","kind":"group","adapterId":"adapter","accountId":"bot","platformSceneId":"chat","name":"Fixture","participationEnabled":true,"learningEnabled":true,"proactiveEnabled":false}`
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*jsjson.Object)
	if err = o.Set("accountId", account); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestSceneWriteTransactionsAndConcurrentRevision(t *testing.T) {
	store := fixtureStore(t)
	ctx := context.Background()
	if _, err := store.Save(ctx, sceneInput(t, "initial")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, account := range []string{"second", "third"} {
		wg.Add(1)
		go func(account string) {
			defer wg.Done()
			_, err := store.Save(ctx, sceneInput(t, account))
			failures <- err
		}(account)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err := store.Main.QueryRow("SELECT value FROM scenes WHERE id='scene'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	parsed, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if value(parsed.(*jsjson.Object), "memoryReadRevision") != float64(2) {
		t.Fatal(raw)
	}
	gate, err := recovery.ReadGate(ctx, store.Main)
	if err != nil || gate.MinimumReader != 5 || gate.Head.Sequence != 2 {
		t.Fatal(gate, err)
	}
	// A lone surrogate in an identity component is JSON escaped and hashed
	// losslessly; it is not used directly as a SQLite key.
	account := jsjson.FromUTF16([]uint16{0xd800})
	if _, err = store.Save(ctx, sceneInput(t, account)); err != nil {
		t.Fatal(err)
	}
	if err = store.Main.QueryRow("SELECT value FROM scenes WHERE id='scene'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `\ud800`) {
		t.Fatal("identity string lost", raw)
	}
}

func TestActualNodeSceneSQLKeyIdentity(t *testing.T) {
	raw, err := os.ReadFile("testdata/write.json")
	if err != nil {
		t.Fatal(err)
	}
	type record struct {
		KeyHex string
		Value  string
	}
	var fixture struct {
		KeyCases []struct {
			Input  json.RawMessage
			Rows   []record
			Events []string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	store := fixtureStore(t)
	ctx := context.Background()
	gate, err := recovery.ReadGate(ctx, store.Main)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for i, entry := range fixture.KeyCases {
		before, err := store.Authority.Head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := jsjson.ParseBytes(entry.Input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Save(ctx, parsed.(*jsjson.Object)); err != nil {
			t.Fatalf("case%d: %v", i, err)
		}
		rows, err := store.Main.Query("SELECT hex(id),value FROM scenes ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		actual := []record{}
		for rows.Next() {
			var item record
			if err = rows.Scan(&item.KeyHex, &item.Value); err != nil {
				t.Fatal(err)
			}
			actual = append(actual, item)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		got, _ := json.Marshal(actual)
		want, _ := json.Marshal(entry.Rows)
		if string(got) != string(want) {
			t.Fatalf("case%d SQL rows mismatch\n%s\n%s", i, got, want)
		}
		rows, err = ledger.Query("SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", before.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		payloads := []string{}
		for rows.Next() {
			var payload string
			if err = rows.Scan(&payload); err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, payload)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		got, _ = json.Marshal(payloads)
		want, _ = json.Marshal(entry.Events)
		if string(got) != string(want) {
			t.Fatalf("case%d event bytes mismatch\n%s\n%s", i, got, want)
		}
	}
}
