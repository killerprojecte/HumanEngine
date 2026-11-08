package modelread

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sync"
)

// RuntimePolicy binds ticket validation to exactly the same source read owner
// used by recall/send validation, never reopening a root writer from a callback.
type RuntimePolicy struct {
	*Policy
	sources   *sourceevents.Store
	main      *sql.DB
	authority *recovery.Authority
}

func NewRuntime(store *assertions.Store, sources *sourceevents.Store, main *sql.DB, a *recovery.Authority) (*RuntimePolicy, error) {
	if sources == nil {
		return nil, errors.New("model_read_runtime_sources_required")
	}
	if e := store.CheckReadBinding(a, main, nil); e != nil {
		return nil, e
	}
	p, e := New(store)
	if e != nil {
		return nil, e
	}
	r := &RuntimePolicy{p, sources, main, a}
	e = sources.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
		return sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error { return scope.CheckBinding(main, a) })
	})
	if e != nil {
		return nil, e
	}
	return r, nil
}
func (p *RuntimePolicy) validScoped(reads botruntime.ReadAuthority, t *Ticket) (valid bool, err error) {
	if t == nil || t.self != t || t.owner != p.Policy {
		return false, nil
	}
	err = p.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if e := scope.CheckBinding(p.main, p.authority); e != nil {
			return e
		}
		rows, e := scope.Query("SELECT value FROM scenes WHERE id=?", t.scene)
		if e != nil {
			return e
		}
		defer rows.Close()
		var scene *assertions.Object
		if rows.Next() {
			var raw string
			if e := rows.Scan(&raw); e != nil {
				return e
			}
			value, e := jsjson.Parse(raw)
			if e != nil {
				return e
			}
			scene, _ = value.(*assertions.Object)
		}
		if e := rows.Err(); e != nil {
			return e
		}
		enabled, revision, personal, e := sceneState(scene)
		if e != nil {
			return nil
		}
		valid = enabled && revision == t.revision && personal == t.personalization
		return nil
	})
	return
}

type turnState struct {
	mu       sync.Mutex
	ticket   *Ticket
	attached bool
}

func (p *RuntimePolicy) turn(c *botruntime.TurnContext) *turnState {
	return c.OwnedExtensionState(p, func() any { return &turnState{} }).(*turnState)
}

// Begin precedes asynchronous memory work. The original ticket remains on the
// turn after denial and cannot be replaced by toggling settings back on.
func (p *RuntimePolicy) Begin(ctx context.Context, c *botruntime.TurnContext) (*Ticket, error) {
	if c.Signal().Aborted() {
		return nil, errors.New("memory_read_revoked")
	}
	if c.Mode != "real" {
		return nil, errors.New("memory_read_disabled")
	}
	state := p.turn(c)
	state.mu.Lock()
	defer state.mu.Unlock()
	ticket := state.ticket
	if ticket != nil && ticket.scene != c.Scene.ID {
		return nil, errors.New("memory_read_revoked")
	}
	var e error
	if ticket == nil {
		ticket, e = p.Require(ctx, c.Scene.ID)
		if e != nil {
			return nil, e
		}
	}
	if e = p.Assert(ctx, ticket); e != nil {
		return nil, e
	}
	state.ticket = ticket
	if !state.attached {
		c.AddScopedValidation(func(reads botruntime.ReadAuthority) (bool, error) {
			if c.Signal().Aborted() || ticket.scene != c.Scene.ID {
				return false, nil
			}
			return p.validScoped(reads, ticket)
		})
		state.attached = true
	}
	return ticket, nil
}
func (p *RuntimePolicy) GateTool(ctx context.Context, c *botruntime.TurnContext) error {
	if c.Mode != "real" {
		return nil
	}
	ticket, e := p.Begin(ctx, c)
	if e != nil {
		return e
	}
	if !ticket.personalization {
		return errors.New("personalization_disabled")
	}
	return nil
}
func (p *RuntimePolicy) CaptureGuard(c *botruntime.TurnContext) (string, func() bool, bool) {
	state := p.turn(c)
	state.mu.Lock()
	ticket := state.ticket
	state.mu.Unlock()
	if ticket == nil {
		return "", nil, false
	}
	identity, _ := jsjson.Stringify([]any{ticket.scene, ticket.revision, ticket.personalization})
	return identity, func() bool { return p.Valid(context.Background(), ticket) }, true
}
func (p *RuntimePolicy) ValidContext(ctx context.Context, c *botruntime.TurnContext) bool {
	if c.Signal().Aborted() {
		return false
	}
	state := p.turn(c)
	state.mu.Lock()
	ticket := state.ticket
	state.mu.Unlock()
	return ticket == nil || ticket.scene == c.Scene.ID && p.Valid(ctx, ticket)
}
