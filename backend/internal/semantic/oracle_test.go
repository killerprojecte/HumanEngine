package semantic

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestActualSourcePurgeOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	if e != nil {
		t.Fatalf("Generate the actual TypeScript source oracle before integration validation: node --import tsx backend/tests/generate-semantic-purge-fixtures.ts: %v", e)
	}
	var fixture struct {
		Sources   map[string]string
		Contracts []struct {
			Name, Kind string
			Input      json.RawMessage
			Valid      bool
			Canonical  string
		}
		Cases []struct {
			Name                                         string
			Candidates, Attempts                         []json.RawMessage
			Target                                       string
			ExpectedCandidates, ExpectedAttempts, Events json.RawMessage
		}
	}
	semMust(t, json.Unmarshal(raw, &fixture))
	for path, hash := range fixture.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", path))
		semMust(t, e)
		if fmt.Sprintf("%x", sha256.Sum256(b)) != hash {
			t.Fatalf("stale source %s", path)
		}
	}
	for _, c := range fixture.Contracts {
		t.Run("contract/"+c.Name, func(t *testing.T) {
			var value *jsjson.Object
			var e error
			if c.Kind == "candidate" {
				value, e = ParseCandidate(string(c.Input))
			} else {
				value, e = ParseAttempt(string(c.Input))
			}
			if (e == nil) != c.Valid {
				t.Fatalf("source validation mismatch: %v", e)
			}
			if e == nil {
				raw, e := jsjson.Stringify(value)
				semMust(t, e)
				if raw != c.Canonical {
					t.Fatalf("canonical mismatch: %s vs %s", raw, c.Canonical)
				}
			}
		})
	}
	if len(fixture.Cases) < 4 {
		t.Fatal("incomplete source oracle")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			f := semSetup(t)
			semMust(t, f.run(func(s *Store) error {
				for _, raw := range c.Candidates {
					o, e := ParseCandidate(string(raw))
					if e != nil {
						return e
					}
					if e = s.Write(o, nil, "admit"); e != nil {
						return e
					}
				}
				for _, raw := range c.Attempts {
					o, e := ParseAttempt(string(raw))
					if e != nil {
						return e
					}
					if e = s.WriteAttempt(o, nil); e != nil {
						return e
					}
				}
				return nil
			}))
			semMust(t, f.run(func(s *Store) error { return s.PurgeAssertion(c.Target) }))
			actualCandidates := []map[string]any{}
			actualAttempts := []map[string]any{}
			var exists int
			semMust(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_semantic_candidates'").Scan(&exists))
			if exists > 0 {
				r, e := f.main.Query("SELECT id,scene_id,input_key,version,value FROM memory_semantic_candidates ORDER BY id")
				semMust(t, e)
				for r.Next() {
					var id, scene, key string
					var version int64
					var value any
					semMust(t, r.Scan(&id, &scene, &key, &version, &value))
					actualCandidates = append(actualCandidates, map[string]any{"id": id, "scene_id": scene, "input_key": key, "version": version, "value": value})
				}
				semMust(t, r.Err())
				semMust(t, r.Close())
			}
			semMust(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_semantic_attempts'").Scan(&exists))
			if exists > 0 {
				r, e := f.main.Query("SELECT scene_id,input_key,value FROM memory_semantic_attempts ORDER BY input_key")
				semMust(t, e)
				for r.Next() {
					var scene, key, value string
					semMust(t, r.Scan(&scene, &key, &value))
					actualAttempts = append(actualAttempts, map[string]any{"scene_id": scene, "input_key": key, "value": value})
				}
				semMust(t, r.Err())
				semMust(t, r.Close())
			}
			oracleEqual(t, actualCandidates, c.ExpectedCandidates)
			oracleEqual(t, actualAttempts, c.ExpectedAttempts)
			actualEvents := []json.RawMessage{}
			for _, event := range semEvents(t, f, len(c.Candidates)+len(c.Attempts)) {
				raw, e := jsjson.Marshal(event)
				semMust(t, e)
				actualEvents = append(actualEvents, json.RawMessage(raw))
			}
			oracleEqual(t, actualEvents, c.Events)
		})
	}
}
func oracleEqual(t *testing.T, actual any, expected json.RawMessage) {
	t.Helper()
	raw, e := json.Marshal(actual)
	semMust(t, e)
	var a, b any
	semMust(t, json.Unmarshal(raw, &a))
	semMust(t, json.Unmarshal(expected, &b))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("source mismatch\nactual %s\nexpected %s", raw, expected)
	}
}
