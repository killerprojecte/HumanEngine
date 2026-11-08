package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

func TestEpisodeReadersShareConcreteScopeAndExpire(t *testing.T) {
	f, cs, options := currentReadFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, e := workingset.New(ctx, f.main, f.ledger, workingset.Ports{Enabled: func() bool { return true }, ResolveCurrent: func(*workingset.Scope, workingset.Context, []workingset.Reference) ([]workingset.Snapshot, error) {
		return nil, errors.New("unexpected resolver")
	}})
	must(t, e)
	var held *Reader
	var authority *commitments.ReadService
	var scope *workingset.Scope
	e = w.Read(ctx, func(bound *workingset.Service) error {
		e := bound.ReadScope(ctx, func(s *workingset.Scope) error {
			scope = s
			return f.storage.ReadWorkingSetEpisode(ctx, s, f.assertions, cs, options, func(r *Reader, c *commitments.ReadService) error {
				held, authority = r, c
				page, e := r.Query(Context{SceneID: "s"}, queryInput())
				if e != nil {
					return e
				}
				if len(arr(get(page, "edges"))) != 1 {
					t.Fatal("missing concrete graph")
				}
				view, e := c.CurrentAssertion(commitments.Context{SceneID: "s"}, Reference{ID: "backing-a", Version: 1})
				if e != nil {
					return e
				}
				if view == nil {
					t.Fatal("missing concrete current")
				}
				_, e = c.SourceHead("s", "source")
				return e
			})
		})
		if e != nil {
			return e
		}
		for _, check := range []func() error{held.AssertReady, authority.AssertReady, scope.CheckScope} {
			if !errors.Is(check(), sql.ErrTxDone) {
				t.Fatal("reader escaped callback")
			}
		}
		// The enclosing publication stays valid while its subloan has expired.
		return bound.ReadScope(ctx, func(s *workingset.Scope) error { return s.CheckScope() })
	})
	must(t, e)
	other, otherCS, otherOptions := currentReadFixture(t)
	must(t, w.ReadScope(ctx, func(s *workingset.Scope) error {
		e := other.storage.ReadWorkingSetEpisode(ctx, s, other.assertions, otherCS, otherOptions, func(*Reader, *commitments.ReadService) error { t.Fatal("wrong owner callback"); return nil })
		if e == nil {
			t.Fatal("wrong native owner accepted")
		}
		return nil
	}))
	must(t, w.Transaction(ctx, func(parent *workingset.Service) error {
		return parent.ReadScope(ctx, func(s *workingset.Scope) error {
			return f.storage.WithSession(s.WriteSession()).ReadWorkingSetEpisode(ctx, s, f.assertions, cs, options, func(r *Reader, c *commitments.ReadService) error {
				e := parent.Transaction(ctx, func(child *workingset.Service) error {
					if s.CheckScope() == nil || r.AssertReady() == nil || c.AssertReady() == nil {
						t.Fatal("ancestor used while child active")
					}
					return child.ReadScope(ctx, func(childScope *workingset.Scope) error {
						return f.storage.WithSession(childScope.WriteSession()).ReadWorkingSetEpisode(ctx, childScope, f.assertions, cs, options, func(r *Reader, c *commitments.ReadService) error {
							_, e := r.Query(Context{SceneID: "s"}, queryInput())
							return e
						})
					})
				})
				if e != nil {
					return e
				}
				return errors.Join(s.CheckScope(), r.AssertReady(), c.AssertReady())
			})
		})
	}))
}
