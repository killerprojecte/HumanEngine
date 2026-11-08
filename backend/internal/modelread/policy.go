// Package modelread implements the Engine's model-facing scene permission. It
// does not turn admin/storage/learning operations into model-facing reads.
package modelread

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"math"
)

type Policy struct{ store *assertions.Store }
type Ticket struct {
	self            *Ticket
	owner           *Policy
	scene           string
	revision        float64
	personalization bool
}

func New(store *assertions.Store) (*Policy, error) {
	if store == nil {
		return nil, errors.New("model_read_store_required")
	}
	return &Policy{store}, nil
}
func (t *Ticket) PersonalizationEnabled() bool { return t != nil && t.self == t && t.personalization }
func prop(o *assertions.Object, k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	return o.Get(k)
}
func (p *Policy) state(ctx context.Context, scene string) (enabled bool, revision float64, personal bool, err error) {
	personal = true
	err = p.store.Read(ctx, func(r *assertions.Reader) error {
		row, e := r.Scene(scene)
		if e != nil {
			return e
		}
		enabled, revision, personal, e = sceneState(row)
		return e
	})
	return
}
func (p *Policy) PersonalizationAllowed(ctx context.Context, scene string) (bool, error) {
	enabled, _, personal, e := p.state(ctx, scene)
	return enabled && personal, e
}
func (p *Policy) Require(ctx context.Context, scene string) (*Ticket, error) {
	enabled, revision, personal, e := p.state(ctx, scene)
	if e != nil {
		return nil, e
	}
	if !enabled {
		return nil, errors.New("memory_read_disabled")
	}
	t := &Ticket{owner: p, scene: scene, revision: revision, personalization: personal}
	t.self = t
	return t, nil
}
func (p *Policy) Valid(ctx context.Context, t *Ticket) bool {
	if t == nil || t.self != t || t.owner != p {
		return false
	}
	enabled, revision, personal, e := p.state(ctx, t.scene)
	return e == nil && enabled && revision == t.revision && personal == t.personalization
}
func (p *Policy) Assert(ctx context.Context, t *Ticket) error {
	if !p.Valid(ctx, t) {
		return errors.New("memory_read_revoked")
	}
	return nil
}

func sceneState(row *assertions.Object) (enabled bool, revision float64, personal bool, err error) {
	personal = true
	enabled = row != nil
	for _, key := range []string{"memoryReadEnabled", "personalizationEnabled"} {
		v, has := prop(row, key)
		if !has {
			continue
		}
		flag, ok := v.(bool)
		if !ok {
			return false, 0, false, errors.New("memory_read_revoked")
		}
		if key == "memoryReadEnabled" {
			enabled = enabled && flag
		} else {
			personal = flag
		}
	}
	if v, has := prop(row, "memoryReadRevision"); has && v != nil {
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 9007199254740991 || math.Trunc(n) != n {
			return false, 0, false, errors.New("memory_read_revoked")
		}
		revision = n
	}
	return
}
