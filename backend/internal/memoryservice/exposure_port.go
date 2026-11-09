package memoryservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// ExposurePort owns real capture/refresh outside read callbacks. Cache and turn
// validation use SnapshotScoped, which never enters a root transaction, runs
// lifecycle refresh, imports legacy revisions, or upgrades an exposed reference.
type ExposurePort struct {
	memory    *Service
	sources   *sourceevents.Store
	main      *sql.DB
	authority *recovery.Authority
}
type ExposureSnapshot struct {
	Record  *Object
	State   assertions.State
	Binding string
}

func NewExposurePort(memory *Service, sources *sourceevents.Store, main *sql.DB, a *recovery.Authority) (*ExposurePort, error) {
	if memory == nil || sources == nil || main == nil || a == nil {
		return nil, errors.New("memory_exposure_owners_required")
	}
	if e := memory.d.Assertions.CheckReadBinding(a, main, nil); e != nil {
		return nil, e
	}
	p := &ExposurePort{memory, sources, main, a}
	e := sources.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
		return p.withReader(reads, func(*assertions.Reader) error { return nil })
	})
	if e != nil {
		return nil, e
	}
	return p, nil
}
func (p *ExposurePort) Capture(ctx context.Context, c Context, records []*Object) ([]Reference, error) {
	return p.memory.CaptureExposedReferences(ctx, c, records)
}
func (p *ExposurePort) withReader(reads botruntime.ReadAuthority, fn func(*assertions.Reader) error) error {
	return p.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		check := func() error { return scope.CheckBinding(p.main, p.authority) }
		if e := check(); e != nil {
			return e
		}
		r, e := p.memory.d.Assertions.BorrowReader(check, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, func(q string) (assertions.PreparedRead, error) {
			statement, e := scope.PrepareAssertionRead(q)
			if e != nil {
				return nil, e
			}
			return exposureStatement{statement}, nil
		})
		if e != nil {
			return e
		}
		return fn(r)
	})
}

type exposureStatement struct {
	sourceevents.RuntimeReadStatement
}

func (s exposureStatement) Query(args ...any) (assertions.Rows, error) {
	return s.RuntimeReadStatement.Query(args...)
}
func parentBinding(r *assertions.Reader, id string) (string, error) {
	proofs, e := r.Proofs(id)
	if e != nil {
		return "", e
	}
	binding := ""
	for _, raw := range proofs {
		proof := object(raw)
		if proof == nil {
			return "", errors.New("assertion_proof_object_required")
		}
		parents, ok := value(proof, "parents").([]any)
		if !ok {
			return "", errors.New("assertion_array_required")
		}
		if len(parents) > 0 {
			if assertionauthority.ParentBindingState(proof) != "bound" {
				return "legacy_unknown", nil
			}
			binding = "bound"
		}
	}
	return binding, nil
}

// Bindings captures no authorization cache; it only reads the exact parent
// binding metadata after the real batch capture, matching source prepare order.
func (p *ExposurePort) Bindings(ctx context.Context, ids []string) (out map[string]string, err error) {
	out = map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	err = p.memory.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
		for _, id := range ids {
			binding, e := parentBinding(r, id)
			if e != nil {
				return e
			}
			out[id] = binding
		}
		return nil
	})
	return
}
func (p *ExposurePort) snapshot(r *assertions.Reader, c Context, ids []string) (map[string]ExposureSnapshot, error) {
	read, e := memoryread.New(r, p.memory.d.Authority)
	if e != nil {
		return nil, e
	}
	rows, e := read.List(memoryread.Input{SceneID: c.SceneID, Simulation: c.Simulation, IDs: append([]string{}, ids...)})
	if e != nil {
		return nil, e
	}
	out := map[string]ExposureSnapshot{}
	for _, record := range rows {
		id := text(record, "id")
		entry := ExposureSnapshot{Record: record}
		if !c.Simulation {
			entry.State, e = r.State(id)
			if e != nil {
				return nil, e
			}
			entry.Binding, e = parentBinding(r, id)
			if e != nil {
				return nil, e
			}
		}
		out[id] = entry
	}
	return out, nil
}
func (p *ExposurePort) Snapshot(ctx context.Context, c Context, ids []string) (out map[string]ExposureSnapshot, err error) {
	if e := ready(ctx, p.memory.d.Assertions); e != nil {
		return nil, e
	}
	if !c.Simulation {
		if e := p.memory.RefreshSupport(ctx); e != nil {
			return nil, e
		}
	}
	err = p.memory.d.Assertions.Read(ctx, func(r *assertions.Reader) error { var e error; out, e = p.snapshot(r, c, ids); return e })
	return
}
func (p *ExposurePort) SnapshotScoped(reads botruntime.ReadAuthority, c Context, ids []string) (out map[string]ExposureSnapshot, err error) {
	err = p.withReader(reads, func(r *assertions.Reader) error { var e error; out, e = p.snapshot(r, c, ids); return e })
	return
}
