package memeengine

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
	"testing"
)

func TestRuntimeModelNativeMapAndOrderedArguments(t *testing.T) {
	lone := jsjson.FromUTF16([]uint16{0xd800})
	arguments := fields("z", lone, "a", []any{fields("q", lone), float64(2)})
	cases := []any{
		map[string]any{"role": "assistant", "content": lone, "toolCalls": []any{map[string]any{"id": "call", "name": "synthetic.tool", "arguments": arguments}}},
		fields("role", "assistant", "content", lone, "toolCalls", []any{fields("id", "call", "name", "synthetic.tool", "arguments", arguments)}),
	}
	for i, input := range cases {
		result, e := objectValue(input)
		if e != nil {
			t.Fatalf("%d: %v", i, e)
		}
		if get(result, "content") != lone {
			t.Fatalf("%d: UTF16 content changed", i)
		}
		calls := arr(get(result, "toolCalls"))
		if len(calls) != 1 {
			t.Fatal("lost calls")
		}
		call, e := objectValue(calls[0])
		if e != nil {
			t.Fatal(e)
		}
		raw, e := jsjson.Stringify(get(call, "arguments"))
		if e != nil {
			t.Fatal(e)
		}
		if raw != `{"z":"\ud800","a":[{"q":"\ud800"},2]}` {
			t.Fatalf("ordered arguments or lone surrogate changed: %s", raw)
		}
	}
	for _, bad := range []any{nil, []any{}, "text", map[string]any{"unsupported": make(chan int)}} {
		_, e := objectValue(bad)
		if e == nil {
			t.Fatal("invalid object accepted")
		}
		if _, ok := bad.(map[string]any); !ok && !strings.Contains(e.Error(), "model_response_object_required") {
			t.Fatal(e)
		}
	}
}
