package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const initialSecurity = "12345678-1234-4123-8123-123456789012"

func secretFixtureStore(t *testing.T, security bool) *Store {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1); CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	main, err := sql.Open("sqlite3", filepath.Join(root, "data", "main.sqlite")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	_, err = main.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if security {
		_, err = main.Exec(`CREATE TABLE IF NOT EXISTS recovery_security_state (id INTEGER PRIMARY KEY CHECK(id=1), revision TEXT NOT NULL); INSERT INTO recovery_security_state VALUES(1,'12345678-1234-4123-8123-123456789012')`)
		if err != nil {
			t.Fatal(err)
		}
	}
	gate, _ := json.Marshal(recovery.Gate{Head: recovery.Head{InstanceID: initialSecurity, Hash: strings.Repeat("0", 64)}, Authority: path, MinimumReader: 1, State: "ready"})
	if _, err = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(gate)); err != nil {
		t.Fatal(err)
	}
	authority, err := recovery.OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authority.Close(); main.Close() })
	store, err := NewStore(context.Background(), main, authority, "")
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func prop(v any, key string) any {
	o, ok := v.(*jsjson.Object)
	if !ok {
		return nil
	}
	x, _ := o.Get(key)
	return x
}
func str(v any) string { x, _ := v.(string); return x }
func fallback(v any, d string) string {
	if v == nil {
		return d
	}
	return str(v)
}
func TestActualSecretStoreOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/secret-store.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := jsjson.Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range prop(root, "cases").([]any) {
		t.Run(str(prop(entry, "name")), func(t *testing.T) {
			absent := prop(entry, "absentSecurity") == true
			s := secretFixtureStore(t, !absent)
			ctx := context.Background()
			ns, key := fallback(prop(entry, "namespace"), "synthetic"), fallback(prop(entry, "key"), "key")
			steps := prop(entry, "steps").([]any)
			previous := initialSecurity
			for index, op := range prop(entry, "ops").([]any) {
				step := steps[index]
				fail := str(prop(entry, "fail"))
				failAt, _ := prop(entry, "failAt").(float64)
				if fail != "" && index == int(failAt) {
					if _, err := s.main.Exec("CREATE TRIGGER fail_secret BEFORE " + fail + " ON secrets BEGIN SELECT RAISE(ABORT,'fixture_secret_write_failed'); END"); err != nil {
						t.Fatal(err)
					}
				}
				currentNS, currentKey := ns, key
				if index > 0 {
					currentNS = fallback(prop(entry, "otherNamespace"), ns)
					currentKey = fallback(prop(entry, "otherKey"), key)
				}
				var mutation *Mutation
				if op != nil {
					mutation = &Mutation{Action: str(prop(op, "action")), Value: prop(op, "value")}
				}
				_, err := s.Apply(ctx, currentNS, currentKey, mutation)
				if absent {
					if !errors.Is(err, ErrSecuritySchemaUnsupported) {
						t.Fatal(err)
					}
					head, _ := s.authority.Head(ctx)
					if head.Sequence != 0 {
						t.Fatal("unsupported bootstrap appended intent")
					}
					return
				}
				wantError := str(prop(step, "error"))
				if (err == nil) != (wantError == "") || err != nil && err.Error() != wantError {
					t.Fatalf("step %d error %v want %s", index, err, wantError)
				}
				var revision string
				if err := s.main.QueryRow("SELECT revision FROM recovery_security_state").Scan(&revision); err != nil {
					t.Fatal(err)
				}
				if (revision != previous) != (prop(step, "rotated") == true) {
					t.Fatal("rotation mismatch")
				}
				previous = revision
				var count int
				if err := s.main.QueryRow("SELECT count(*) FROM secrets").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if float64(count) != prop(step, "count") {
					t.Fatal("rollback/row count mismatch")
				}
				gate, err := recovery.ReadGate(ctx, s.main)
				if err != nil {
					t.Fatal(err)
				}
				head, err := s.authority.Head(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if fail != "" && index == int(failAt) {
					if head.Sequence != gate.Head.Sequence+1 {
						t.Fatal("failed mutation did not leave exactly one durable intent")
					}
					if _, _, err := s.Get(ctx, ns, key); err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") {
						t.Fatal("failed intent published a credential", err)
					}
					var stored string
					if count > 0 {
						if err := s.main.QueryRow("SELECT ciphertext FROM secrets").Scan(&stored); err != nil {
							t.Fatal(err)
						}
						v, err := s.cipher.Decrypt(ns, key, stored)
						if err != nil || v != "old" {
							t.Fatal("prior ciphertext not restored")
						}
					}
					return
				}
				if head != gate.Head {
					t.Fatal("ACK mismatch")
				}
				if head.Sequence > 0 {
					ledger, err := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite")+"?mode=ro")
					if err != nil {
						t.Fatal(err)
					}
					var raw string
					err = ledger.QueryRow("SELECT payload FROM revocations ORDER BY sequence DESC LIMIT 1").Scan(&raw)
					ledger.Close()
					if err != nil {
						t.Fatal(err)
					}
					payload, err := jsjson.Parse(raw)
					if err != nil || str(prop(payload, "revision")) != revision {
						t.Fatal("security revision differs from durable intent")
					}
					if len(payload.(*jsjson.Object).Members()) != 2 || prop(payload, "kind") != "security_state" {
						t.Fatal("authority contains credential metadata")
					}
				}
				for _, r := range prop(step, "reads").([]any) {
					n, k := str(prop(r, "namespace")), str(prop(r, "key"))
					has, err := s.Has(ctx, n, k)
					if err != nil || has != prop(prop(r, "has"), "value") {
						t.Fatal("has mismatch", err)
					}
					value, found, err := s.Get(ctx, n, k)
					expected := prop(r, "get")
					wantErr := str(prop(expected, "error"))
					if (err == nil) != (wantErr == "") || err != nil && err.Error() != wantErr {
						t.Fatal("get error mismatch", err, wantErr)
					}
					if wantErr == "" {
						v := prop(expected, "value")
						if found != (v != nil) || found && value != str(v) {
							t.Fatal("get value mismatch")
						}
					}
				}
				values, err := s.RedactionValues(ctx)
				expected := prop(step, "redaction")
				wantErr := str(prop(expected, "error"))
				if (err == nil) != (wantErr == "") || err != nil && err.Error() != wantErr {
					t.Fatal("redaction error mismatch", err, wantErr)
				}
				if wantErr == "" {
					want := []string{}
					for _, v := range prop(expected, "value").([]any) {
						want = append(want, str(v))
					}
					if !reflect.DeepEqual(values, want) {
						t.Fatal("redaction values mismatch")
					}
				}
			}
		})
	}
}
func TestSecretKeyMissingAndValidationOrdering(t *testing.T) {
	s := secretFixtureStore(t, true)
	s.keyPath = filepath.Join(t.TempDir(), "key")
	ctx := context.Background()
	if _, err := s.Apply(ctx, "ns", "key", &Mutation{Action: "set", Value: "synthetic secret"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.keyPath); err != nil {
		t.Fatal(err)
	}
	s.cipher = nil
	if _, err := s.Apply(ctx, "ns", "new", &Mutation{Action: "set", Value: " "}); !errors.Is(err, ErrInvalidSecret) {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, "ns", "new", &Mutation{Action: "set", Value: "second"}); err == nil || err.Error() != "secret_key_missing" {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, "ns", "missing"); err != nil {
		t.Fatal("missing lookup must not load key", err)
	}
	if _, err := s.Apply(ctx, "ns", "key", &Mutation{Action: "clear"}); err != nil {
		t.Fatal("clear must not load key", err)
	}
	if _, err := s.Apply(ctx, "ns", "new", &Mutation{Action: "set", Value: "second"}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretQueuedCancellation(t *testing.T) {
	s := secretFixtureStore(t, true)
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Has(ctx, "ns", "key"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued call ignored cancellation")
	}
	if _, err := s.Has(ctx, "ns", "key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, "ns", "key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.RedactionValues(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, "ns", "key", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSecretConcurrentStoresAndRevocation(t *testing.T) {
	s := secretFixtureStore(t, true)
	s.keyPath = filepath.Join(t.TempDir(), "key")
	other, err := NewStore(context.Background(), s.main, s.authority, s.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func(i int) {
			store := s
			if i%2 == 1 {
				store = other
			}
			_, err := store.Apply(context.Background(), "ns", "key", &Mutation{Action: "set", Value: "synthetic"})
			results <- err
		}(i)
	}
	for i := 0; i < 12; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	head, err := s.authority.Head(context.Background())
	if err != nil || head.Sequence != 12 {
		t.Fatal("lost intents", err)
	}
	if _, _, err := other.Get(context.Background(), "ns", "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.authority.Append(context.Background(), recovery.SecurityState{Revision: "12345678-1234-4123-8123-123456789013"}, head, false); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []*Mutation{nil, {Action: "preserve"}, {Action: "set", Value: " "}, {Action: "clear"}} {
		if _, err := s.Apply(context.Background(), "ns", "key", mutation); err == nil || !strings.Contains(err.Error(), "recovery_authority_head_changed") {
			t.Fatal("readiness must precede mutation validation", err)
		}
	}
	if _, err := s.Has(context.Background(), "ns", "key"); err == nil {
		t.Fatal("stale has succeeded")
	}
	if _, err := s.RedactionValues(context.Background()); err == nil {
		t.Fatal("stale redaction succeeded")
	}
}

func TestSecretSchemaAndKeySideEffects(t *testing.T) {
	s := secretFixtureStore(t, false)
	s.keyPath = filepath.Join(t.TempDir(), "key")
	// Encryption/key creation precedes the transaction's unsupported-schema error.
	if _, err := s.Apply(context.Background(), "ns", "key", &Mutation{Action: "set", Value: "synthetic"}); !errors.Is(err, ErrSecuritySchemaUnsupported) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.keyPath); err != nil {
		t.Fatal("source first-use key ordering lost", err)
	}
	if _, err := s.Apply(context.Background(), "ns", "key", &Mutation{Action: "clear"}); err != nil {
		t.Fatal("missing clear should not require security table", err)
	}
	if _, err := s.main.Exec("CREATE TABLE recovery_security_state(id INTEGER PRIMARY KEY,revision TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), "ns", "key", &Mutation{Action: "set", Value: "synthetic"}); !errors.Is(err, ErrSecuritySchemaUnsupported) {
		t.Fatal(err)
	}
}
