package workingset

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

func TestViewRuntimeUsesExactPublicationAndWriteChild(t *testing.T) {
	f, other := setup(t), setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sources, err := sourceevents.New(f.db, f.authority, sourceevents.Options{})
	must(t, err)

	c := Context{SceneID: "scene"}
	selected := f.snapshots[0].Selection()
	input := []Selection{selected}
	_, err = f.service.SetPinned(ctx, c, selected, 0, true)
	must(t, err)
	resolve := f.service.ports.ResolveCurrent
	f.service.ports.ResolveCurrent = func(scope *Scope, c Context, refs []Reference) ([]Snapshot, error) {
		if scope.WriteSession() != nil {
			t.Fatal("runtime view exposed a writer")
		}
		return resolve(scope, c, refs)
	}
	expect := func(scope sourceevents.RuntimeReadScope, revision float64, pinned bool) {
		t.Helper()
		view, err := f.service.ViewRuntime(scope, c, input, true)
		must(t, err)
		if !view.Enabled || len(view.Items) != 1 {
			t.Fatalf("missing runtime item: %+v", view)
		}
		item := view.Items[0]
		if item.PinStatus != "current" || item.PinRevision != revision || item.PinIntent != pinned || item.Pinned != pinned {
			t.Fatalf("wrong pin in runtime view: %+v", item)
		}
	}
	expired := func(held sourceevents.RuntimeReadScope) {
		t.Helper()
		got, err := f.service.ViewRuntime(held, c, input, true)
		if !errors.Is(err, sql.ErrTxDone) || len(got.Items) != 0 {
			t.Fatalf("expired runtime scope returned %+v, %v", got, err)
		}
	}
	read := func(store *sourceevents.Store, revision float64, pinned bool) error {
		return store.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			var held sourceevents.RuntimeReadScope
			if err := store.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
				held = scope
				expect(scope, revision, pinned)
				got, err := other.service.ViewRuntime(scope, c, input, true)
				if err == nil || len(got.Items) != 0 {
					t.Fatalf("foreign working-set owner returned %+v, %v", got, err)
				}
				return nil
			}); err != nil {
				return err
			}
			// The inner scope has expired but the enclosing read owner remains live.
			expired(held)
			return reads.Ready()
		})
	}
	must(t, read(sources, 1, true))
	must(t, f.authority.WithWriteSession(ctx, f.db, func(parent *recovery.WriteSession) error {
		return parent.WithSavepoint(func(child *recovery.WriteSession) error {
			// The second revision is not yet visible to a root publication.
			if _, err := f.service.WithSession(child).SetPinned(ctx, c, selected, 1, false); err != nil {
				return err
			}
			return read(sources.WithSession(child), 2, false)
		})
	}))
	must(t, read(sources, 2, false))
}

func TestViewRuntimeWitnessOnlyForExactQualifiedSelection(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sources, err := sourceevents.New(f.db, f.authority, sourceevents.Options{})
	must(t, err)
	c := Context{SceneID: "scene"}
	selected := f.snapshots[0].Selection()
	input := []Selection{selected}
	resolve := f.service.ports.ResolveCurrent
	body := new(jsjson.Object)
	_ = body.Set("synthetic", true)
	bind := func(key string) {
		f.service.ports.ResolveView = func(scope *Scope, c Context, refs []Reference) (ViewResolution, error) {
			out, err := resolve(scope, c, refs)
			return ViewResolution{Snapshots: out, CanonicalBodies: map[string]*jsjson.Object{key: body}}, err
		}
	}
	must(t, sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			expectDenied := func() {
				view, bodies, err := f.service.ViewRuntimeWithWitness(scope, c, input)
				if err == nil || len(view.Items) != 0 || len(bodies) != 0 {
					t.Fatal("missing or mismatched body yielded authority")
				}
			}
			expectDenied()
			bind(ReferenceKey(selected.Ref))
			expectDenied()
			bind(SelectionKey(selected))
			view, bodies, err := f.service.ViewRuntimeWithWitness(scope, c, input)
			must(t, err)
			if len(view.Items) != 1 || len(bodies) != 1 || bodies[SelectionKey(selected)] != body {
				t.Fatal("matching witness missing")
			}
			mismatch := selected
			mismatch.DependencyKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			view, bodies, err = f.service.ViewRuntimeWithWitness(scope, c, []Selection{mismatch})
			must(t, err)
			if len(view.Items) != 0 || len(bodies) != 0 {
				t.Fatal("body leaked from a different resolved dependency")
			}
			return nil
		})
	}))
}
