// Package memoryservice composes authoritative refresh, listing and returned-body
// capture. It does not expose a bot/model facade or replace notification owners.
package memoryservice

import (
	"context"
	"errors"
	"math"
	"reflect"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type Object = assertions.Object
type Context struct {
	SceneID    string
	Simulation bool
}
type Reference struct {
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	DeletionEpoch int64  `json:"deletionEpoch"`
}

// Notifications is deliberately mandatory. An adapter must enqueue exact-ref
// semantic notifications with source post-stack requalification, not invoke a
// model here. Until semantic authority/episode discovery/scheduler composition
// exists, this is an explicit incomplete production boundary, never a default.
type Notifications interface{ AssertionCommitted(string, Reference) error }
type Dependencies struct {
	Assertions    *assertions.Store
	Commitments   *commitments.Store
	Relations     *relationstorage.Store
	Projections   *projections.Coordinator
	WorkingSet    *workingset.Service
	Knowledge     *knowledge.Service
	Semantic      *semantic.Store
	Notifications Notifications
	// Dirty must notify the existing workspace maintenance scheduler, not run it
	// within the current write child. No startup enumeration or polling is added.
	Dirty func()
	// Log is the actual runtime logging adapter. Projection failure after a
	// successful commit is diagnostic only; never return it as a write failure.
	Log                func(level, category, message string)
	CommitmentsEnabled func(commitments.Context) (bool, error)
	Authority          assertionauthority.Options
}
type Service struct {
	d         Dependencies
	revisions *assertions.Coordinator
}

func New(d Dependencies) (*Service, error) {
	if d.Assertions == nil || d.Commitments == nil || d.Relations == nil || d.Projections == nil || d.WorkingSet == nil || d.Knowledge == nil || d.Semantic == nil || d.Notifications == nil || d.Dirty == nil || d.Log == nil || d.CommitmentsEnabled == nil || d.Authority.DateParser == nil || d.Authority.Compare == nil {
		return nil, errors.New("memory_service_dependencies_required")
	}
	s := &Service{d: d}
	c, e := assertions.NewCoordinator(d.Assertions, assertions.CoordinatorOptions{
		HistorySupports: s.historySupports, Retain: s.retain,
		RemoveProjection: func(st *assertions.Store, id string) error {
			if e := d.Projections.RemoveFrom(st, id); e != nil {
				return e
			}
			return s.unavailable(st, id)
		},
		Committed: s.committed,
		AfterCommit: func() error {
			if _, e := d.Projections.Flush(context.Background()); e != nil {
				d.Log("warn", "memory", "memory_projection_refresh_failed")
			}
			return nil
		},
	})
	if e != nil {
		return nil, e
	}
	s.revisions = c
	return s, nil
}
func value(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func text(o *Object, k string) string { v, _ := value(o, k).(string); return v }
func object(v any) *Object            { o, _ := v.(*Object); return o }
func (s *Service) historySupports(st *assertions.Store, id string) (out []any, err error) {
	err = st.Read(context.Background(), func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		out, e = a.HistorySupports(id)
		return e
	})
	return
}

// Retention is deliberately historical: it does not use current validity or
// domainSourceCurrent, which would erase history on source edits/expiry.
func (s *Service) retain(st *assertions.Store, record *Object) (ok bool, err error) {
	if text(record, "status") == "invalidated" || text(record, "lineageStatus") == "quarantined" {
		return false, nil
	}
	err = st.Read(context.Background(), func(r *assertions.Reader) error {
		sources, valid := value(record, "sources").([]any)
		if !valid {
			return errors.New("assertion_array_required")
		}
		for _, v := range sources {
			source := object(v)
			recalled, e := r.Recalled(text(source, "sceneId"), text(source, "messageId"))
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
}
func (s *Service) mutations(ws *recovery.WriteSession) *workingset.MutationHooks {
	return s.d.WorkingSet.WithSession(ws).Mutations(workingset.MutationPorts{ResolveCommitted: s.resolveCommitted})
}
func (s *Service) resolveCommitted(ws *recovery.WriteSession, c workingset.Context, refs []workingset.Reference) (out []workingset.Snapshot, err error) {
	err = s.d.Commitments.WithSession(ws).ReadCurrent(context.Background(), s.d.Assertions.WithSession(ws), s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		resolver, e := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
		if e != nil {
			return e
		}
		out, e = resolver.Resolve(c, refs)
		return e
	})
	return
}

// ResolveWorkingSet is the actual concrete readonly/write scope adapter used
// when constructing workingset.Service. It never reenters a publication.
func ResolveWorkingSet(plans *commitments.Store, assertionsStore *assertions.Store, enabled func(commitments.Context) (bool, error), options assertionauthority.Options) func(*workingset.Scope, workingset.Context, []workingset.Reference) ([]workingset.Snapshot, error) {
	return func(scope *workingset.Scope, c workingset.Context, refs []workingset.Reference) (out []workingset.Snapshot, err error) {
		err = plans.ReadWorkingSetCurrent(context.Background(), scope, assertionsStore, enabled, options, func(read *commitments.ReadService) error {
			resolver, e := memoryworkspace.NewResolver(read, options.DateParser)
			if e != nil {
				return e
			}
			out, e = resolver.Resolve(c, refs)
			return e
		})
		return
	}
}
func (s *Service) unavailable(st *assertions.Store, id string) error {
	return st.WithWriteSession(func(ws *recovery.WriteSession) error { _, e := s.mutations(ws).InvalidateAssertion(id); return e })
}
func (s *Service) purgeRelations(st *assertions.Store, id string) error {
	return st.WithWriteSession(func(ws *recovery.WriteSession) error {
		_, e := s.d.Relations.WithSession(ws).PurgeAssertion(id)
		return e
	})
}
func (s *Service) committed(st *assertions.Store, id string, version, epoch int64) error {
	if e := s.purgeRelations(st, id); e != nil {
		return e
	}
	if e := s.d.Projections.CommittedFrom(st, id, version, epoch); e != nil {
		return e
	}
	var record *Object
	if e := st.Read(context.Background(), func(r *assertions.Reader) error { var e error; record, e = r.Record(id); return e }); e != nil {
		return e
	}
	scope := object(value(record, "scope"))
	if text(scope, "kind") != "scene" {
		return nil
	}
	scene := text(scope, "sceneId")
	if e := st.WithWriteSession(func(ws *recovery.WriteSession) error {
		result, e := s.mutations(ws).Committed(workingset.Context{SceneID: scene}, []workingset.Reference{{Kind: "assertion", ID: id, Version: float64(version), DeletionEpoch: float64(epoch)}})
		if e == nil && result.Queued > 0 {
			s.d.Dirty()
		}
		return e
	}); e != nil {
		return e
	}
	if scoped, ok := s.d.Notifications.(interface {
		AssertionCommittedInSession(*recovery.WriteSession, string, Reference) error
	}); ok {
		return st.WithWriteSession(func(ws *recovery.WriteSession) error {
			return scoped.AssertionCommittedInSession(ws, scene, Reference{id, version, epoch})
		})
	}
	return s.d.Notifications.AssertionCommitted(scene, Reference{id, version, epoch})
}
func (s *Service) owners() assertionlifecycle.Owners {
	return assertionlifecycle.Owners{
		RemoveProjection: func(st *assertions.Store, id string) error {
			if e := s.purgeRelations(st, id); e != nil {
				return e
			}
			if e := s.d.Projections.RemoveFrom(st, id); e != nil {
				return e
			}
			return s.unavailable(st, id)
		},
		BeforePurgeHistory: func(st *assertions.Store, id string) error {
			if e := s.purgeRelations(st, id); e != nil {
				return e
			}
			return s.unavailable(st, id)
		},
		BeforeInvalidateRecord: func(st *assertions.Store, id string) error {
			if e := s.d.Knowledge.PurgeInAssertionScope(st, id); e != nil {
				return e
			}
			return st.WithWriteSession(func(ws *recovery.WriteSession) error { return s.d.Semantic.WithSession(ws).PurgeAssertion(id) })
		},
	}
}
func (s *Service) refresh(tx *assertions.RevisionTxn) error {
	p, e := assertionlifecycle.New(tx, s.owners(), s.d.Authority)
	if e != nil {
		return e
	}
	_, e = p.RefreshSupport()
	return e
}
func (s *Service) RefreshSupport(ctx context.Context) error {
	return s.revisions.Transaction(ctx, s.refresh)
}
func ready(ctx context.Context, st *assertions.Store) error {
	return st.Read(ctx, func(r *assertions.Reader) error { return r.AssertReady() })
}
func (s *Service) read(ctx context.Context, st *assertions.Store, input memoryread.Input) (out []*Object, err error) {
	err = st.Read(ctx, func(r *assertions.Reader) error {
		read, e := memoryread.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		out, e = read.List(input)
		return e
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *Service) List(ctx context.Context, c Context, person string) ([]*Object, error) {
	return s.list(ctx, c, memoryread.Input{SceneID: c.SceneID, Simulation: c.Simulation, PersonID: person})
}
func (s *Service) ListByIDs(ctx context.Context, c Context, ids []string) ([]*Object, error) {
	return s.list(ctx, c, memoryread.Input{SceneID: c.SceneID, Simulation: c.Simulation, IDs: append([]string{}, ids...)})
}
func (s *Service) list(ctx context.Context, c Context, input memoryread.Input) ([]*Object, error) {
	if e := ready(ctx, s.d.Assertions); e != nil {
		return nil, e
	}
	if !c.Simulation {
		if e := s.RefreshSupport(ctx); e != nil {
			return nil, e
		}
	}
	return s.read(ctx, s.d.Assertions, input)
}
func (s *Service) CaptureParentReferences(ctx context.Context, c Context, returned []*Object) ([]Reference, error) {
	if c.Simulation {
		return []Reference{}, nil
	}
	if len(returned) > 40 {
		return nil, errors.New("workspace_candidate_limit")
	}
	return s.capture(ctx, c, returned)
}
func (s *Service) CaptureExposedReferences(ctx context.Context, c Context, returned []*Object) ([]Reference, error) {
	if c.Simulation {
		return []Reference{}, nil
	}
	return s.capture(ctx, c, returned)
}
func (s *Service) capture(ctx context.Context, c Context, returned []*Object) ([]Reference, error) {
	out := []Reference{}
	err := s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		// Source listByIds enters a nested revision transaction. Never flush/checkpoint
		// that child: legacy import below occurs before the outer final revision flush.
		if e := ready(ctx, tx.Store); e != nil {
			return e
		}
		if e := tx.Transaction(ctx, s.refresh); e != nil {
			return e
		}
		ids := make([]string, len(returned))
		for i, r := range returned {
			if r == nil {
				return errors.New("memory_record_required")
			}
			ids[i] = text(r, "id")
		}
		records, e := s.read(ctx, tx.Store, memoryread.Input{SceneID: c.SceneID, IDs: ids})
		if e != nil {
			return e
		}
		current := map[string]*Object{}
		for _, r := range records {
			current[text(r, "id")] = r
		}
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			for _, record := range returned {
				body := record.Clone()
				body.Delete("score")
				authorized := current[text(record, "id")]
				if authorized == nil || !deepEqual(authorized, body) {
					continue
				}
				id := text(record, "id")
				state, e := r.State(id)
				if e != nil {
					return e
				}
				if state.Deleted {
					continue
				}
				if state.Version == 0 && state.DeletionEpoch == 0 {
					proofs, e := a.HistorySupports(id)
					if e != nil {
						return e
					}
					if _, e = tx.Store.ImportLegacy(authorized, proofs); e != nil {
						return e
					}
					state, e = r.State(id)
					if e != nil {
						return e
					}
				}
				if state.Version > 0 {
					out = append(out, Reference{id, state.Version, state.DeletionEpoch})
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Node isDeepStrictEqual on the JSON-shaped record domain ignores object key
// insertion order, preserves array order/property presence, and uses Object.is
// for numbers (NaN equals NaN, negative zero differs). JSON text is not equality.
func deepEqual(a, b any) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		if !ok {
			return false
		}
		if x == nil || y == nil {
			return x == y
		}
		if x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			v, present := y.Get(m.Key)
			if !present || !deepEqual(m.Value, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !deepEqual(v, y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && ((math.IsNaN(x) && math.IsNaN(y)) || x == y && (x != 0 || math.Signbit(x) == math.Signbit(y)))
	default:
		return reflect.DeepEqual(a, b)
	}
}
