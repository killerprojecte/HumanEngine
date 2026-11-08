package assertionproposal

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
	"testing"
)

func validRecord() *Object {
	return obj("content", "valid", "locked", false, "status", "active", "kind", "fact", "importance", "medium", "confidence", 0.5, "scope", obj("kind", "scene", "sceneId", "s"), "sources", []any{obj("sceneId", "s", "messageId", "m", "senderId", "p")})
}
func TestRecordValidationSourceBoundaries(t *testing.T) {
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name, key string
		value     any
		want      string
	}{
		{"astral accepted", "content", strings.Repeat("😀", 10000), ""},
		{"astral rejected", "content", strings.Repeat("😀", 10001), "invalid_memory"},
		{"empty content", "content", "", "invalid_memory"},
		{"whitespace allowed", "content", " ", ""},
		{"optional null", "validFrom", nil, "invalid_memory"},
		{"date parser overflow normalization", "validFrom", "2024-02-30", ""},
		{"reported fact", "epistemicKind", "reported", "invalid_memory"},
		{"public present null scene", "scope", obj("kind", "sharedPublicKnowledge", "sceneId", nil), "invalid_public_scope"},
		{"scene source mismatch", "scope", obj("kind", "scene", "sceneId", "other"), "memory_scope_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validRecord()
			_ = m.Set(tc.key, tc.value)
			e := ValidateMemory(m, p)
			if tc.want == "" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil || e.Error() != tc.want {
				t.Fatalf("got %v want %s", e, tc.want)
			}
		})
	}
	m := validRecord()
	_ = m.Set("validFrom", "2026-11-09")
	_ = m.Set("validUntil", "2026-11-08")
	if e := ValidateMemory(m, p); e == nil || e.Error() != "invalid_memory" {
		t.Fatalf("reversed range: %v", e)
	}
}
func TestUniqueSourcesJSONIdentityAndSanitization(t *testing.T) {
	a := obj("sceneId", "s", "messageId", "m", "senderId", "p", "eventRef", obj("a", 1.0, "b", 2.0), "extra", "removed")
	b := obj("sceneId", "s", "messageId", "m", "senderId", "p", "eventRef", obj("b", 2.0, "a", 1.0))
	c := a.Clone()
	_ = c.Set("ignored", "also removed")
	got, e := UniqueSources([]any{a, b, c})
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 {
		t.Fatal(len(got))
	}
	if has(got[0], "extra") || has(got[0], "ignored") {
		t.Fatal("extra fields leaked")
	}
	raw, e := jsjson.Stringify(get(got[0], "eventRef"))
	if e != nil || raw != "{\"a\":1,\"b\":2}" {
		t.Fatalf("%s %v", raw, e)
	}
	_ = object(get(a, "eventRef")).Set("a", 9.0)
	if get(get(got[0], "eventRef"), "a") != 1.0 {
		t.Fatal("event ref is aliased")
	}
}
func TestRuntimeUUIDIsRandomV4(t *testing.T) {
	a, e := uuid()
	if e != nil {
		t.Fatal(e)
	}
	b, e := uuid()
	if e != nil {
		t.Fatal(e)
	}
	if a == b || len(a) != 36 || a[14] != '4' || !strings.ContainsRune("89ab", rune(a[19])) {
		t.Fatalf("invalid UUIDs %s %s", a, b)
	}
}
