package replyplan

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"testing"
)

func TestNativeUndefinedAdapter(t *testing.T) {
	if !isUndefined(jsjson.Undefined) || truthy(jsjson.Undefined) || !deepEqual(Undefined, jsjson.Undefined) || deepEqual(Undefined, nil) {
		t.Fatal("native undefined semantics diverged")
	}
	o := obj()
	set(o, "x", jsjson.Undefined)
	set(o, "y", nil)
	if !has(o, "x") || defined(o, "x") || !defined(o, "y") || !deepEqual(o, clone(o)) {
		t.Fatal("own/clone semantics diverged")
	}
	if raw, err := stringify(o); err != nil || raw != `{"y":null}` {
		t.Fatal(raw, err)
	}
	if raw, err := stringify([]any{jsjson.Undefined, nil}); err != nil || raw != `[null,null]` {
		t.Fatal(raw, err)
	}
	if raw, err := stringify(jsjson.Undefined); err != nil || raw != "" {
		t.Fatal(raw, err)
	}
}
