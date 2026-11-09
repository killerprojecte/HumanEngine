package snowlumaruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"testing"
)

func TestOptionalContinuationState(t *testing.T) {
	var initial *jsjson.Object
	wire, err := jsjson.Stringify(obj("state", optionalState(initial)))
	if err != nil || wire != `{"state":null}` {
		t.Fatalf("initial state: %q %v", wire, err)
	}
	state := obj("synthetic", true)
	if optionalState(state) != state {
		t.Fatal("existing guest state identity changed")
	}
	wire, err = jsjson.Stringify(obj("state", optionalState(state)))
	if err != nil || wire != `{"state":{"synthetic":true}}` {
		t.Fatalf("continuation state: %q %v", wire, err)
	}
}
