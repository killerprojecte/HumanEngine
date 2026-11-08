package knowledge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type readOracleStorage struct{ rows []*Object }

func (s *readOracleStorage) Rows(scene *string, limit *int) ([]*Object, error) {
	out := []*Object{}
	for _, v := range s.rows {
		if scene == nil || get(v, "sceneId") == *scene {
			out = append(out, v)
		}
	}
	if limit != nil && len(out) > *limit {
		out = out[:*limit]
	}
	return out, nil
}
func (s *readOracleStorage) Get(id string) (*Object, error) {
	for _, v := range s.rows {
		if get(v, "id") == id {
			return v, nil
		}
	}
	return nil, nil
}
func (s *readOracleStorage) Entity(id, scene string) (*Object, error) {
	limit := 201
	rows, _ := s.Rows(&scene, &limit)
	if len(rows) > 200 {
		return nil, fmt.Errorf("knowledge_scan_limit")
	}
	for _, v := range rows {
		for _, p := range []any{get(v, "subject"), get(v, "object")} {
			e := get(p, "entity")
			if get(p, "kind") == "entity" && get(e, "originEdgeId") == get(v, "id") && get(e, "id") == id {
				return object(e), nil
			}
		}
	}
	return nil, nil
}
func TestActualReadSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/read-oracle.json")
	must(t, e)
	fixture := parse(t, string(raw))
	// This is an actual TypeScript source oracle with mocked storage/authority,
	// not evidence that the native composition tests have executed.
	expectedRaw, e := os.ReadFile("testdata/read-oracle-sources.json")
	must(t, e)
	expected := map[string]string{}
	must(t, json.Unmarshal(expectedRaw, &expected))
	required := []string{
		"src/memory/knowledge/read.ts",
		"src/memory/knowledge/query.ts",
		"src/memory/knowledge/contracts.ts",
		"src/memory/knowledge/qualification.ts",
		"src/memory/knowledge/registry.ts",
		"src/memory/domain/proofs.ts",
		"backend/internal/knowledge/testdata/generate-read-oracle.ts",
		"package-lock.json",
	}
	sources := object(get(fixture, "sources"))
	if sources == nil || len(sources.Members()) != len(required) || len(expected) != len(required) {
		t.Fatal("missing or unexpected source/provenance manifest entries")
	}
	for _, path := range required {
		digest, ok := expected[path]
		if !ok || len(digest) != 64 || get(sources, path) != digest {
			t.Fatalf("source/provenance manifest mismatch: %s", path)
		}
		b, e := os.ReadFile(filepath.Join("../../..", path))
		must(t, e)
		if fmt.Sprintf("%x", sha256.Sum256(b)) != digest {
			t.Fatalf("stale source/provenance %s", path)
		}
	}
	for i, v := range arr(get(fixture, "schema")) {
		t.Run(fmt.Sprintf("schema-%d", i), func(t *testing.T) {
			p, e := ParseQuery(get(v, "input"), get(v, "tool") == true)
			if (e == nil) != (get(v, "valid") == true) {
				t.Fatal("schema mismatch", e)
			}
			if e == nil && jsonText(t, p) != get(v, "canonical") {
				t.Fatal("schema canonical mismatch")
			}
		})
	}
	for _, v := range arr(get(fixture, "cases")) {
		t.Run(str(get(v, "name")), func(t *testing.T) {
			storage := &readOracleStorage{}
			for _, row := range arr(get(v, "rows")) {
				storage.rows = append(storage.rows, object(clone(row)))
			}
			r := &Reader{storage: storage, ready: func() error { return nil }, dates: utcDate, now: func() int64 { return int64(num(get(v, "now"))) }, enabled: func(Context) (bool, error) { return get(v, "enabled") == true, nil }, current: func(_ Context, reference Reference) (*Assertion, error) {
				if reference.Version != 1 || reference.DeletionEpoch != 0 {
					return nil, nil
				}
				view := get(get(v, "views"), reference.ID)
				if view == nil {
					return nil, nil
				}
				return &Assertion{Record: object(clone(get(view, "record"))), Proofs: arr(clone(get(view, "proofs")))}, nil
			}}
			c := get(v, "ctx")
			ctx := Context{SceneID: str(get(c, "sceneId")), Simulation: get(c, "simulation") == true, Provenance: str(get(c, "provenance")), ExposedEntities: arr(get(c, "exposedEntities"))}
			var result *Object
			var err error
			switch get(v, "op") {
			case "discover":
				result, err = r.Discover(ctx, object(get(v, "exact")), object(get(v, "input")))
			case "project":
				result, err = r.Project(ctx, storage.rows[0])
			default:
				result, err = r.Query(ctx, object(get(v, "input")))
			}
			if has(v, "error") {
				if err == nil || err.Error() != get(v, "error") {
					t.Fatalf("error mismatch Go %v TS %v", err, get(v, "error"))
				}
				return
			}
			must(t, err)
			if result == nil && get(v, "result") == nil {
				return
			}
			if !deepEqual(result, get(v, "result")) {
				t.Fatalf("result mismatch\nGo %s\nTS %s", jsonText(t, result), jsonText(t, get(v, "result")))
			}
		})
	}
}
