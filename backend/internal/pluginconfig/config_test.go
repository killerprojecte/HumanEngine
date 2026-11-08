package pluginconfig

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"strconv"
	"testing"
)

func parse(t *testing.T, raw string) any {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func same(t *testing.T, v any, want string) {
	t.Helper()
	raw, e := jsjson.Stringify(v)
	if e != nil {
		t.Fatal(e)
	}
	if raw != want {
		t.Fatalf("got %s want %s", raw, want)
	}
}
func TestPreserveSourceSemantics(t *testing.T) {
	schema := parse(t, `{"type":"object","properties":{"token":{"type":"string","secret":true},"nested":{"type":"object","properties":{"token":{"secret":true}}},"array":{"type":"array","items":{"secret":true}},"ordinary":{"type":"string"}}}`).(*jsjson.Object)
	previous := parse(t, `{"token":"secret","nested":{"token":"nested-secret"},"array":["a","b"],"ordinary":"old"}`)
	for _, tc := range []struct{ input, want string }{
		{`{}`, `{"token":"secret"}`},
		{`{"token":null}`, `{"token":"secret"}`},
		{`{"token":""}`, `{"token":""}`},
		{`{"nested":{}}`, `{"nested":{"token":"nested-secret"},"token":"secret"}`},
		{`{"nested":null}`, `{"nested":null,"token":"secret"}`},
		{`{"array":[null,"",null]}`, `{"array":["a","",null],"token":"secret"}`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			v, _ := Preserve(parse(t, tc.input), true, previous, true, schema)
			same(t, v, tc.want)
		})
	}
	same(t, previous, `{"token":"secret","nested":{"token":"nested-secret"},"array":["a","b"],"ordinary":"old"}`)
}
func TestRedactionAndPublicSchema(t *testing.T) {
	schema := parse(t, `{"type":"object","default":{"token":"TOP","unknown":"keep","list":["a","b"]},"properties":{"token":{"type":"string","secret":true,"default":"INNER"},"list":{"type":"array","items":{"type":"string","secret":true,"default":"ITEM"}}}}`).(*jsjson.Object)
	safe := PublicSchema(schema)
	same(t, safe, `{"type":"object","default":{"unknown":"keep","list":[null,null]},"properties":{"token":{"type":"string","secret":true},"list":{"type":"array","items":{"type":"string","secret":true}}}}`)
	v, keep := Redact(parse(t, `{"token":null,"other":"\ud800","list":["hidden"]}`), schema)
	if !keep {
		t.Fatal("object omitted")
	}
	same(t, v, `{"other":"\ud800","list":[null]}`)
	if _, keep := Redact("s", parse(t, `{"secret":true}`).(*jsjson.Object)); keep {
		t.Fatal("secret root retained")
	}
}

func TestSourceDifferential(t *testing.T) {
	raw, err := os.ReadFile("testdata/source.json")
	if err != nil {
		t.Fatal(err)
	}
	rows := parse(t, string(raw)).([]any)
	for i, row := range rows {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			r := row.(*jsjson.Object)
			get := func(k string) any { v, _ := r.Get(k); return v }
			schema := get("schema").(*jsjson.Object)
			expected := func(k string) string {
				s, e := jsjson.Stringify(get(k))
				if e != nil {
					t.Fatal(e)
				}
				return s
			}
			preserved, _ := Preserve(get("input"), true, get("previous"), true, schema)
			same(t, preserved, expected("preserved"))
			redacted, _ := Redact(get("input"), schema)
			same(t, redacted, expected("redacted"))
			same(t, PublicSchema(schema), expected("publicSchema"))
		})
	}
}

func TestPreserveArrayAsPreviousObject(t *testing.T) {
	schema := parse(t, `{"type":"object","properties":{"0":{"secret":true},"length":{"secret":true}}}`).(*jsjson.Object)
	result, _ := Preserve(parse(t, `{}`), true, parse(t, `["old"]`), true, schema)
	same(t, result, `{"0":"old","length":1}`)
}
