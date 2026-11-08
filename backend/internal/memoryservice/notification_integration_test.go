package memoryservice

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// This test drives the real revision owner and current provenance authority.
// The explicit test dispatcher advances only after the transaction returns;
// episode discovery is deliberately outside this assertion-only fixture.
func TestNotificationActualCommitAndEligibilityRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "commit"
		if fail {
			name = "eligibility-rollback"
		}
		t.Run(name, func(t *testing.T) {
			base, f := serviceSetup(t)
			ctx := context.Background()
			var service *Service
			stack := []func(){}
			delivered, failed := 0, 0
			queue, e := NewNotificationQueue(NotificationPorts{
				Enabled: func(string) bool { return true }, PolicyIdentity: func(string) string { return "fixed-synthetic-policy" },
				Current: func(scene string, ref Reference) (*Object, error) {
					return service.SemanticCurrent(ctx, Context{SceneID: scene}, ref)
				},
				Episodes:  func(string, Reference) ([]*Object, error) { return []*Object{}, nil },
				PostStack: func(fn func()) { stack = append(stack, fn) }, Failed: func() { failed++ },
			})
			if e != nil {
				t.Fatal(e)
			}
			_, e = queue.Subscribe(func(dirty *Object) error {
				delivered++
				if text(object(value(dirty, "trigger")), "dependencyKey") == "" {
					t.Fatal("missing actual provenance dependency")
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			d := base.d
			d.Notifications = queue
			service, e = New(d)
			if e != nil {
				t.Fatal(e)
			}
			record := putRecord(t, f, "queued", `,"lineageStatus":"quarantined"`)
			source := parseObject(t, `{"sceneId":"s","messageId":"message","senderId":"p"}`)
			_ = record.Set("sources", []any{source})
			raw, e := jsjson.Stringify(record)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.main.Exec("UPDATE memories SET value=? WHERE id='queued'", raw); e != nil {
				t.Fatal(e)
			}
			if _, e = f.main.Exec(`INSERT INTO memory_supports(memory_id,value) VALUES('queued','[{"sources":[{"sceneId":"s","messageId":"message","senderId":"p"}],"parents":[],"parentReferences":[]}]')`); e != nil {
				t.Fatal(e)
			}
			if fail {
				gate, e := recovery.ReadGate(ctx, f.main)
				if e != nil {
					t.Fatal(e)
				}
				external, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
				if e != nil {
					t.Fatal(e)
				}
				_, e = external.Exec(`CREATE TRIGGER synthetic_eligibility_failure BEFORE INSERT ON revocations WHEN json_extract(NEW.payload,'$.kind')='assertion_eligibility' BEGIN SELECT RAISE(ABORT,'synthetic eligibility failure'); END`)
				closeErr := external.Close()
				if e != nil {
					t.Fatal(e)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			restored := record.Clone()
			_ = restored.Set("lineageStatus", "verified")
			e = service.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error { return tx.Repository.Write(restored) })
			if fail {
				if e == nil || !strings.Contains(e.Error(), "synthetic eligibility failure") {
					t.Fatalf("expected actual late failure: %v", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			if delivered != 0 || len(stack) != 1 {
				t.Fatalf("not queued exactly once after owner stack: %d %d", delivered, len(stack))
			}
			stack[0]()
			expected := 1
			if fail {
				expected = 0
			}
			if delivered != expected || failed != 0 {
				t.Fatalf("delivery=%d failed=%d", delivered, failed)
			}
			if e = f.store.Read(ctx, func(read *assertions.Reader) error {
				current, e := read.Record("queued")
				if e != nil {
					return e
				}
				want := "verified"
				if fail {
					want = "quarantined"
				}
				if text(current, "lineageStatus") != want {
					t.Fatal("local eligibility result differs", current)
				}
				state, e := read.State("queued")
				if e != nil {
					return e
				}
				if (!fail && state.Version != 1) || (fail && state.Version != 0) {
					t.Fatal("checkpoint escaped owner outcome", state)
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
