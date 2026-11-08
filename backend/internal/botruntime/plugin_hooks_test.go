package botruntime

import "testing"

func TestRemovableHookIdentityAndStablePriority(t *testing.T) {
	r := new(Runtime)
	first := r.RegisterHook(Hook{ID: "same", Priority: 2})
	second := r.RegisterHook(Hook{ID: "same", Priority: 2})
	early := r.RegisterHook(Hook{ID: "early", Priority: 1})
	original := r.hooks[1].identity
	replacement := r.hooks[2].identity
	first()
	first()
	if len(r.hooks) != 2 || r.hooks[0].ID != "early" || r.hooks[1].identity != replacement || r.hooks[1].identity == original {
		t.Fatal("removal affected wrong registration")
	}
	second()
	early()
	if len(r.hooks) != 0 {
		t.Fatal("hooks retained")
	}
}
