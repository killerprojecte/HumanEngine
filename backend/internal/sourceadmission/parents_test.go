package sourceadmission

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

// This fixture exercises real revision/history, support storage, projection
// invalidation and authority. There is no injected parent-validity callback.
func parentCoordinator(t *testing.T, f scopedFixture) *assertions.Coordinator {
	t.Helper()
	ctx := context.Background()
	options := testOptions(t)
	projection, e := projections.New(ctx, f.main, f.recovery, func() bool { return true }, projections.AssertionReader(f.store, options), projections.Options{})
	if e != nil {
		t.Fatal(e)
	}
	c, e := assertions.NewCoordinator(f.store, assertions.CoordinatorOptions{
		HistorySupports: func(st *assertions.Store, id string) (out []any, err error) {
			err = st.Read(ctx, func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, options)
				if e != nil {
					return e
				}
				out, e = a.HistorySupports(id)
				return e
			})
			return
		},
		// Source memoryservice retention: historical privacy, not current applicability.
		Retain: func(st *assertions.Store, record *Object) (ok bool, err error) {
			if get(record, "status") == "invalidated" || get(record, "lineageStatus") == "quarantined" {
				return false, nil
			}
			err = st.Read(ctx, func(r *assertions.Reader) error {
				for _, s := range arr(get(record, "sources")) {
					recalled, e := r.Recalled(str(get(s, "sceneId")), str(get(s, "messageId")))
					if e != nil {
						return e
					}
					if recalled {
						return nil
					}
				}
				ok = true
				return nil
			})
			return
		},
		RemoveProjection: projection.RemoveFrom, Committed: projection.CommittedFrom,
	})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestContextualParentsExactNativeRevision(t *testing.T) {
	for _, mode := range []string{"mutation", "recall-or", "remove-or"} {
		t.Run(mode, func(t *testing.T) {
			f := scopedSetup(t)
			coordinator := parentCoordinator(t, f)
			ctx := context.Background()
			parent := parseObject(t, `{"id":"parent","scope":{"kind":"scene","sceneId":"s","personId":"p"},"kind":"fact","content":"Synthetic parent","sources":[{"sceneId":"s","messageId":"parent-a","senderId":"p"},{"sceneId":"s","messageId":"parent-b","senderId":"p"}],"importance":"medium","confidence":0.7,"learnedAt":"2026-01-01T00:00:00Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified"}`)
			proofA := obj("sources", []any{arr(get(parent, "sources"))[0]}, "parents", []any{}, "parentReferences", []any{})
			proofB := obj("sources", []any{arr(get(parent, "sources"))[1]}, "parents", []any{}, "parentReferences", []any{})
			input := parseObject(t, `{"sceneId":"s","simulation":false,"provenance":"contextual","dependencyMemoryIds":["parent"],"sourceEvidence":[{"sceneId":"s","messageId":"m","senderId":"p","available":true,"isBot":false}]}`)
			candidate := parseObject(t, `{"kind":"fact","content":"Synthetic child","sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"subject":{"kind":"self","sourceMessageId":"m"}}`)
			e := coordinator.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
				if e := tx.Repository.Write(parent); e != nil {
					return e
				}
				if e := tx.Repository.SaveSupports("parent", []any{proofA, proofB}); e != nil {
					return e
				}
				if e := tx.Checkpoint(); e != nil {
					return e
				}
				bind := func() error {
					return tx.Store.Read(ctx, func(r *assertions.Reader) error {
						state, e := r.State("parent")
						if e != nil {
							return e
						}
						if state.Version < 1 || state.Deleted {
							t.Fatalf("missing real revision: %+v", state)
						}
						_ = input.Set("dependencyParentReferences", []any{obj("id", "parent", "version", float64(state.Version), "deletionEpoch", float64(state.DeletionEpoch))})
						return nil
					})
				}
				check := func(want string) error {
					return tx.Store.Read(ctx, func(r *assertions.Reader) error {
						a, e := New(r, testOptions(t))
						if e != nil {
							return e
						}
						got, e := a.Prepare(input, candidate)
						if e != nil {
							return e
						}
						status := got.Reason
						if got.Status == "accepted" {
							status = got.Status
							if !deepEqual(get(got.Proof, "parentReferences"), get(input, "dependencyParentReferences")) {
								t.Fatal("exact parent reference not preserved")
							}
						}
						if status != want {
							t.Fatalf("%s: got %s want %s", mode, status, want)
						}
						return nil
					})
				}
				if e := bind(); e != nil {
					return e
				}
				if e := check("accepted"); e != nil {
					return e
				}
				// Same ID with a wrong deletion epoch cannot be rescued by a current preview.
				exact := clone(get(input, "dependencyParentReferences"))
				ref := object(arr(get(input, "dependencyParentReferences"))[0])
				_ = ref.Set("deletionEpoch", num(get(ref, "deletionEpoch"))+1)
				if e := check("invalid_memory_dependencies"); e != nil {
					return e
				}
				_ = input.Set("dependencyParentReferences", clone(exact))
				ref = object(arr(get(input, "dependencyParentReferences"))[0])
				_ = ref.Set("id", "another-parent")
				if e := check("invalid_memory_dependencies"); e != nil {
					return e
				}
				_ = input.Set("dependencyParentReferences", clone(exact))
				switch mode {
				case "mutation":
					_ = parent.Set("content", "Synthetic parent changed")
					if e := tx.Repository.Write(parent); e != nil {
						return e
					}
					if e := tx.Checkpoint(); e != nil {
						return e
					}
					if e := check("invalid_memory_dependencies"); e != nil {
						return e
					}
					if e := bind(); e != nil {
						return e
					}
					if e := check("accepted"); e != nil {
						return e
					}
				case "recall-or":
					for i, id := range []string{"parent-a", "parent-b"} {
						if e := tx.Store.WithWriteSession(func(s *recovery.WriteSession) error {
							_, e := s.Exec("INSERT INTO recall_tombstones VALUES('s',?,'2026-11-01T00:00:00Z')", id)
							return e
						}); e != nil {
							return e
						}
						// Current provenance can retain the other OR alternative, but
						// the captured historical record still includes the recalled
						// source. Its exact reference must be rejected, not rebound.
						if e := tx.Store.Read(ctx, func(r *assertions.Reader) error {
							a, e := assertionauthority.New(r, testOptions(t))
							if e != nil {
								return e
							}
							valid, e := a.ValidParent(object(get(parent, "scope")), "parent")
							if e != nil {
								return e
							}
							applicable, e := a.CurrentApplicable(parent)
							if e != nil {
								return e
							}
							if valid != (i == 0) || applicable != (i == 0) {
								t.Fatalf("OR current authority after recall %d: valid=%v applicable=%v", i, valid, applicable)
							}
							return nil
						}); e != nil {
							return e
						}
						if e := check("invalid_memory_dependencies"); e != nil {
							return e
						}
					}
				case "remove-or":
					if e := tx.Repository.SaveSupports("parent", []any{proofB}); e != nil {
						return e
					}
					if e := tx.Checkpoint(); e != nil {
						return e
					}
					if e := check("invalid_memory_dependencies"); e != nil {
						return e
					}
					if e := bind(); e != nil {
						return e
					}
					if e := check("accepted"); e != nil {
						return e
					}
					if e := tx.Repository.SaveSupports("parent", []any{}); e != nil {
						return e
					}
					if e := tx.Checkpoint(); e != nil {
						return e
					}
					if e := bind(); e != nil {
						return e
					}
					if e := check("invalid_memory_dependencies"); e != nil {
						return e
					}
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
