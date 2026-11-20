package recovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestOrderedWriteReachedPrefix(t *testing.T) {
	for _, point := range []string{"success", "prepare-first", "prepare-second", "sql-first", "sql-second", "ack", "before-main-commit", "main-commit-unknown", "authority-commit-unknown", "cancel-after-first-intent"} {
		t.Run(point, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, e := AuthorityPath(f.mainPath, f.gate.Authority); e != nil {
				t.Fatal(e)
			}
			if point == "ack" {
				if _, e := f.main.Exec("CREATE TRIGGER fail_ack BEFORE UPDATE ON recovery_gate WHEN NEW.value != OLD.value BEGIN SELECT RAISE(ABORT,'synthetic_ack_failure'); END"); e != nil {
					t.Fatal(e)
				}
			}
			prepared, applied := 0, 0
			injected := errors.New("synthetic_ordered_failure")
			hooks := writeHooks{}
			if point == "before-main-commit" {
				hooks.beforeMainCommit = func() error { return injected }
			}
			if point == "cancel-after-first-intent" {
				hooks.afterIntent = func(Head) error { cancel(); return nil }
			}
			if point == "main-commit-unknown" || point == "authority-commit-unknown" {
				hooks.commit = func(ctx context.Context, c *sql.Conn, db string) error {
					if e := execCommit(ctx, c, db); e != nil {
						return e
					}
					if point == db+"-commit-unknown" {
						return injected
					}
					return nil
				}
			}
			var retained *MainMutation
			_, err := f.authority.executeOrderedWrite(ctx, f.main, func(*MainRead) (OrderedWritePlan, error) {
				p := OrderedWritePlan{MinimumReader: 1}
				for i := 0; i < 3; i++ {
					i := i
					p.Steps = append(p.Steps, OrderedWriteStep{
						BeforeIntent: func() error {
							prepared++
							if point == "prepare-first" && i == 0 || point == "prepare-second" && i == 1 {
								return injected
							}
							return nil
						},
						Intents: []Revocation{Source{SceneID: "s", MessageID: fmt.Sprintf("ordered-%d", i)}},
						Apply: func(m *MainMutation) error {
							retained = m
							applied++
							if _, e := m.Exec("UPDATE data SET value=?", fmt.Sprintf("step-%d", i)); e != nil {
								return e
							}
							if point == "sql-first" && i == 0 || point == "sql-second" && i == 1 {
								return injected
							}
							return nil
						},
					})
				}
				return p, nil
			}, hooks)
			if point == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("fault accepted")
			}
			if retained != nil {
				if _, e := retained.Exec("UPDATE data SET value='escaped'"); !errors.Is(e, sql.ErrTxDone) {
					t.Fatalf("retained capability: %v", e)
				}
			}
			head, e := f.authority.Head(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			want := int64(3)
			switch point {
			case "prepare-first":
				want = 0
			case "prepare-second", "sql-first", "authority-commit-unknown", "cancel-after-first-intent":
				want = 1
			case "sql-second":
				want = 2
			}
			if head.Sequence != want {
				t.Fatalf("durable prefix %d want %d prepared=%d applied=%d", head.Sequence, want, prepared, applied)
			}
			if point == "success" || point == "main-commit-unknown" {
				assertMain(t, f, "step-2", 3, 1)
			} else {
				assertMain(t, f, "original", 0, 1)
				if want > 0 {
					assertRestartGap(t, f)
				}
			}
			if point == "main-commit-unknown" || point == "authority-commit-unknown" {
				if !errors.Is(err, ErrCommitOutcomeUnknown) {
					t.Fatalf("uncertain commit not classified: %v", err)
				}
			}
			if point == "sql-first" && prepared != 1 {
				t.Fatalf("prepared later key custody after SQL failure: %d", prepared)
			}
		})
	}
}

func TestOrdinaryOrderedWriteUsesNormalReservation(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		before, intent, fail bool
		stale                bool
		begins, durable      int
	}{
		{name: "no intent", begins: 1},
		{name: "invalid plan", before: true},
		{name: "stale head", stale: true, begins: 1, durable: 1},
		{name: "failed after intent", intent: true, fail: true, begins: 2, durable: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx := context.Background()
			if tc.stale {
				if _, err := f.authority.Append(ctx, Source{"s", "independent"}, f.gate.Head, false); err != nil {
					t.Fatal(err)
				}
			}
			begins, applied := 0, false
			if err := f.authority.conn.Raw(func(raw any) error {
				raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(func(op int, operation, _, _ string) int {
					if op == sqlite3.SQLITE_TRANSACTION && operation == "BEGIN" {
						begins++
					}
					return sqlite3.SQLITE_OK
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("synthetic_ordinary_write_failure")
			_, err := f.authority.PrepareOrderedWrite(ctx, f.main, func(*MainRead) (OrderedWritePlan, error) {
				step := OrderedWriteStep{Apply: func(m *MainMutation) error {
					applied = true
					if _, err := m.Exec("UPDATE data SET value='written'"); err != nil {
						return err
					}
					if tc.fail {
						return injected
					}
					return nil
				}}
				if tc.intent {
					step.Intents = []Revocation{Source{"s", "write"}}
				}
				if tc.before {
					return OrderedWritePlan{}, injected
				}
				return OrderedWritePlan{MinimumReader: 1, Steps: []OrderedWriteStep{step}}, nil
			})
			observed := begins
			if err := f.authority.conn.Raw(func(raw any) error {
				raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(nil)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.stale:
				if err == nil || err.Error() != "recovery_authority_head_changed" {
					t.Fatalf("stale write returned %v", err)
				}
			case tc.before || tc.fail:
				if !errors.Is(err, injected) {
					t.Fatalf("injected failure returned %v", err)
				}
			case err != nil:
				t.Fatal(err)
			}
			if observed != tc.begins || applied != (!tc.before && !tc.stale) {
				t.Fatalf("reservations=%d, callback=%t; want %d, %t", observed, applied, tc.begins, !tc.before && !tc.stale)
			}
			head, err := f.authority.Head(ctx)
			if err != nil || head.Sequence != int64(tc.durable) {
				t.Fatalf("unexpected durable head %+v: %v", head, err)
			}
			if tc.before || tc.stale || tc.fail {
				assertMain(t, f, "original", 0, 1)
			} else {
				assertMain(t, f, "written", 0, 1)
			}
		})
	}
}
