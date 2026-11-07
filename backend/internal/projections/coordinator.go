package projections

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
)

type Coordinator struct {
	outbox    *Outbox
	processor *Processor
	enabled   func() bool
}

// New constructs real outbox and FTS/person state, including when disabled.
// It does not enumerate assertions, enqueue old rows or run model/extraction.
func New(ctx context.Context, main *sql.DB, authority *recovery.Authority, enabled func() bool, eligible EligibleReader, options Options) (*Coordinator, error) {
	if e := storage.RequireFTS5(); e != nil {
		return nil, e
	}
	if enabled == nil || eligible == nil {
		return nil, errors.New("projection_coordinator_dependencies_required")
	}
	outbox, e := NewOutbox(ctx, main, authority, options)
	if e != nil {
		return nil, e
	}
	processor, e := NewProcessor(ctx, outbox, eligible, options.Now)
	if e != nil {
		return nil, e
	}
	return &Coordinator{outbox, processor, enabled}, nil
}
func (c *Coordinator) WithSession(s *recovery.WriteSession) *Coordinator {
	n := *c
	n.outbox = c.outbox.WithSession(s)
	n.processor = c.processor.WithSession(s)
	return &n
}
func (c *Coordinator) Committed(id string, version, epoch int64) error {
	if !c.enabled() {
		return nil
	}
	for _, kind := range []Kind{Lexical, Person} {
		if _, e := c.outbox.Enqueue(Snapshot{id, version, epoch, kind}); e != nil {
			return e
		}
	}
	return nil
}
func (c *Coordinator) Remove(id string) error { return c.processor.Remove(id) }
func (c *Coordinator) Flush(ctx context.Context) (*DrainResult, error) {
	if !c.enabled() {
		return nil, nil
	}
	result, e := c.processor.Drain(ctx, 64)
	return &result, e
}

// CommittedFrom and RemoveFrom bind existing revision-owner callbacks to their
// exact active capability. They never open a second transaction/connection.
func (c *Coordinator) CommittedFrom(store *assertions.Store, id string, version, epoch int64) error {
	return store.WithWriteSession(func(s *recovery.WriteSession) error { return c.WithSession(s).Committed(id, version, epoch) })
}
func (c *Coordinator) RemoveFrom(store *assertions.Store, id string) error {
	return store.WithWriteSession(func(s *recovery.WriteSession) error { return c.WithSession(s).Remove(id) })
}

// FlushFrom preserves the source facade's post-revision timing when that
// boundary is still inside a larger runtime transaction. The supplied store
// must already be rebound to that currently active enclosing capability.
func (c *Coordinator) FlushFrom(ctx context.Context, store *assertions.Store) (result *DrainResult, err error) {
	err = store.WithWriteSession(func(s *recovery.WriteSession) error {
		var e error
		result, e = c.WithSession(s).Flush(ctx)
		return e
	})
	return
}

// AssertionReader implements the actual source owner's eligibility contract:
// repository Get + !internalUse + Supported, never CurrentApplicable. Future
// validFrom assertions are intentionally indexable before becoming usable.
func AssertionReader(store *assertions.Store, options assertionauthority.Options) EligibleReader {
	return func(session *recovery.WriteSession, id string) (record *Object, err error) {
		if store == nil {
			return nil, errors.New("projection_assertion_store_required")
		}
		err = store.WithSession(session).Read(context.Background(), func(r *assertions.Reader) error {
			value, found, e := r.Get(id)
			if e != nil || !found {
				return e
			}
			candidate, ok := value.(*Object)
			if !ok || candidate == nil {
				return nil
			}
			if truthy(get(candidate, "internalUse")) {
				return nil
			}
			a, e := assertionauthority.New(r, options)
			if e != nil {
				return e
			}
			supported, e := a.Supported(candidate)
			if e == nil && supported {
				record = candidate
			}
			return e
		})
		return
	}
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}
