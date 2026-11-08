package commitments

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestWorkspaceProvidersUseActualScopedAuthority(t *testing.T) {
	f, as, opts := currentFixture(t)
	ctx := context.Background()
	var escaped *ReadService
	must(t, f.store.ReadCurrent(ctx, as, currentEnabled, opts, func(s *ReadService) error {
		escaped = s
		must(t, s.AssertReady())
		enabled, e := s.Enabled(Context{SceneID: "scene"})
		must(t, e)
		if !enabled {
			t.Fatal("disabled")
		}
		view, e := s.CurrentAssertion(Context{SceneID: "scene"}, Reference{ID: "root", Version: 1})
		must(t, e)
		if view == nil || get(view.Record, "id") != "root" || len(view.Proofs) == 0 {
			t.Fatal("missing actual authority")
		}
		stale, e := s.CurrentAssertion(Context{SceneID: "scene"}, Reference{ID: "root", Version: 2})
		must(t, e)
		if stale != nil {
			t.Fatal("wrong revision accepted")
		}
		heads, e := s.Heads("scene")
		must(t, e)
		if len(heads) == 0 {
			t.Fatal("missing actual heads")
		}
		head, e := s.SourceHead("scene", "root")
		must(t, e)
		if head != nil {
			t.Fatal("legacy metadata invented")
		}
		return nil
	}))
	for _, run := range []func() error{escaped.AssertReady, func() error { _, e := escaped.Enabled(Context{}); return e }, func() error { _, e := escaped.CurrentAssertion(Context{}, Reference{}); return e }, func() error { _, e := escaped.Heads("scene"); return e }, func() error { _, e := escaped.SourceHead("scene", "root"); return e }} {
		if e := run(); e == nil {
			t.Fatal("escaped workspace provider usable")
		}
	}
	must(t, f.ledger.WithWriteSession(ctx, f.main, func(parent *recovery.WriteSession) error {
		return f.store.WithSession(parent).ReadCurrent(ctx, as.WithSession(parent), currentEnabled, opts, func(s *ReadService) error {
			return parent.WithSavepoint(func(child *recovery.WriteSession) error {
				if _, e := s.SourceHead("scene", "root"); e == nil {
					t.Fatal("suspended parent read")
				}
				return f.store.WithSession(child).ReadCurrent(ctx, as.WithSession(child), currentEnabled, opts, func(active *ReadService) error {
					_, e := active.CurrentAssertion(Context{SceneID: "scene"}, Reference{ID: "root", Version: 1})
					return e
				})
			})
		})
	}))
}
