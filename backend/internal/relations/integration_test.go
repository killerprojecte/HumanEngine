package relations

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"math"
	"strings"
	"testing"
)

func productionService(t *testing.T, f *fixture) *Service {
	t.Helper()
	p, e := jsdate.New("UTC")
	must(t, e)
	on := func(Context) (bool, error) { return true, nil }
	s, e := New(f.main, f.ledger, assertions.Options{}, Options{Authority: assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, NowMillis: func() int64 { return 1793491200000 }}, Enabled: on, LearningEnabled: on, AutoAdmission: on, NowMillis: func() int64 { return 1793491200000 }, ID: func() string { return "synthetic-relation" }})
	must(t, e)
	return s
}
func syntheticRecord(t *testing.T) *Object {
	return object(parse(t, `{"id":"a","scope":{"kind":"scene","sceneId":"s","personId":"p"},"content":"Synthetic plan","kind":"claim","sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"importance":"medium","confidence":0.6,"learnedAt":"2026-11-01T00:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified","epistemicKind":"explicit"}`))
}
func syntheticProof(t *testing.T) *Object {
	return object(parse(t, `{"sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"parents":[],"subjectWitness":{"kind":"self","personId":"p","sourceMessageId":"m"}}`))
}
func coordinator(t *testing.T, f *fixture, s *Service) *assertions.Coordinator {
	t.Helper()
	c, e := assertions.NewCoordinator(f.assertions, assertions.CoordinatorOptions{
		HistorySupports: func(store *assertions.Store, id string) (out []any, err error) {
			err = store.Read(context.Background(), func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, s.options.Authority)
				if e != nil {
					return e
				}
				out, e = a.HistorySupports(id)
				return e
			})
			return
		},
		// This isolated fixture has no additional retention/working-set owners.
		Retain: func(_ *assertions.Store, r *Object) (bool, error) { return get(r, "status") != "invalidated", nil },
		RemoveProjection: func(store *assertions.Store, id string) error {
			if e := s.PurgeInAssertionScope(store, id); e != nil {
				return e
			}
			return store.RemoveVector(id)
		},
		Committed: s.AssertionCommitted,
	})
	must(t, e)
	return c
}
func seedAssertion(t *testing.T, c *assertions.Coordinator) {
	must(t, c.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.Write(syntheticRecord(t)); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("a", []any{syntheticProof(t)})
	}))
}
func admitSynthetic(t *testing.T, s *Service) {
	t.Helper()
	must(t, s.Transaction(context.Background(), func(b *Service) error {
		out, e := b.Admit(context.Background(), Context{SceneID: "s", Provenance: "source-only"}, obj("id", "a", "version", float64(1), "deletionEpoch", float64(0)), []any{parse(t, `{"predicate":"participates_in","modality":"asserted","temporalScope":"current","personId":"p","target":{"kind":"new","entityKind":"plan","title":"Synthetic"},"polarity":"positive","conditions":[]}`)})
		if e != nil {
			return e
		}
		if len(out.Created) != 1 || get(out.Created[0], "status") != "accepted" {
			t.Fatalf("admission %v", out)
		}
		return nil
	}))
}
func count(t *testing.T, f *fixture, table string) int {
	t.Helper()
	var n int
	must(t, f.main.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}
func TestRealAuthorityPublicationAndRevisionPurge(t *testing.T) {
	f := setup(t)
	s := productionService(t, f)
	c := coordinator(t, f, s)
	seedAssertion(t, c)
	admitSynthetic(t, s)
	out, e := s.List(context.Background(), Context{SceneID: "s"}, ListOptions{})
	must(t, e)
	if len(out.Items) != 1 {
		t.Fatal(out)
	}
	var borrowed *relationstorage.Reader
	must(t, f.assertions.Read(context.Background(), func(r *assertions.Reader) error {
		var e error
		borrowed, e = r.Relations()
		if e != nil {
			return e
		}
		_, e = borrowed.Rows("s")
		return e
	}))
	if _, e = borrowed.Rows("s"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped reader %v", e)
	}
	must(t, c.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		r := syntheticRecord(t)
		_ = r.Set("content", "Revised synthetic plan")
		return tx.Repository.Write(r)
	}))
	if count(t, f, "memory_relation_candidates") != 0 || count(t, f, "memory_relation_tombstones") != 1 {
		t.Fatal("revision did not purge real relation")
	}
}
func TestRealLifecyclePurgeAndDurableIntentRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			f := setup(t)
			s := productionService(t, f)
			c := coordinator(t, f, s)
			seedAssertion(t, c)
			admitSynthetic(t, s)
			before, e := recovery.ReadGate(context.Background(), f.main)
			must(t, e)
			stop := errors.New("synthetic_root_failure")
			e = c.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
				// Unimplemented unrelated owners are explicit test callbacks, not production adapters.
				owners := assertionlifecycle.Owners{RemoveProjection: func(store *assertions.Store, id string) error {
					if e := s.PurgeInAssertionScope(store, id); e != nil {
						return e
					}
					return store.RemoveVector(id)
				}, BeforePurgeHistory: s.PurgeInAssertionScope, BeforeInvalidateRecord: func(*assertions.Store, string) error { return errors.New("unexpected_invalidation_owner") }}
				lifecycle, e := assertionlifecycle.New(tx, owners, s.options.Authority)
				if e != nil {
					return e
				}
				if e = lifecycle.PurgeHistoryDependents("a", nil, nil); e != nil {
					return e
				}
				if fail {
					return stop
				}
				return nil
			})
			if fail {
				if !errors.Is(e, stop) {
					t.Fatal(e)
				}
				if count(t, f, "memory_relation_candidates") != 1 || count(t, f, "memory_relation_tombstones") != 0 {
					t.Fatal("relation SQL escaped root rollback")
				}
				after, e := recovery.ReadGate(context.Background(), f.main)
				must(t, e)
				if after.Head.Sequence != before.Head.Sequence {
					t.Fatal("main ack escaped rollback")
				}
				var denied bool
				e = f.assertions.Read(context.Background(), func(*assertions.Reader) error { return nil })
				denied = e != nil
				if !denied {
					t.Fatal("durable intent gap allowed publication")
				}
			} else {
				must(t, e)
				if count(t, f, "memory_relation_candidates") != 0 || count(t, f, "memory_relation_tombstones") != 1 {
					t.Fatal("lifecycle failed to purge relations")
				}
			}
		})
	}
}
func TestRelationNativeTransactionLossAndAncestor(t *testing.T) {
	f := setup(t)
	s := productionService(t, f)
	c := coordinator(t, f, s)
	seedAssertion(t, c)
	_, e := f.main.Exec("CREATE TRIGGER rollback_candidate BEFORE INSERT ON memory_relation_candidates BEGIN SELECT RAISE(ROLLBACK,'synthetic_native_loss'); END")
	must(t, e)
	e = s.Transaction(context.Background(), func(b *Service) error {
		return b.Storage.WithWriteSession(func(session *recovery.WriteSession) error {
			return b.assertions.Read(context.Background(), func(r *assertions.Reader) error {
				escaped, e := r.Relations()
				if e != nil {
					return e
				}
				must(t, session.WithSavepoint(func(child *recovery.WriteSession) error {
					if _, e := escaped.Rows("s"); e == nil {
						t.Fatal("ancestor reader remained active")
					}
					childReader, e := b.Storage.WithSession(child).Reader()
					if e != nil {
						return e
					}
					_, e = childReader.Rows("s")
					return e
				}))
				if _, e := escaped.Rows("s"); e != nil {
					t.Fatalf("ancestor did not resume: %v", e)
				}
				_, failure := b.Admit(context.Background(), Context{SceneID: "s"}, obj("id", "a", "version", float64(1), "deletionEpoch", float64(0)), []any{parse(t, `{"predicate":"participates_in","modality":"asserted","temporalScope":"current","personId":"p","target":{"kind":"new","entityKind":"plan","title":"Synthetic"},"polarity":"positive","conditions":[]}`)})
				if failure == nil || !strings.Contains(failure.Error(), "synthetic_native_loss") {
					t.Fatalf("native failure %v", failure)
				}
				if _, e := escaped.Rows("s"); e == nil {
					t.Fatal("native loss reader remained active")
				}
				return nil
			})
		})
	})
	if e == nil {
		t.Fatal("caught native rollback allowed success")
	}
	if count(t, f, "memory_relation_entities") != 0 || count(t, f, "memory_relation_candidates") != 0 {
		t.Fatal("native rollback retained rows")
	}
}
func TestProofDeepStrictEquality(t *testing.T) {
	positive := obj("x", float64(0), "y", []any{"a", "b"})
	reordered := obj("y", []any{"a", "b"}, "x", float64(0))
	negative := obj("x", math.Copysign(0, -1), "y", []any{"a", "b"})
	if !deepEqual(positive, reordered) || deepEqual(positive, negative) || deepEqual([]any{"a", "b"}, []any{"b", "a"}) {
		t.Fatal("deep strict equality changed")
	}
}

func TestProjectionRejectsSignedZeroProofChange(t *testing.T) {
	proof := syntheticProof(t)
	_ = proof.Set("proofId", "synthetic-proof")
	_ = proof.Set("extension", float64(0))
	live := clone(proof).(*Object)
	_ = live.Set("extension", math.Copysign(0, -1))
	backing := obj("id", "a", "version", float64(1), "deletionEpoch", float64(0))
	row := obj("sceneId", "s", "personId", "p", "backing", backing, "target", obj("backing", backing), "proofs", []any{proof})
	c := &candidates{options: Options{Enabled: func(Context) (bool, error) { return true, nil }}, current: func(Context, *Object) (*assertionauthority.Result, error) {
		return &assertionauthority.Result{Record: syntheticRecord(t), Proofs: []any{live}}, nil
	}}
	projected, e := c.projection(Context{SceneID: "s"}, row)
	must(t, e)
	if projected != nil {
		t.Fatal("signed zero change authorized original edge")
	}
}

func TestRelationSchemaHasNoForeignKeys(t *testing.T) {
	f := setup(t)
	for _, table := range []string{"memory_relation_entities", "memory_relation_candidates", "memory_relation_tombstones"} {
		rows, e := f.main.Query("PRAGMA foreign_key_list(" + table + ")")
		must(t, e)
		if rows.Next() {
			t.Fatalf("invented relation FK: %s", table)
		}
		must(t, rows.Err())
		must(t, rows.Close())
	}
}
