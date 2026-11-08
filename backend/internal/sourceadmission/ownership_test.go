package sourceadmission

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"strings"
	"testing"
)

func TestAdmissionSameChildEditsRecallAndExpiry(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	var escaped *Admission
	c := parseObject(t, `{"kind":"fact","content":"Synthetic","sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"subject":{"kind":"self","sourceMessageId":"m"}}`)
	input := parseObject(t, `{"sceneId":"s","simulation":false,"provenance":"source-only","sourceEvidence":[{"sceneId":"s","messageId":"m","senderId":"p","available":true,"isBot":false}]}`)
	identity := sourceevents.Identity{SceneID: "s", AdapterID: "adapter", AccountID: "account", MessageID: "m", SenderID: "p"}
	e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
			a, e := New(reader, testOptions(t))
			if e != nil {
				return e
			}
			escaped = a
			check := func(want string) {
				t.Helper()
				r, e := a.Prepare(input, c)
				if e != nil {
					t.Fatal(e)
				}
				got := r.Reason
				if r.Status == "accepted" {
					got = r.Status
					if get(get(r.Proof, "subjectWitness"), "kind") != "self" {
						t.Fatal("missing actual witness")
					}
				}
				if got != want {
					t.Fatalf("got %s want %s", got, want)
				}
			}
			check("accepted")
			first, e := f.events.WithSession(session).Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("1", 64)})
			if e != nil {
				return e
			}
			check("accepted") // registered legacy source remains eligible before any edit.
			ev := object(arr(get(input, "sourceEvidence"))[0])
			_ = ev.Set("eventRef", first.Ref.Value())
			check("accepted")
			updated, e := f.events.WithSession(session).Observe(ctx, sourceevents.Change{Kind: "edit", Identity: identity, BodyHash: strings.Repeat("2", 64), EditID: "edit", Order: sourceevents.Order{Kind: "platform_sequence", Sequence: 1}})
			if e != nil {
				return e
			}
			check("source_event_changed")
			// A missing ref after an edit cannot rebind to preview/current head.
			_ = ev.Set("eventRef", nil)
			check("source_event_changed")
			_ = ev.Set("eventRef", updated.Ref.Value())
			check("accepted")
			if e := session.WithSavepoint(func(child *recovery.WriteSession) error {
				if _, e := a.Prepare(input, c); e == nil {
					t.Fatal("ancestor admission allowed while child active")
				}
				return f.store.WithSession(child).Read(ctx, func(cr *assertions.Reader) error {
					ca, e := New(cr, testOptions(t))
					if e != nil {
						return e
					}
					r, e := ca.Prepare(input, c)
					if e != nil {
						return e
					}
					if r.Status != "accepted" {
						t.Fatal(r)
					}
					return nil
				})
			}); e != nil {
				return e
			}
			check("accepted")
			if _, e = session.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"other","learningEnabled":true}'`); e != nil {
				return e
			}
			check("source_event_changed")
			if _, e = session.Exec("INSERT INTO recall_tombstones VALUES('s','m','2026-01-01T00:00:00Z')"); e != nil {
				return e
			}
			check("invalid_source_evidence")
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := escaped.KnowledgeSource(input, "m"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped source: %v", e)
	}
	if _, e := escaped.KnowledgeSpans(input, obj("sourceSpans", []any{}), nil); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped empty spans: %v", e)
	}
	if _, e := escaped.Prepare(input, c); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped admission: %v", e)
	}
}
