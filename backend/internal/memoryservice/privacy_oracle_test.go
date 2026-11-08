package memoryservice

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type privacyFixture struct {
	Node, Timezone string
	Sources        map[string]string
	Schemas        map[string]string
	Cases          []struct {
		Name  string
		Steps []manualStep
	}
}

var privacyTables = []string{"config", "scenes", "recall_tombstones", "memories", "memory_sources", "memory_supports", "memory_deleted", "memory_assertion_state", "memory_assertion_revisions", "memory_assertion_revision_sources", "memory_assertion_revision_parents", "memory_command_receipts", "memory_relation_entities", "memory_relation_candidates", "memory_relation_tombstones", "memory_knowledge_edges", "memory_semantic_candidates", "memory_semantic_attempts", "memory_commitment_heads", "memory_commitment_revisions", "memory_commitment_commands", "memory_commitment_transitions", "memory_commitment_temporal_windows", "memory_identity_registry", "memory_working_set_pins", "memory_working_set_items", "memory_working_set_jobs", "memory_working_set_receipts", "memory_projection_outbox", "memory_fts", "memory_person_projection_state"}

func readPrivacyFixture(t *testing.T) privacyFixture {
	t.Helper()
	raw, e := os.ReadFile("testdata/privacy-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f privacyFixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func privacyFixed(t *testing.T, pre manualSnapshot) (*Service, scopedFixture) {
	t.Helper()
	s, f := manualFixed(t)
	flags := map[string]any{}
	for _, r := range pre["config"] {
		if r["key"] == "settings" {
			if e := json.Unmarshal([]byte(r["value"].(string)), &flags); e != nil {
				t.Fatal(e)
			}
		}
	}
	plans, e := commitments.New(f.main, f.recovery, commitments.Options{Compare: s.d.Authority.Compare, DateParser: s.d.Authority.DateParser, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	s.d.Commitments = plans
	s.d.CommitmentsEnabled = func(commitments.Context) (bool, error) { return flags["memoryCommitmentsEnabled"] == true, nil }
	p, e := projections.New(context.Background(), f.main, f.recovery, func() bool { return flags["memoryProjectionEnabled"] == true }, projections.AssertionReader(s.d.Assertions, s.d.Authority), projections.Options{})
	if e != nil {
		t.Fatal(e)
	}
	s.d.Projections = p
	ws, e := workingset.New(context.Background(), f.main, f.recovery, workingset.Ports{Enabled: func() bool { return flags["memoryWorkingSetEnabled"] == true || flags["memoryEpisodesEnabled"] == true }, ResolveCurrent: ResolveWorkingSet(plans, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority), Now: func() float64 { return 1793188800000 }})
	if e != nil {
		t.Fatal(e)
	}
	s.d.WorkingSet = ws
	s, e = New(s.d)
	if e != nil {
		t.Fatal(e)
	}
	return s, f
}
func privacyExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if e := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n > 0
}
func privacySeed(t *testing.T, f scopedFixture, schemas map[string]string, pre manualSnapshot) {
	t.Helper()
	for _, table := range privacyTables {
		if !privacyExists(t, f.main, table) {
			if ddl, ok := schemas[table]; ok {
				if _, e := f.main.Exec(ddl); e != nil {
					t.Fatalf("schema %s: %v", table, e)
				}
			}
		}
	}
	for i := len(privacyTables) - 1; i >= 0; i-- {
		table := privacyTables[i]
		if privacyExists(t, f.main, table) {
			if _, e := f.main.Exec("DELETE FROM " + table); e != nil {
				t.Fatalf("clear %s: %v", table, e)
			}
		}
	}
	for _, table := range privacyTables {
		for _, row := range pre[table] {
			keys := []string{}
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			args := []any{}
			marks := []string{}
			for _, k := range keys {
				args = append(args, row[k])
				marks = append(marks, "?")
			}
			if _, e := f.main.Exec("INSERT INTO "+table+"("+strings.Join(keys, ",")+") VALUES("+strings.Join(marks, ",")+")", args...); e != nil {
				t.Fatalf("seed %s: %v", table, e)
			}
		}
	}
}
func privacySnapshot(t *testing.T, db *sql.DB) manualSnapshot {
	t.Helper()
	out := manualSnapshot{}
	for _, table := range privacyTables {
		out[table] = []map[string]any{}
		if !privacyExists(t, db, table) {
			continue
		}
		rows, e := db.Query("SELECT * FROM " + table)
		if e != nil {
			t.Fatal(e)
		}
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if e = rows.Scan(ptrs...); e != nil {
				t.Fatal(e)
			}
			r := map[string]any{}
			for i, k := range cols {
				switch v := cells[i].(type) {
				case int64:
					r[k] = float64(v)
				case []byte:
					r[k] = string(v)
				default:
					r[k] = v
				}
			}
			out[table] = append(out[table], r)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		if e = rows.Close(); e != nil {
			t.Fatal(e)
		}
	}
	return out
}
func privacyCompare(t *testing.T, got, want manualSnapshot) {
	t.Helper()
	encode := func(rows []map[string]any) []string {
		out := []string{}
		for _, r := range rows {
			row := map[string]any{}
			for k, v := range r {
				row[k] = v
				if raw, ok := v.(string); ok && (strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[")) {
					var parsed any
					if json.Unmarshal([]byte(raw), &parsed) == nil {
						row[k] = parsed
					}
				}
			}
			b, e := json.Marshal(row)
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	}
	for _, table := range privacyTables {
		if !reflect.DeepEqual(encode(got[table]), encode(want[table])) {
			t.Fatalf("%s differs\ngot %v\nwant %v", table, encode(got[table]), encode(want[table]))
		}
	}
}
func TestPrivacyActualSourceFullOwnerPrestates(t *testing.T) {
	data := readPrivacyFixture(t)
	if data.Node != "v24.22.1" || data.Timezone != "UTC" || len(data.Cases) != 18 {
		t.Fatal("oracle coverage/runtime drift")
	}
	for path, want := range data.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", path))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatalf("source drift %s", path)
		}
	}
	mutations, setups := 0, 0
	for _, c := range data.Cases {
		for i, step := range c.Steps {
			if step.Operation != "delete" && step.Operation != "recall" && step.Operation != "prepareRecallTwice" {
				setups++
				continue
			}
			mutations++
			t.Run(fmt.Sprintf("%s/%d-%s", c.Name, i, step.Operation), func(t *testing.T) {
				s, f := privacyFixed(t, step.Before)
				privacySeed(t, f, data.Schemas, step.Before)
				var first string
				if e := json.Unmarshal(step.Args[0], &first); e != nil {
					t.Fatal(e)
				}
				var result any
				var e error
				if step.Operation == "delete" {
					result, e = s.Delete(context.Background(), first)
				} else {
					var message string
					if e = json.Unmarshal(step.Args[1], &message); e != nil {
						t.Fatal(e)
					}
					if step.Operation == "prepareRecallTwice" {
						e = f.store.Transaction(context.Background(), func(st *assertions.Store) error {
							return st.WithWriteSession(func(ws *recovery.WriteSession) error {
								execute, e := s.PrepareRecall(context.Background(), ws, first, message)
								if e != nil {
									return e
								}
								affected, e := execute()
								if e != nil {
									return e
								}
								_, again := execute()
								if again == nil {
									return fmt.Errorf("preparation not consumed")
								}
								result = map[string]any{"affected": affected, "repeatError": again.Error()}
								return nil
							})
						})
					} else {
						result, e = s.Recall(context.Background(), first, message)
					}
				}
				if step.Error != "" {
					if e == nil || e.Error() != step.Error {
						t.Fatalf("error %v want %s", e, step.Error)
					}
				} else if e != nil {
					t.Fatal(e)
				}
				if len(step.Result) > 0 {
					var expected any
					if e = json.Unmarshal(step.Result, &expected); e != nil {
						t.Fatal(e)
					}
					b, _ := json.Marshal(result)
					var actual any
					_ = json.Unmarshal(b, &actual)
					if !reflect.DeepEqual(actual, expected) {
						t.Fatalf("result %s want %s", b, step.Result)
					}
				}
				got, e := privacyNormalizeFreshJobGenerations(privacySnapshot(t, f.main), step.After, step.Before)
				if e != nil {
					t.Fatal(e)
				}
				privacyCompare(t, got, step.After)
			})
		}
	}
	if mutations != 23 || setups != 29 {
		t.Fatalf("coverage drift mutations=%d setups=%d", mutations, setups)
	}
}

// Source mutation-hooks generates randomUUID for each replacement job. The
// oracle pins its process-global generator, including UUIDs consumed by setup
// outside the saved prestate. Only fresh final job generations are alpha-renamed:
// old generations, leases, snapshots, order-independent row identity and every
// other business field retain the normal exact comparison.
var privacyGenerationUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func privacyNormalizeFreshJobGenerations(got, want, before manualSnapshot) (manualSnapshot, error) {
	const table = "memory_working_set_jobs"
	key := func(row map[string]any) string {
		b, _ := json.Marshal([]any{row["scene_id"], row["kind"], row["entity_id"]})
		return string(b)
	}
	decode := func(row map[string]any) (map[string]any, error) {
		raw, ok := row["value"].(string)
		if !ok {
			return nil, fmt.Errorf("job value is not JSON text")
		}
		var v map[string]any
		e := json.Unmarshal([]byte(raw), &v)
		return v, e
	}
	prior := map[string]bool{}
	for _, row := range before[table] {
		v, e := decode(row)
		if e != nil {
			return nil, e
		}
		gen, ok := v["generation"].(string)
		if !ok {
			return nil, fmt.Errorf("prior job generation missing")
		}
		prior[gen] = true
	}
	expected := map[string]map[string]any{}
	for _, row := range want[table] {
		v, e := decode(row)
		if e != nil {
			return nil, e
		}
		expected[key(row)] = v
	}
	out := manualSnapshot{}
	for table, rows := range got {
		out[table] = rows
	}
	out[table] = []map[string]any{}
	usedActual, usedExpected := map[string]bool{}, map[string]bool{}
	for _, row := range got[table] {
		target := expected[key(row)]
		if target == nil {
			out[table] = append(out[table], row)
			continue
		}
		expectedGeneration, ok := target["generation"].(string)
		if !ok || prior[expectedGeneration] {
			out[table] = append(out[table], row)
			continue
		}
		v, e := decode(row)
		if e != nil {
			return nil, e
		}
		generation, ok := v["generation"].(string)
		if !ok || !privacyGenerationUUID.MatchString(generation) || !privacyGenerationUUID.MatchString(expectedGeneration) || prior[generation] || usedActual[generation] || usedExpected[expectedGeneration] {
			return nil, fmt.Errorf("job replacement generation is not fresh and unique")
		}
		usedActual[generation], usedExpected[expectedGeneration] = true, true
		v["generation"] = expectedGeneration
		raw, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		copy := map[string]any{}
		for k, value := range row {
			copy[k] = value
		}
		copy["value"] = string(raw)
		out[table] = append(out[table], copy)
	}
	return out, nil
}

func TestPrivacyGenerationNormalizationPreservesBusinessAndOldTokens(t *testing.T) {
	old := "00000000-0000-4000-8000-000000000001"
	fresh := "00000000-0000-4000-8000-000000000002"
	actual := "00000000-0000-4000-8000-000000000003"
	snapshot := func(gen string) manualSnapshot {
		return manualSnapshot{"memory_working_set_jobs": {{"scene_id": "s", "kind": "commitment", "entity_id": "c", "value": fmt.Sprintf(`{"generation":%q,"lease":null,"snapshot":{"dependencyKey":"exact"}}`, gen)}}}
	}
	before, want, got := snapshot(old), snapshot(fresh), snapshot(actual)
	normalized, e := privacyNormalizeFreshJobGenerations(got, want, before)
	if e != nil {
		t.Fatal(e)
	}
	privacyCompare(t, normalized, want)
	if _, e = privacyNormalizeFreshJobGenerations(snapshot(old), want, before); e == nil {
		t.Fatal("reused prior generation accepted")
	}
	if _, e = privacyNormalizeFreshJobGenerations(snapshot("invalid"), want, before); e == nil {
		t.Fatal("invalid UUID accepted")
	}
	unchanged, e := privacyNormalizeFreshJobGenerations(got, before, before)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(unchanged, got) {
		t.Fatal("old expected generation was normalized")
	}
	if !reflect.DeepEqual(got, snapshot(actual)) {
		t.Fatal("input snapshot mutated")
	}
}
