package memeservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"reflect"
	"strings"
	"testing"
)

func TestSplitDurableSourceOutcomes(t *testing.T) {
	oracle := readObjectFixture(t, "testdata/durable-oracle.json")
	for _, v := range arr(get(oracle, "cases")) {
		tc := obj(v)
		name := str(get(tc, "name"))
		t.Run(name, func(t *testing.T) {
			f := newMemeFixture(t)
			ctx, release, e := f.authority.OperationContext(context.Background(), f.main)
			if e != nil {
				t.Fatal(e)
			}
			defer release()
			if _, e := f.main.Exec("PRAGMA foreign_keys=ON;CREATE TABLE effects(id TEXT PRIMARY KEY);CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)"); e != nil {
				t.Fatal(e)
			}
			var ticket *recovery.DurableTicket
			for _, v := range arr(get(tc, "steps")) {
				step := obj(v)
				action := str(get(step, "action"))
				var err error
				switch action {
				case "begin":
					if name == "begin-in-transaction" {
						err = f.authority.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
							_, e := f.authority.BeginDurable(ctx, f.main, func(*recovery.WriteSession) (recovery.Revocation, error) {
								return recovery.Source{SceneID: "s", MessageID: "m"}, nil
							})
							return e
						})
						break
					}
					ticket, err = f.authority.BeginDurable(ctx, f.main, func(ws *recovery.WriteSession) (recovery.Revocation, error) {
						return recovery.Source{SceneID: "s", MessageID: "m"}, nil
					})
				case "commit", "retry":
					id := "one"
					if action == "retry" {
						id = "two"
					}
					err = f.authority.CommitDurable(ctx, f.main, ticket, func(ws *recovery.WriteSession) error { _, e := ws.Exec("INSERT INTO effects VALUES(?)", id); return e })
				case "failure":
					err = f.authority.CommitDurable(ctx, f.main, ticket, func(ws *recovery.WriteSession) error {
						if _, e := ws.Exec("INSERT INTO effects VALUES('one')"); e != nil {
							return e
						}
						return errors.New("synthetic_apply_failure")
					})
				case "commit-failure":
					err = f.authority.CommitDurable(ctx, f.main, ticket, func(ws *recovery.WriteSession) error { _, e := ws.Exec("INSERT INTO child VALUES(99)"); return e })
				case "repeat":
					err = f.authority.CommitDurable(ctx, f.main, ticket, func(ws *recovery.WriteSession) error { return nil })
				case "copy": // Copy the complete VALID value, including private self/owner/head fields.
					// Reflection performs a real value copy without a misleading vet copylock warning.
					copied := new(recovery.DurableTicket)
					reflect.ValueOf(copied).Elem().Set(reflect.ValueOf(ticket).Elem())
					err = f.authority.CommitDurable(ctx, f.main, copied, func(ws *recovery.WriteSession) error { return nil })
				case "foreign":
					other, e := sql.Open("sqlite3", ":memory:")
					if e != nil {
						t.Fatal(e)
					}
					err = f.authority.CommitDurable(ctx, other, ticket, func(ws *recovery.WriteSession) error { return nil })
					other.Close()
				case "changed":
					head, e := f.authority.Head(ctx)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = f.authority.Append(ctx, recovery.Source{SceneID: "s", MessageID: "new"}, head, true); e != nil {
						t.Fatal(e)
					}
					err = f.authority.CommitDurable(ctx, f.main, ticket, func(ws *recovery.WriteSession) error { return nil })
				}
				want := str(get(step, "error"))
				if want == "" && err != nil {
					t.Fatalf("%s: %v", action, err)
				}
				if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
					t.Fatalf("%s error %v want %s", action, err, want)
				}
				var gate string
				if e := f.main.QueryRow("SELECT value FROM recovery_gate").Scan(&gate); e != nil {
					t.Fatal(e)
				}
				g := mustObject(t, gate)
				head, e := f.authority.Head(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if num(get(obj(get(g, "head")), "sequence")) != num(get(step, "main")) || float64(head.Sequence) != num(get(step, "authority")) {
					t.Fatalf("%s head mismatch %s %#v", action, gate, head)
				}
				rows, e := f.main.Query("SELECT id FROM effects ORDER BY id")
				if e != nil {
					t.Fatal(e)
				}
				effects := []any{}
				for rows.Next() {
					var id string
					if e = rows.Scan(&id); e != nil {
						t.Fatal(e)
					}
					effects = append(effects, id)
				}
				rows.Close()
				if stringify(effects) != stringify(get(step, "effects")) {
					t.Fatalf("effects %v", effects)
				}
				var count int
				if e = f.main.QueryRow("SELECT COUNT(*) FROM child").Scan(&count); e != nil {
					t.Fatal(e)
				}
				if float64(count) != num(get(step, "child")) {
					t.Fatal("commit failure leaked child")
				}
			}
		})
	}
}
