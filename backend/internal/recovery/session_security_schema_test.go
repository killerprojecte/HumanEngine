package recovery

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOrderedSecuritySchemaExactLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "before-intent", "bad-schema", "later-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriterFixture(t)
			ctx := context.Background()
			if mode == "bad-schema" {
				if _, e := f.main.Exec("CREATE TABLE recovery_security_state(id INTEGER PRIMARY KEY,revision TEXT)"); e != nil {
					t.Fatal(e)
				}
			}
			before, e := f.authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			injected := errors.New("synthetic_security_failure")
			_, e = f.authority.PrepareOrderedWrite(ctx, f.main, func(*MainRead) (OrderedWritePlan, error) {
				return OrderedWritePlan{MinimumReader: 1, Steps: []OrderedWriteStep{{EnsureSecurityState: true,
					BeforeIntent: func() error {
						if mode == "before-intent" {
							return injected
						}
						return nil
					},
					Intents: []Revocation{SecurityState{Revision: "12345678-1234-4123-8123-123456789012"}},
					Apply: func(m *MainMutation) error {
						if _, err := m.Exec("INSERT INTO recovery_security_state VALUES(1,?)", "12345678-1234-4123-8123-123456789012"); err != nil {
							return err
						}
						if mode == "later-failure" {
							return injected
						}
						return nil
					},
				}}}, nil
			})
			if mode == "success" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("failure accepted")
			}
			after, e := f.authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			want := before.Sequence
			if mode == "success" || mode == "later-failure" {
				want++
			}
			if after.Sequence != want {
				t.Fatalf("intent sequence=%d want%d", after.Sequence, want)
			}
			var n int
			if e = f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='recovery_security_state'").Scan(&n); e != nil {
				t.Fatal(e)
			}
			if (mode == "before-intent" || mode == "later-failure") && n != 0 {
				t.Fatal("DDL escaped rollback", n)
			}
			gate, e := ReadGate(ctx, f.main)
			if e != nil {
				t.Fatal(e)
			}
			if mode != "success" && gate.Head != before {
				t.Fatal("failed transaction advanced gate")
			}
			if mode == "success" {
				var oldVersion, newVersion int
				f.main.QueryRow("PRAGMA schema_version").Scan(&oldVersion)
				e = f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error { return s.EnsureSecurityStateSchema() })
				if e != nil {
					t.Fatal(e)
				}
				f.main.QueryRow("PRAGMA schema_version").Scan(&newVersion)
				if newVersion != oldVersion {
					t.Fatal("idempotent ensure changed schema")
				}
			}
		})
	}
}

func TestSecuritySchemaSessionRollbackAndCapabilitySealing(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	injected := errors.New("synthetic_rollback")
	var held *WriteSession
	e := f.authority.WithWriteSession(ctx, f.main, func(s *WriteSession) error {
		held = s
		if e := s.EnsureSecurityStateSchema(); e != nil {
			return e
		}
		return injected
	})
	if !errors.Is(e, injected) {
		t.Fatal(e)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='recovery_security_state'").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if e = held.EnsureSecurityStateSchema(); e == nil {
		t.Fatal("retained schema capability accepted")
	}
}

func TestSecuritySchemaPreservesMinimumAndRestrictions(t *testing.T) {
	for _, minimum := range []int64{1, ReaderCapability} {
		for _, command := range []string{"", "CREATE TABLE escaped(value TEXT)", "COMMIT", "UPDATE recovery_gate SET value=value"} {
			t.Run(fmt.Sprintf("minimum-%d/%s", minimum, command), func(t *testing.T) {
				f := newWriterFixture(t)
				gate := f.gate
				gate.MinimumReader = minimum
				save(t, f.main, gate)
				called := false
				_, err := f.authority.PrepareOrderedWrite(context.Background(), f.main, func(*MainRead) (OrderedWritePlan, error) {
					return OrderedWritePlan{MinimumReader: 1, Steps: []OrderedWriteStep{{EnsureSecurityState: true, Apply: func(m *MainMutation) error {
						called = true
						if command != "" {
							_, e := m.Exec(command)
							if e == nil {
								t.Error("ordinary mutation escaped restored authorizer", command)
							}
							return e
						}
						return nil
					}}}}, nil
				})
				if !called {
					t.Fatal("did not reach ordinary mutation", err)
				}
				if command == "" && err != nil {
					t.Fatal(err)
				}
				if command != "" && err == nil {
					t.Fatal("prohibited SQL accepted")
				}
				after, e := ReadGate(context.Background(), f.main)
				if e != nil {
					t.Fatal(e)
				}
				if after.MinimumReader != minimum {
					t.Fatalf("minimumReader changed %d -> %d", minimum, after.MinimumReader)
				}
				if after.Head != gate.Head {
					t.Fatal("schema-only operation appended intent")
				}
				var count int
				if e = f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='escaped'").Scan(&count); e != nil || count != 0 {
					t.Fatal("DDL escaped", count, e)
				}
			})
		}
	}
}
