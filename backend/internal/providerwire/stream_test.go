package providerwire

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
)

type chunkBody struct {
	chunks  [][]byte
	closed  bool
	failure error
}

func (b *chunkBody) Read(p []byte) (int, error) {
	if len(b.chunks) == 0 {
		if b.failure != nil {
			return 0, b.failure
		}
		return 0, io.EOF
	}
	n := copy(p, b.chunks[0])
	b.chunks[0] = b.chunks[0][n:]
	if len(b.chunks[0]) == 0 {
		b.chunks = b.chunks[1:]
	}
	return n, nil
}
func (b *chunkBody) Close() error { b.closed = true; return nil }
func TestActualSourceStreams(t *testing.T) {
	raw, e := os.ReadFile("testdata/stream-source.json")
	if e != nil {
		t.Fatal(e)
	}
	fixture, e := DecodeObject(raw)
	if e != nil {
		t.Fatal(e)
	}
	cases := fixture["cases"].([]any)
	if len(cases) != 16 {
		t.Fatal("missing source cases")
	}
	for _, v := range cases {
		c := v.(Object)
		t.Run(c["name"].(string), func(t *testing.T) {
			body := &chunkBody{}
			for _, v := range c["chunks"].([]any) {
				b, e := base64.StdEncoding.DecodeString(v.(string))
				if e != nil {
					t.Fatal(e)
				}
				body.chunks = append(body.chunks, b)
			}
			progress := []any{}
			got, err := ReadStream(body, c["protocol"].(string), func(kind string) {
				progress = append(progress, kind)
				if c["callbackError"] == true {
					panic("synthetic callback")
				}
			})
			if !body.closed {
				t.Fatal("body not closed")
			}
			if want, ok := c["error"].(Object); ok {
				var se *StreamError
				if !errors.As(err, &se) || se.Message != want["message"] || se.Code != want["code"] {
					t.Fatalf("error %v want %v", err, want)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, c["output"]) {
					t.Fatalf("got %s want %s", jsonString(got), jsonString(c["output"]))
				}
			}
			if !reflect.DeepEqual(progress, c["progress"]) {
				t.Fatalf("progress %v want %v", progress, c["progress"])
			}
		})
	}
}
func TestStreamUnicodeSemantics(t *testing.T) {
	if trimJS("\ufeff \t") != "" || trimJS("\u0085") != "\u0085" {
		t.Fatal("ECMAScript whitespace mismatch")
	}
	calls := 0
	r := responsesStream{lengths: map[string]int{}, progress: func(string) { calls++ }}
	r.observe(0, map[string]any{"type": "function_call", "arguments": "é"})
	r.observe(0, map[string]any{"type": "function_call", "arguments": "a "})
	if calls != 1 {
		t.Fatalf("UTF16 progress should ignore added whitespace: %d", calls)
	}
}
func TestSplitSurrogateDelta(t *testing.T) {
	a, e := DecodeJSON([]byte(`"\ud83d"`))
	if e != nil {
		t.Fatal(e)
	}
	b, e := DecodeJSON([]byte(`"\ude00"`))
	if e != nil {
		t.Fatal(e)
	}
	m := Object{}
	appendStream(m, "text", a, func() {})
	appendStream(m, "text", b, func() {})
	if m["text"] != "😀" {
		t.Fatalf("split surrogate not combined: %x", m["text"])
	}
}
