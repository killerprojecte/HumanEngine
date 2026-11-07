package sourceevents

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestActualSourceReviewRegressions(t *testing.T) {
	type callback struct {
		Phase, Kind          string
		OccurredAt, HeadHash *string
	}
	var oracle struct {
		ReviewCases []struct {
			Name, Callback, InputOccurredAt, Rows string
			Recall, CatchInside                   bool
			Caught, OuterError                    *string
			Callbacks                             []callback
			Gate                                  recovery.Gate
			AuthorityHead                         recovery.Head
		}
	}
	b, e := os.ReadFile("testdata/source-event-recovery.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &oracle); e != nil {
		t.Fatal(e)
	}
	if len(oracle.ReviewCases) != 7 {
		t.Fatal("missing actual-source review regressions", len(oracle.ReviewCases))
	}
	for _, c := range oracle.ReviewCases {
		t.Run(c.Name, func(t *testing.T) {
			ctx := context.Background()
			input := edit(1)
			if c.Callback == "id" {
				input = ingress()
			}
			occurredAt := "2026-10-28T01:02:03.000Z"
			input.OccurredAt = &occurredAt
			armed := false
			var callbacks []callback
			mutate := func(phase string) {
				if armed && c.Callback == phase {
					occurredAt = "2026-10-29T04:05:06.000Z"
				}
			}
			record := func(phase string, m Mutation) {
				if !armed {
					return
				}
				x := callback{Phase: phase, Kind: m.Kind}
				if m.Metadata != nil && m.Metadata.OccurredAt != nil {
					at := *m.Metadata.OccurredAt
					x.OccurredAt = &at
				}
				if m.HeadHash != "" {
					hash := m.HeadHash
					x.HeadHash = &hash
				}
				callbacks = append(callbacks, x)
			}
			f := newFixture(t, Options{
				ID:          func() string { mutate("id"); return "event-1" },
				Now:         func() string { mutate("now"); return "2026-10-28T00:00:00.000Z" },
				AssertReady: func() error { mutate("readiness"); return nil },
				BeforeWrite: func(s *Store, m Mutation) error {
					mutate("before-write")
					record("before-write", m)
					return s.RecordMutation(m)
				},
			})
			f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
				mutate("subscriber")
				record("subscriber", m)
				return nil
			}})
			f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
				record("second-subscriber", m)
				return nil
			}})
			if c.Callback != "id" {
				if _, e := f.store.Observe(ctx, ingress()); e != nil {
					t.Fatal(e)
				}
			}
			if c.Recall {
				if _, e := f.main.Exec("INSERT INTO source_event_heads VALUES(?,?,?,?,?)", "corrupt-later", "corrupt-2", "scene", "message", "{"); e != nil {
					t.Fatal(e)
				}
				rows, e := f.main.Query("SELECT event_id FROM source_event_heads WHERE scene_id=? AND message_id=?", "scene", "message")
				if e != nil {
					t.Fatal(e)
				}
				var ids []string
				for rows.Next() {
					var id string
					if e = rows.Scan(&id); e != nil {
						t.Fatal(e)
					}
					ids = append(ids, id)
				}
				if e = rows.Err(); e != nil {
					t.Fatal(e)
				}
				if e = rows.Close(); e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(ids, []string{"event-1", "corrupt-2"}) {
					t.Fatal("fixture recall order", ids)
				}
			}
			armed = true
			var caught error
			e := f.authority.WithWriteSession(ctx, f.main, func(tx *recovery.WriteSession) error {
				s := f.store.WithSession(tx)
				var err error
				if c.Recall {
					err = s.RevokeAll(ctx, "scene", "message")
				} else {
					_, err = s.Observe(ctx, input)
				}
				if c.CatchInside {
					caught = err
					return nil
				}
				return err
			})
			checkError := func(label string, actual error, expected *string) {
				t.Helper()
				if (actual != nil) != (expected != nil) || (actual != nil && actual.Error() != *expected) {
					t.Fatalf("%s: got %v, want %v", label, actual, expected)
				}
			}
			checkError("caught", caught, c.Caught)
			checkError("outer", e, c.OuterError)
			if !reflect.DeepEqual(callbacks, c.Callbacks) {
				t.Fatalf("callback snapshots: got %+v, want %+v", callbacks, c.Callbacks)
			}
			if occurredAt != c.InputOccurredAt {
				t.Fatal("caller input mutation", occurredAt, c.InputOccurredAt)
			}
			if actual := dump(t, f.main); actual != c.Rows {
				t.Fatalf("SQLite rows differ\nactual %s\nexpected %s", actual, c.Rows)
			}
			gate, e := recovery.ReadGate(ctx, f.main)
			if e != nil || gate.Head != c.Gate.Head || gate.MinimumReader != c.Gate.MinimumReader {
				t.Fatal("main gate", gate, c.Gate, e)
			}
			head, e := f.authority.Head(ctx)
			if e != nil || head != c.AuthorityHead {
				t.Fatal("durable intent", head, c.AuthorityHead, e)
			}
		})
	}
}
