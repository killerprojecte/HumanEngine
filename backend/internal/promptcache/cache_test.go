package promptcache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func object(m ...jsjson.Member) *jsjson.Object {
	o, e := jsjson.NewObject(m...)
	if e != nil {
		panic(e)
	}
	return o
}
func member(k string, v any) jsjson.Member { return jsjson.Member{Key: k, Value: v} }
func get(v any, k string) any              { x, _ := v.(*jsjson.Object).Get(k); return x }
func number(v any, k string) int           { return int(get(v, k).(float64)) }
func stringValue(v any, k string) string   { return get(v, k).(string) }
func snapshot(w *Windows, b *Boundaries) any {
	windowRows, boundaryRows := []any{}, []any{}
	entriesValue := func(es []digestEntry) []any {
		out := []any{}
		for _, e := range es {
			out = append(out, object(member("id", e.id), member("hash", e.hash)))
		}
		return out
	}
	for e := w.windows.order.Front(); e != nil; e = e.Next() {
		r := e.Value.(row[prefix])
		windowRows = append(windowRows, []any{r.key, object(member("signature", r.value.signature), member("entries", entriesValue(r.value.entries)))})
	}
	for e := b.states.order.Front(); e != nil; e = e.Next() {
		r := e.Value.(row[*boundaryState])
		var value any
		if p := r.value.successful; p != nil {
			value = object(member("authority", p.signature), member("entries", entriesValue(p.entries)))
		}
		boundaryRows = append(boundaryRows, []any{r.key, value})
	}
	return object(member("windows", windowRows), member("boundaries", boundaryRows))
}
func TestActualSourceTransitions(t *testing.T) {
	var fixture struct {
		Node         string
		SourceHashes map[string]string
		Steps        []struct {
			Raw    string
			Result json.RawMessage
			Error  *string
			Digest string
		}
	}
	bytes, err := os.ReadFile("testdata/source-node.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Node != "v24.22.1" {
		t.Fatal(fixture.Node)
	}
	for path, want := range fixture.SourceHashes {
		data, err := os.ReadFile("../../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Fatalf("source changed: %s", path)
		}
	}
	var windows Windows
	var boundaries Boundaries
	tickets := map[string]*Ticket{}
	for i, step := range fixture.Steps {
		op, e := jsjson.Parse(step.Raw)
		if e != nil {
			t.Fatal(e)
		}
		var result any
		var callErr error
		kind := stringValue(op, "kind")
		key, _ := get(op, "key").(string)
		current := []Entry{}
		if rows, ok := get(op, "current").([]any); ok {
			for _, v := range rows {
				current = append(current, Entry{stringValue(v, "id"), v})
			}
		}
		switch kind {
		case "select":
			opts := get(op, "options")
			required := []string{}
			for _, v := range get(op, "required").([]any) {
				required = append(required, v.(string))
			}
			selected, e := windows.Select(key, current, Options{get(opts, "enabled").(bool), number(opts, "maxMessages"), number(opts, "retainMessages"), number(opts, "maxBytes")}, get(op, "authority"), required)
			callErr = e
			if e == nil {
				ids := []any{}
				for _, v := range selected {
					ids = append(ids, v.ID)
				}
				result = ids
			}
		case "prepare":
			ticket, e := boundaries.Prepare(key, current, get(op, "authority"))
			callErr = e
			if e == nil {
				tickets[stringValue(op, "ticket")] = ticket
				indexes := []any{}
				for _, v := range ticket.ContextCacheBoundaries {
					indexes = append(indexes, float64(v))
				}
				result = indexes
			}
		case "commit":
			tickets[stringValue(op, "ticket")].Commit()
		case "delete":
			windows.Delete(key)
			boundaries.Delete(key)
		case "clear":
			windows.Clear()
			boundaries.Clear()
		default:
			t.Fatal(kind)
		}
		if step.Error != nil {
			if callErr == nil || callErr.Error() != *step.Error {
				t.Fatalf("step %d %s error got %v want %s", i, kind, callErr, *step.Error)
			}
		} else if callErr != nil {
			t.Fatalf("step %d: %v", i, callErr)
		}
		// Keep UTF-16 identity in result IDs too: encoding/json's any decoder
		// would replace lone surrogates with U+FFFD before comparison.
		expected, e := jsjson.ParseBytes(step.Result)
		if e != nil {
			t.Fatal(e)
		}
		want, e := jsjson.Marshal(expected)
		if e != nil {
			t.Fatal(e)
		}
		got, e := jsjson.Marshal(result)
		if e != nil {
			t.Fatal(e)
		}
		if string(got) != string(want) {
			t.Fatalf("step %d result got %s want %s", i, got, want)
		}
		h, _, e := digest(snapshot(&windows, &boundaries))
		if e != nil || h != step.Digest {
			t.Fatalf("step %d %s state hash got %s want %s err %v", i, kind, h, step.Digest, e)
		}
	}
}
func TestConcurrentStaleTicketCommit(t *testing.T) {
	var b Boundaries
	old, e := b.Prepare("turn", []Entry{{"old", object(member("id", "old"))}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	fresh, e := b.Prepare("turn", []Entry{{"new", object(member("id", "new"))}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); old.Commit(); fresh.Commit() }()
	}
	wg.Wait()
	next, e := b.Prepare("turn", []Entry{{"new", object(member("id", "new"))}, {"next", object(member("id", "next"))}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.ContextCacheBoundaries) != 2 || next.ContextCacheBoundaries[0] != 0 {
		t.Fatal(next.ContextCacheBoundaries)
	}
	b.Clear()
	next.Commit()
	if b.states.order.Len() != 0 {
		t.Fatal("cleared ticket republished")
	}
}
