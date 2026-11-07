package replyplan

import (
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestUndefinedArrayIDRetainsRelationClosure(t *testing.T) {
	// Source String(["m", undefined]) is "m,", so both records are selected.
	// The second record has no closure and must not be silently ignored.
	records := []any{
		obj(member("id", "m,"), member("relationEvidence", []any{obj(member("target", obj(member("entityId", "entity"))), member("requiredMemoryIds", []any{"m,"}))})),
		obj(member("id", []any{"m", Undefined})),
	}
	if err := ValidateRelationSelection(records, []string{"m,"}); err == nil || err.Error() != "planner_incomplete_relation_evidence" {
		t.Fatalf("source relation closure lost: %v", err)
	}
}

func TestJavaScriptArrayStringCoercion(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{
		{[]any{Undefined}, ""},
		{[]any{"m", Undefined}, "m,"},
		{[]any{[]any{Undefined}, nil}, ","},
		{[]any{obj()}, "[object Object]"},
	} {
		got, err := jsString(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
}

func TestNodeBase64UsesUTF16LowBytes(t *testing.T) {
	// Node v24.22.1 Buffer.from(base64url) truncates non-ASCII code units
	// before decoding, including padding. These are intentionally noncanonical.
	units := []uint16{}
	for _, b := range []byte("Im9rIg") {
		units = append(units, uint16(b)+0x100)
	}
	units = append(units, 0x13d)
	if got := decodeReference(jsjson.FromUTF16(units) + "ignored"); got != "ok" {
		t.Fatalf("Node low-byte base64 semantics changed: %v", got)
	}
}

func TestUndefinedRetrievedMessageIsNotIgnored(t *testing.T) {
	plan := parse(t, `{"action":"reply","targetMessageIds":[],"contextMessageIds":[],"strategy":"","constraints":[],"facts":[],"assets":[],"emoji":[],"actions":[]}`)
	input := ValidationInput{Evidence: []*Object{obj(member("id", "r"), member("toolName", "messages.get"), member("value", obj(member("items", []any{Undefined}))))}}
	if _, err := (&Policy{}).ValidateReplyPlan(plan, input); err == nil || err.Error() != "Cannot read properties of undefined (reading 'id')" {
		t.Fatalf("source nullish record failure lost: %v", err)
	}
}
