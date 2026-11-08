package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"github.com/mattn/go-sqlite3"
)

func currentReadFixture(t *testing.T) (*fixture, *commitments.Store, ReadOptions) {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	compare := func(string, string) (int, error) { return 0, errors.New("unexpected_native_collation_request") }
	options := ReadOptions{KnowledgeEnabled: true, CommitmentEnabled: false, Authority: assertionauthority.Options{DateParser: utcDate, Compare: compare, NowMillis: func() int64 { return 1793491200000 }}}
	cs, e := commitments.New(f.main, f.ledger, commitments.Options{DateParser: utcDate, Compare: compare})
	must(t, e)
	co, e := assertions.NewCoordinator(f.assertions, assertions.CoordinatorOptions{
		HistorySupports: func(store *assertions.Store, id string) (out []any, err error) {
			err = store.Read(ctx, func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, options.Authority)
				if e != nil {
					return e
				}
				out, e = a.HistorySupports(id)
				return e
			})
			return
		},
		Retain: func(*assertions.Store, *Object) (bool, error) { return true, nil }, RemoveProjection: func(*assertions.Store, string) error { return nil }, Committed: func(*assertions.Store, string, int64, int64) error { return nil },
	})
	must(t, e)
	proof := obj("sources", []any{obj("sceneId", "s", "messageId", "source", "senderId", "p")}, "parents", []any{}, "parentReferences", []any{}, "subjectWitness", obj("kind", "self", "personId", "p", "sourceMessageId", "source"))
	record := obj("id", "backing-a", "scope", obj("kind", "scene", "sceneId", "s", "personId", "p"), "content", "Synthetic current knowledge backing", "kind", "fact", "epistemicKind", "explicit", "sources", clone(get(proof, "sources")), "importance", "low", "confidence", float64(.5), "learnedAt", "2026-10-28T00:00:00.000Z", "status", "active", "locked", false, "origin", "learned", "lineageStatus", "verified")
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.Write(record); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("backing-a", []any{proof})
	}))
	must(t, f.storage.Transaction(ctx, func(s *Store) error {
		return f.assertions.WithSession(s.session).Read(ctx, func(r *assertions.Reader) error {
			authority, e := assertionauthority.New(r, options.Authority)
			if e != nil {
				return e
			}
			view, e := authority.RelationAuthority(assertionauthority.Context{SceneID: "s"}, Reference{ID: "backing-a", Version: 1})
			if e != nil {
				return e
			}
			if view == nil {
				t.Fatal("real assertion missing")
			}
			edge := testEdge("a", object(view.Proofs[0]))
			fp, e := BackingFingerprint(view.Record)
			if e != nil {
				return e
			}
			_ = edge.Set("backingFingerprint", fp)
			_ = edge.Set("object", obj("kind", "entity", "entity", obj("id", "entity-a", "sceneId", "s", "kind", "topic", "title", "Synthetic topic", "backing", clone(get(edge, "backing")), "originEdgeId", "a")))
			return s.Put(edge, "native-read-key")
		})
	}))
	return f, cs, options
}
func queryInput() *Object {
	return obj("anchor", obj("kind", "person", "personId", "p"), "predicates", []any{"prefers_topic"}, "depth", float64(1))
}
func TestKnowledgeConcretePublicationAndActiveChild(t *testing.T) {
	f, cs, options := currentReadFixture(t)
	f.main.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var held *Reader
	var owner *workingset.Scope
	calls := 0
	expectWrite := false
	w, e := workingset.New(ctx, f.main, f.ledger, workingset.Ports{Enabled: func() bool { return true }, ResolveCurrent: func(scope *workingset.Scope, _ workingset.Context, _ []workingset.Reference) ([]workingset.Snapshot, error) {
		calls++
		owner = scope
		if (scope.WriteSession() != nil) != expectWrite {
			t.Fatal("wrong native scope kind")
		}
		e := f.storage.ReadWorkingSetCurrent(ctx, scope, f.assertions, cs, options, func(r *Reader) error {
			held = r
			result, e := r.Query(Context{SceneID: "s"}, queryInput())
			if e != nil {
				return e
			}
			if len(arr(get(result, "edges"))) != 1 || len(arr(get(get(result, "supportClosure"), "records"))) != 1 {
				t.Fatal("real query lost current knowledge", jsonText(t, result))
			}
			result, e = r.Discover(Context{SceneID: "s"}, obj("id", "backing-a", "version", float64(1), "deletionEpoch", float64(0)), queryInput())
			if e != nil {
				return e
			}
			if len(arr(get(result, "edges"))) != 1 {
				t.Fatal("real discover lost entity root")
			}
			if _, e := r.Discover(Context{SceneID: "s"}, obj("id", "backing-a", "version", float64(2), "deletionEpoch", float64(0)), queryInput()); e == nil || e.Error() != "knowledge_backing_unavailable" {
				t.Fatal("stale assertion reference accepted", e)
			}
			if expectWrite {
				return f.storage.WithSession(scope.WriteSession()).Transaction(ctx, func(child *Store) error {
					if e := r.AssertReady(); e == nil {
						t.Fatal("suspended ancestor accepted")
					}
					return child.ReadWorkingSetCurrent(ctx, scope, f.assertions, cs, options, func(*Reader) error { t.Fatal("ancestor bound child"); return nil })
				})
			}
			rows, e := scope.Query("DELETE FROM memory_knowledge_edges RETURNING id")
			if e == nil {
				rows.Next()
				e = rows.Err()
				rows.Close()
			}
			var native sqlite3.Error
			if !errors.As(e, &native) || native.Code != sqlite3.ErrAuth {
				t.Fatalf("read SQL write denial %v", e)
			}
			return nil
		})
		if !expectWrite && e == nil {
			if e := held.AssertReady(); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal("reader escaped inner callback", e)
			}
			if e := scope.CheckScope(); e != nil {
				return nil, e
			}
		}
		return []workingset.Snapshot{}, e
	}})
	must(t, e)
	selection := []workingset.Selection{{Ref: workingset.Reference{Kind: "assertion", ID: "backing-a", Version: 1}, DependencyKey: strings.Repeat("a", 64)}}
	_, e = w.View(ctx, workingset.Context{SceneID: "s"}, selection, true)
	must(t, e)
	if calls != 1 {
		t.Fatal("resolver calls", calls)
	}
	if e := owner.CheckScope(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("owner did not expire", e)
	}
	if _, e := held.Query(Context{Simulation: true}, queryInput()); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("simulation bypassed expired owner", e)
	}
	if _, e := f.storage.Rows(nil, nil); e == nil {
		t.Fatal("write-owned Store.Rows weakened")
	}
	expectWrite = true
	// The intentional suspended-ancestor probe must fail and roll back the child.
	e = w.Transaction(ctx, func(tx *workingset.Service) error {
		_, e := tx.View(ctx, workingset.Context{SceneID: "s"}, selection, true)
		return e
	})
	if e == nil {
		t.Fatal("suspended ancestor did not reject")
	}
}
func TestKnowledgeConcreteOwnerBindingAndRecall(t *testing.T) {
	f, cs, options := currentReadFixture(t)
	other := setup(t)
	ctx := context.Background()
	_, e := f.main.Exec("INSERT INTO recall_tombstones VALUES('s','source','2026-11-01T00:00:00.000Z')")
	must(t, e)
	w, e := workingset.New(ctx, f.main, f.ledger, workingset.Ports{Enabled: func() bool { return true }, ResolveCurrent: func(scope *workingset.Scope, _ workingset.Context, _ []workingset.Reference) ([]workingset.Snapshot, error) {
		if e := other.storage.ReadWorkingSetCurrent(ctx, scope, f.assertions, cs, options, func(*Reader) error { t.Fatal("foreign store callback"); return nil }); e == nil {
			t.Fatal("foreign owner accepted")
		}
		e := f.storage.ReadWorkingSetCurrent(ctx, scope, f.assertions, cs, options, func(r *Reader) error {
			result, e := r.Query(Context{SceneID: "s"}, queryInput())
			if e == nil && len(arr(get(result, "edges"))) != 0 {
				t.Fatal("recalled source survived current authority")
			}
			return e
		})
		return []workingset.Snapshot{}, e
	}})
	must(t, e)
	_, e = w.View(ctx, workingset.Context{SceneID: "s"}, []workingset.Selection{{Ref: workingset.Reference{Kind: "assertion", ID: "backing-a", Version: 1}, DependencyKey: strings.Repeat("a", 64)}}, true)
	must(t, e)
}
