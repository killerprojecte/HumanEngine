package memeservice

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"reflect"
	"testing"
)

func readObjectFixture(t *testing.T, path string) *Object {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	v, e := jsjson.Parse(string(b))
	if e != nil {
		t.Fatal(e)
	}
	return obj(v)
}
func fixtureContext(v any) Context {
	o := obj(v)
	c := Context{SceneID: str(get(o, "sceneId")), Simulation: yes(get(o, "simulation"))}
	for _, v := range arr(get(o, "sourceEvidence")) {
		c.SourceEvidence = append(c.SourceEvidence, obj(v))
	}
	return c
}
func normalizeResult(v any) any {
	switch a := v.(type) {
	case *Object:
		if a == nil {
			return nil
		}
		n := object()
		for _, k := range a.Keys() {
			x := get(a, k)
			if k == "lastUsedAt" {
				x = "TIMESTAMP"
			}
			set(n, k, normalizeResult(x))
		}
		return n
	case []*Object:
		b := []any{}
		for _, v := range a {
			b = append(b, normalizeResult(v))
		}
		return b
	case []any:
		b := []any{}
		for _, v := range a {
			b = append(b, normalizeResult(v))
		}
		return b
	case []string:
		b := []any{}
		for _, v := range a {
			b = append(b, v)
		}
		return b
	}
	return v
}
func TestActualLiveSourceDifferential(t *testing.T) {
	fixture := readObjectFixture(t, "testdata/live-oracle.json")
	pairs := map[string]int{}
	for _, v := range arr(get(fixture, "comparisons")) {
		a := arr(v)
		pairs[str(a[0])+"\x00"+str(a[1])] = int(num(a[2]))
	}
	compare := func(a, b string) (int, error) {
		v, ok := pairs[a+"\x00"+b]
		if !ok {
			return 0, fmt.Errorf("missing pinned source collation %q %q", a, b)
		}
		return v, nil
	}
	for _, v := range arr(get(fixture, "cases")) {
		tc := obj(v)
		t.Run(str(get(tc, "name")), func(t *testing.T) {
			f := newMemeFixture(t)
			ctx := context.Background()
			date, e := jsdate.New("UTC")
			if e != nil {
				t.Fatal(e)
			}
			store, e := assertions.New(f.main, f.authority, assertions.Options{DateParser: date})
			if e != nil {
				t.Fatal(e)
			}
			policy := assertionauthority.Options{DateParser: date, Compare: compare}
			root := t.TempDir()
			s, e := New(ctx, f.main, f.authority, store, policy, root)
			if e != nil {
				t.Fatal(e)
			}
			for _, id := range []string{"s", "other", "off"} {
				saveMemeScene(t, f, fields("id", id, "adapterId", "synthetic", "accountId", "account", "learningEnabled", id != "off"))
			}
			if e = s.transaction(ctx, func(p *scope) error {
				for _, a := range arr(get(tc, "initial")) {
					if e := p.write(obj(a)); e != nil {
						return e
					}
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			for index, v := range arr(get(tc, "steps")) {
				step := obj(v)
				action := obj(get(step, "action"))
				id := str(get(action, "id"))
				var result any
				var err error
				switch str(get(action, "method")) {
				case "update":
					result, err = s.Update(ctx, id, obj(get(action, "patch")))
				case "include":
					result, err = s.AdminSetIncluded(ctx, id, yes(get(action, "included")))
				case "promote":
					result, err = s.PromoteKnown(ctx, fixtureContext(get(action, "ctx")), id)
				case "feedback":
					feedback := obj(get(action, "feedback"))
					err = s.RecordFeedback(ctx, fixtureContext(get(action, "ctx")), id, obj(get(feedback, "source")), yes(get(feedback, "positive")))
				case "recall":
					result, err = s.Recall(ctx, str(get(action, "scene")), str(get(action, "message")))
				case "usage":
					err = s.RecordUsage(ctx, fixtureContext(get(action, "ctx")), id)
				case "search":
					tags := []string{}
					for _, v := range arr(get(action, "tags")) {
						tags = append(tags, str(v))
					}
					result, err = s.Search(ctx, fixtureContext(get(action, "ctx")), str(get(action, "query")), tags)
				case "page":
					input := obj(get(action, "input"))
					o := PageOptions{Query: str(get(input, "query")), Scope: str(get(input, "scope")), State: str(get(input, "state"))}
					if v, ok := get(input, "page").(float64); ok {
						o.Page = &v
					}
					if v, ok := get(input, "pageSize").(float64); ok {
						o.PageSize = &v
					}
					page, e := s.AdminPage(ctx, o)
					err = e
					result = fields("items", normalizeResult(page.Items), "total", float64(page.Total), "page", page.Page, "pageSize", page.PageSize)
				case "restart":
					s, err = New(ctx, f.main, f.authority, store, policy, root)
				case "get":
					result, err = s.AdminGet(ctx, id)
				default:
					t.Fatal("unknown action")
				}
				wantError := str(get(step, "error"))
				if wantError != "" {
					if err == nil || err.Error() != wantError {
						t.Fatalf("step %d error %v want %s", index, err, wantError)
					}
					result = nil
				} else if err != nil {
					t.Fatalf("step %d: %v", index, err)
				}
				actual := normalizeResult(result)
				want := get(step, "result")
				if result != nil && reflect.ValueOf(result).Kind() == reflect.Pointer && reflect.ValueOf(result).IsNil() {
					actual = nil
				}
				if stringify(actual) != stringify(want) {
					t.Fatalf("step %d result\n%s\nwant\n%s", index, stringify(actual), stringify(want))
				}
				snapshot, e := s.AdminList(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if stringify(normalizeResult(snapshot)) != stringify(get(step, "snapshot")) {
					t.Fatalf("step %d snapshot\n%s\nwant\n%s", index, stringify(normalizeResult(snapshot)), stringify(get(step, "snapshot")))
				}
			}
		})
	}
}
