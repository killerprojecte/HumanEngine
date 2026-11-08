package memoryservice

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
)

func TestSemanticEpisodeSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/episode-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := jsjson.Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	root := object(parsed)
	required := map[string]bool{"src/memory/semantic/episode-anchors.ts": false, "src/memory/semantic/contracts.ts": false, "src/memory/knowledge/contracts.ts": false, "src/memory/knowledge/registry.ts": false, "src/memory/domain/contracts.ts": false, "package-lock.json": false, "backend/internal/memoryservice/testdata/generate-episode-oracle.ts": false}
	for _, raw := range epArr(value(root, "sources")) {
		entry := object(raw)
		path := text(entry, "path")
		if _, ok := required[path]; !ok || required[path] {
			t.Fatalf("unexpected or duplicate source %s", path)
		}
		required[path] = true
		b, e := os.ReadFile(filepath.Join("../../..", path))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != text(entry, "sha256") {
			t.Fatalf("stale source %s", path)
		}
	}
	for path, present := range required {
		if !present {
			t.Fatalf("missing source %s", path)
		}
	}
	for _, rawCase := range epArr(value(root, "cases")) {
		test := object(rawCase)
		t.Run(text(test, "name"), func(t *testing.T) {
			input := object(value(test, "input"))
			ctx := Context{SceneID: text(object(value(input, "context")), "sceneId"), Simulation: value(object(value(input, "context")), "simulation") == true}
			trace := []any{}
			contextObject := func(c Context) *Object { return epObj("sceneId", c.SceneID, "simulation", c.Simulation) }
			p := episodePorts{
				current: func(c Context, r *Object) (*Object, error) {
					trace = append(trace, []any{"current", contextObject(c), r})
					return object(epGet(value(input, "current"), text(r, "id"))), nil
				},
				anchor: func(c Context, r *Object) (*commitments.Assertion, error) {
					trace = append(trace, []any{"anchor", contextObject(c), r})
					v := object(epGet(value(input, "anchors"), text(r, "id")))
					if v == nil {
						return nil, nil
					}
					return &commitments.Assertion{Record: object(value(v, "record")), Proofs: epArr(value(v, "proofs"))}, nil
				},
				query: func(c knowledge.Context, q *Object) (*Object, error) {
					co := epObj("sceneId", c.SceneID, "simulation", c.Simulation, "provenance", c.Provenance)
					if c.ExposedEntities != nil {
						_ = co.Set("exposedEntities", c.ExposedEntities)
					}
					trace = append(trace, []any{"query", co, q})
					key := "page"
					if epGet(value(q, "anchor"), "kind") == "person" {
						key = "first"
					}
					return object(value(input, key)), nil
				},
			}
			got, e := p.discover(ctx, object(value(input, "trigger")))
			if e != nil {
				t.Fatal(e)
			}
			want := value(test, "output")
			actual, e := jsjson.Stringify(got)
			if e != nil {
				t.Fatal(e)
			}
			expected, e := jsjson.Stringify(want)
			if e != nil {
				t.Fatal(e)
			}
			if actual != expected {
				t.Fatalf("episode differs\nGot: %s\nWant: %s", actual, expected)
			}
			if !epEqual(trace, value(test, "trace")) {
				a, _ := jsjson.Stringify(trace)
				w, _ := jsjson.Stringify(value(test, "trace"))
				t.Fatalf("authority/query call order differs\n%s\n%s", a, w)
			}
			for _, raw := range epArr(value(test, "validations")) {
				v := object(raw)
				groups := epArr(value(got, "groups"))
				key := text(v, "key")
				ok, e := p.valid(ctx, object(epGet(groups[0], "anchor")), epArr(value(v, "parents")), &key)
				if e != nil {
					t.Fatal(e)
				}
				if ok != (value(v, "expected") == true) {
					t.Fatalf("valid %s differs", text(v, "name"))
				}
			}
		})
	}
}
