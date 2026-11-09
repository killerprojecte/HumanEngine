package toolresults

import (
	"context"
	"errors"
	"testing"
)

func TestGenerationRejectsRegistrationAfterPriorInvalidation(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		name := "legacy_gap_control"
		if guarded {
			name = "revoked_generation"
		}
		t.Run(name, func(t *testing.T) {
			cache := New(Config{TempDir: t.TempDir()})
			defer cache.Dispose()
			life, cancel := context.WithCancel(context.Background())
			defer cancel()
			owner := Owner{Scope: "scene", SceneID: "scene", PluginID: "web-read"}
			if guarded {
				owner.Generation = NewGeneration(life)
			}
			// Model the precise failed ordering: current passed, then lifecycle revoke
			// and cache invalidation completed, then a new Store registers using a
			// still-live derived context whose AfterFunc delivery has not run.
			cancel()
			cache.InvalidatePlugin("web-read")
			result, err := cache.Store(context.Background(), Text("late synthetic bytes"), owner, Options{Encoding: UTF8})
			cache.mu.Lock()
			entries, writes := len(cache.entries), len(cache.writes)
			cache.mu.Unlock()
			if guarded {
				if !errors.Is(err, ErrCancelled) || result.ResultID != "" || entries != 0 || writes != 0 {
					t.Fatal("revoked generation entered real cache state", result, err, entries, writes)
				}
				nextLife, stop := context.WithCancel(context.Background())
				defer stop()
				owner.Generation = NewGeneration(nextLife)
				fresh, nextErr := cache.Store(context.Background(), Text("new generation bytes"), owner, Options{Encoding: UTF8})
				if nextErr != nil || fresh.ResultID == "" {
					t.Fatal("old revocation blocked re-enabled generation", fresh, nextErr)
				}
			} else {
				// This control documents the preexisting cache contract: InvalidatePlugin
				// does not permanently ban future writes. A generation must be explicit.
				if err != nil || result.ResultID == "" || entries != 1 || writes != 0 {
					t.Fatal("legacy window control did not publish", result, err, entries, writes)
				}
			}
		})
	}
}
