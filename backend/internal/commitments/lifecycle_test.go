package commitments

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestRefreshRealAssertionHistorySameRoot(t *testing.T) {
	f := seeded(t)
	ctx := context.Background()
	_, e := f.main.Exec("CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))")
	must(t, e)
	store, e := assertions.New(f.main, f.ledger, assertions.Options{DateParser: f.store.options.DateParser, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	must(t, e)
	must(t, store.Initialize(ctx))
	// This synthetic input has no equal-time observation ordering. Unexpected
	// comparator use fails, rather than substituting a non-source policy.
	options := assertionauthority.Options{DateParser: f.store.options.DateParser, Compare: func(a, b string) (int, error) { return 0, errors.New("unexpected_observation_comparison") }, NowMillis: func() int64 { n, _, _ := f.store.options.DateParser.ParseMillis("2026-10-28T13:00:00.000Z"); return n }}
	history := func(s *assertions.Store, id string) (proofs []any, err error) {
		err = s.Read(ctx, func(r *assertions.Reader) error {
			a, e := assertionauthority.New(r, options)
			if e != nil {
				return e
			}
			proofs, e = a.HistorySupports(id)
			return e
		})
		return
	}
	coordinator, e := assertions.NewCoordinator(store, assertions.CoordinatorOptions{HistorySupports: history, Retain: func(s *assertions.Store, r *Object) (bool, error) {
		return get(r, "status") != "invalidated" && get(r, "lineageStatus") != "quarantined", nil
	}, RemoveProjection: func(_ *assertions.Store, id string) error {
		f.events = append(f.events, []any{"projection", id})
		return nil
	}, Committed: func(_ *assertions.Store, id string, version, epoch int64) error {
		f.events = append(f.events, []any{"committed", id, float64(version), float64(epoch)})
		return nil
	}})
	must(t, e)
	p := func(message string) *Object {
		return obj("sources", []any{obj("sceneId", "scene", "messageId", message, "senderId", "a")}, "parents", []any{}, "parentReferences", []any{}, "subjectWitness", obj("kind", "self", "personId", "a", "sourceMessageId", message))
	}
	rootProof, safeProof := p("root"), p("independent")
	record := obj("id", "root", "scope", obj("kind", "scene", "sceneId", "scene", "personId", "a"), "content", "Synthetic declaration root", "kind", "fact", "epistemicKind", "explicit", "sources", []any{arr(get(rootProof, "sources"))[0], arr(get(safeProof, "sources"))[0]}, "importance", "low", "confidence", float64(.5), "learnedAt", "2026-10-28T00:00:00.000Z", "status", "active", "locked", false, "origin", "learned", "lineageStatus", "verified")
	must(t, coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.Write(record); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("root", []any{rootProof, safeProof})
	}))
	// Replace only trusted initial proof material with real RelationAuthority's
	// canonical proof IDs. The stored terms/ref/recordedAt remain immutable.
	must(t, coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(session *recovery.WriteSession) error {
			commitments := f.store.WithSession(session)
			return tx.Store.Read(ctx, func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, options)
				if e != nil {
					return e
				}
				view, e := a.RelationAuthority(assertionauthority.Context{SceneID: "scene"}, Reference{ID: "root", Version: 1, DeletionEpoch: 0})
				if e != nil {
					return e
				}
				if view == nil {
					return errors.New("initial_relation_unavailable")
				}
				fingerprint, e := BackingFingerprint(view.Record)
				if e != nil {
					return e
				}
				transform := func(evidence *Object) (*Object, error) {
					next := evidence.Clone()
					_ = next.Set("proofs", clone(view.Proofs))
					_ = next.Set("backingFingerprint", fingerprint)
					return next, nil
				}
				head, e := commitments.reader().Head("commitment-1")
				if e != nil {
					return e
				}
				revision, e := commitments.reader().Revision("commitment-1", 1)
				if e != nil {
					return e
				}
				ev, e := mapEvidence(arr(get(revision, "evidence")), transform)
				if e != nil {
					return e
				}
				_ = revision.Set("evidence", ev)
				if e = commitments.Put(head, revision); e != nil {
					return e
				}
				timeline, e := commitments.reader().Timeline("commitment-1")
				if e != nil {
					return e
				}
				timeline, e = transformTemporal(timeline, transform)
				if e != nil {
					return e
				}
				return commitments.ReplaceTimeline("commitment-1", timeline)
			})
		})
	}))
	var captured []Reference
	must(t, coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; captured, e = ReadPrivacyRefs(r, options); return e }); e != nil {
			return e
		}
		if len(captured) != 1 || captured[0] != (Reference{ID: "root", Version: 1, DeletionEpoch: 0}) {
			t.Fatalf("captured refs %+v", captured)
		}
		if e := tx.Store.WithWriteSession(func(session *recovery.WriteSession) error {
			if _, e := session.AppendIntent(recovery.Source{SceneID: "scene", MessageID: "root"}); e != nil {
				return e
			}
			_, e := session.Exec("INSERT INTO recall_tombstones VALUES('scene','root','2026-10-28T13:00:00.000Z')")
			return e
		}); e != nil {
			return e
		}
		next := record.Clone()
		_ = next.Set("sources", get(safeProof, "sources"))
		if e := tx.Repository.SaveSupports("root", []any{safeProof}); e != nil {
			return e
		}
		if e := tx.Repository.Write(next); e != nil {
			return e
		}
		return f.service.Refresh(ctx, tx, captured, options)
	}))
	must(t, f.store.Read(ctx, func(r *Reader) error {
		snapshot, e := r.Snapshot("commitment-1")
		if e != nil {
			return e
		}
		head := get(snapshot, "head")
		if get(head, "mutationVersion") != float64(3) {
			t.Fatalf("expected prune then requalification: %s", stringify(t, head))
		}
		revision := arr(get(snapshot, "revisions"))[0]
		for _, ev := range arr(get(revision, "evidence")) {
			if reference(get(ev, "backing")) != (Reference{ID: "root", Version: 1, DeletionEpoch: 0}) || reference(get(ev, "qualificationRef")) != (Reference{ID: "root", Version: 2, DeletionEpoch: 0}) {
				t.Fatalf("exact original binding not retained: %s", stringify(t, ev))
			}
			if len(arr(get(ev, "proofs"))) != 1 || get(arr(get(arr(get(ev, "proofs"))[0], "sources"))[0], "messageId") != "independent" {
				t.Fatal("original OR survivor incorrect")
			}
		}
		return nil
	}))
	// A later revision is not authority to rebind an already stale capture.
	must(t, coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		next := record.Clone()
		_ = next.Set("sources", get(safeProof, "sources"))
		_ = next.Set("confidence", float64(.6))
		if e := tx.Repository.Write(next); e != nil {
			return e
		}
		return f.service.Refresh(ctx, tx, captured, options)
	}))
	must(t, f.store.Read(ctx, func(r *Reader) error {
		revision, e := r.Revision("commitment-1", 1)
		if e != nil {
			return e
		}
		for _, ev := range arr(get(revision, "evidence")) {
			if reference(get(ev, "qualificationRef")) != (Reference{ID: "root", Version: 2, DeletionEpoch: 0}) {
				t.Fatal("stale capture followed a latest-version fallback")
			}
		}
		return nil
	}))
}

func TestNativeRollbackRevokesPrivacyScope(t *testing.T) {
	f := seeded(t)
	_, e := f.main.Exec("CREATE TRIGGER synthetic_native_rollback BEFORE UPDATE ON memory_commitment_heads BEGIN SELECT RAISE(ROLLBACK,'synthetic_native_rollback'); END")
	must(t, e)
	var retained *PrivacyService
	e = f.store.Transaction(context.Background(), func(store *Store) error {
		retained, _ = NewPrivacyService(store, f.service.owners)
		if e := retained.PurgeSource("scene", "root"); e == nil {
			t.Fatal("native rollback missing")
		}
		if e := retained.PurgeSource("scene", "independent"); e == nil {
			t.Fatal("lost transaction reused")
		}
		return nil
	})
	if e == nil {
		t.Fatal("root accepted lost transaction")
	}
	if e = retained.PurgeSource("scene", "root"); e == nil {
		t.Fatal("expired capability accepted")
	}
	var n int
	must(t, f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&n))
	if n != 1 {
		t.Fatalf("durable failure prefix %d", n)
	}
}
func TestCancellationAfterIntentPreservesGap(t *testing.T) {
	f := seeded(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owners := Owners{AfterWrite: func(_ *Store, _ *Object, _, _ []any) error { cancel(); return nil }}
	e := f.store.Transaction(ctx, func(store *Store) error {
		s, _ := NewPrivacyService(store, owners)
		return s.PurgeSource("scene", "root")
	})
	if e == nil {
		t.Fatal("canceled root succeeded")
	}
	var n int
	must(t, f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&n))
	if n != 1 {
		t.Fatal("cancellation erased intent")
	}
	if e = f.store.Read(context.Background(), func(r *Reader) error { _, e := r.Head("commitment-1"); return e }); e == nil {
		t.Fatal("cancellation gap published")
	}
}
