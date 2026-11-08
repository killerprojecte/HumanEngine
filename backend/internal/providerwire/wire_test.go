package providerwire

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSourceDifferential(t *testing.T) {
	b, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name, Method string
			Input        json.RawMessage
			Extra        bool
			Output       any
			Error        string
		}
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.Cases) != 49 {
		t.Fatal("source cases missing")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var input any
			if e := json.Unmarshal(c.Input, &input); e != nil {
				t.Fatal(e)
			}
			var got any
			var err error
			names := map[string]string{"memory__read": "memory.read", "lookup": "lookup"}
			switch c.Method {
			case "parseChat":
				got, err = ParseChat(input.(Object), names)
			case "parseResponses":
				got, err = ParseResponses(input.(Object), names)
			case "chatInput":
				got, err = ChatInput(input.(Object), c.Extra)
			case "responsesInput":
				messages := []Object{}
				for _, v := range input.([]any) {
					messages = append(messages, v.(Object))
				}
				got, err = ResponsesInput(messages, c.Extra)
			case "normalizedUsage":
				got, err = NormalizedUsage(input)
			default:
				t.Fatalf("unhandled source method %s", c.Method)
			}
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Fatalf("error = %v; want %s", err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, e := EncodeJSON(got)
			if e != nil {
				t.Fatal(e)
			}
			normalized, e := DecodeJSON(encoded)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(normalized, c.Output) {
				t.Fatalf("got %s; want %s", encoded, jsonString(c.Output))
			}
		})
	}
}
func TestUsageActualSource(t *testing.T) {
	b, e := os.ReadFile("testdata/usage-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Cases []struct {
			Input      Object
			Convention string
			Embedding  bool
			Output     Object
		}
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	for i, c := range f.Cases {
		got := NormalizeProviderUsage(c.Input, c.Convention, c.Embedding)
		if !reflect.DeepEqual(got, c.Output) {
			t.Fatalf("case %d got %s want %s", i, jsonString(got), jsonString(c.Output))
		}
	}
}
func TestOrderedContextUnicode(t *testing.T) {
	messages, e := ExpandContextMessages([]Object{{"role": "user", "contextLayout": "messages", "content": "{\"sceneKind\":\"test\",\"timezone\":\"UTC\",\"messages\":[{\"z\":1e-7,\"a\":\"\\ud800\"}],\"z\":1,\"a\":2}"}})
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"{\"sceneKind\":\"test\",\"timezone\":\"UTC\"}", "{\"messages\":[{\"z\":1e-7,\"a\":\"\\ud800\"}]}", "{\"z\":1,\"a\":2}"}
	for i, s := range want {
		if messages[i+1]["content"] != s {
			t.Fatalf("block %d: got %s want %s", i, messages[i+1]["content"], s)
		}
	}
	if WireName("工具.😀.read") != "工具__😀__read" {
		t.Fatal("unicode tool name changed")
	}
}
func TestNativeSchemaAndContext(t *testing.T) {
	s := Object{"anyOf": []any{Object{"type": "object"}, Object{"type": "object"}}}
	got := NativeToolSchema(s)
	if got["type"] != "object" {
		t.Fatal(got)
	}
	if _, ok := s["type"]; ok {
		t.Fatal("mutated input schema")
	}
	messages, e := ExpandContextMessages([]Object{{"role": "user", "content": "{\"messages\":[{\"text\":\"synthetic\"}],\"request\":\"now\"}", "contextLayout": "messages", "contextCacheBoundaries": []any{0}}})
	if e != nil {
		t.Fatal(e)
	}
	if len(messages) != 3 || messages[0]["promptCacheBreakpoint"] != true || messages[1]["promptCacheBreakpoint"] != true {
		t.Fatal(messages)
	}
}
