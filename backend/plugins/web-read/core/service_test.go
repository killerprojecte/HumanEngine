//go:build web_read_candidate

package webread

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSourceServiceParity(t *testing.T) {
	raw, e := os.ReadFile("testdata/node-service.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Tools json.RawMessage  `json:"tools"`
		Cases []map[string]any `json:"cases"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	var wantTools, gotTools any
	json.Unmarshal(fixture.Tools, &wantTools)
	json.Unmarshal(registrations, &gotTools)
	if !reflect.DeepEqual(wantTools, gotTools) {
		t.Fatal("tool registration source mismatch")
	}
	for i, c := range fixture.Cases {
		want := c["expected"]
		delete(c, "expected")
		input, _ := json.Marshal(c)
		raw, _ := json.Marshal(Invoke(input))
		var got any
		json.Unmarshal(raw, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("case %d\ngot %s\nwant %#v", i, raw, want)
		}
	}
}
func TestSourceOrderingAndFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"invalid before cancellation", `{"operation":"plan","tool":"web-read.read","input":{},"cancelled":true}`, `{"result":{"trust":"untrusted_web_content","instruction":` + quote(instruction) + `,"title":"","text":"","truncated":false,"status":"invalid_input"}}`},
		{"cancel before busy", `{"operation":"plan","tool":"web-read.request","input":{"url":"x"},"cancelled":true,"active":2}`, `{"result":{"status":"cancelled","ok":false}}`},
		{"busy before base64", `{"operation":"plan","tool":"web-read.send","input":{"url":"x","method":"POST","bodyEncoding":"base64","body":"bad"},"active":2}`, `{"result":{"status":"busy","ok":false}}`},
		{"action uncertainty", `{"operation":"error","tool":"web-read.send","message":"media_address_not_public","action":true,"requestStarted":true}`, `{"status":"address_not_public","ok":false,"actionOutcome":"unknown","retrySafe":false}`},
		{"cancellation wins", `{"operation":"error","message":"tool_result_failed","cancelled":true,"timedOut":true}`, `{"status":"cancelled","ok":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(Invoke([]byte(tc.in)))
			var got, want any
			json.Unmarshal(raw, &got)
			json.Unmarshal([]byte(tc.want), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s != %s", raw, tc.want)
			}
		})
	}
}
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
