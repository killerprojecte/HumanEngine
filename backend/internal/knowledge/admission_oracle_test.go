package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Admission's source facade is a separately validated boundary. Replay the exact
// span calls captured from actual KnowledgeService.spans, asserting every input;
// do not duplicate its validation algorithm in this test or create an import cycle.
type admissionOracleSpans struct {
	t     *testing.T
	calls []any
	next  int
}

func (s *admissionOracleSpans) KnowledgeSpans(ctx, p *Object, proofs []any) ([]any, error) {
	s.t.Helper()
	if s.next >= len(s.calls) {
		s.t.Fatal("unexpected source span call")
	}
	expected := s.calls[s.next]
	s.next++
	for _, pair := range []struct{ got, want any }{{ctx, get(expected, "ctx")}, {p, get(expected, "proposal")}, {proofs, get(expected, "proofs")}} {
		if !deepEqual(pair.got, pair.want) {
			s.t.Fatalf("source span input mismatch\nGo %s\nTS %s", jsonText(s.t, pair.got), jsonText(s.t, pair.want))
		}
	}
	if has(expected, "error") {
		return nil, errors.New(str(get(expected, "error")))
	}
	return arr(clone(get(expected, "result"))), nil
}

func TestActualAdmissionSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/admission-oracle.json")
	must(t, e)
	fixture := parse(t, string(raw))
	manifest, e := os.ReadFile("testdata/admission-oracle-sources.json")
	must(t, e)
	expected := map[string]string{}
	must(t, json.Unmarshal(manifest, &expected))
	required := []string{"src/memory/knowledge/service.ts", "src/memory/knowledge/read.ts", "src/memory/knowledge/storage.ts", "src/memory/knowledge/contracts.ts", "src/memory/knowledge/qualification.ts", "src/memory/knowledge/registry.ts", "src/memory/domain/proofs.ts", "src/memory/domain/events.ts", "package-lock.json", "backend/internal/knowledge/testdata/generate-admission-oracle.ts"}
	sources := object(get(fixture, "sources"))
	if sources == nil || len(sources.Members()) != len(required) || len(expected) != len(required) {
		t.Fatal("missing or unexpected admission source hashes")
	}
	if get(fixture, "sourceNode") != "v24.22.1" {
		t.Fatal("unexpected oracle Node version")
	}
	for _, path := range required {
		digest, ok := expected[path]
		if !ok || len(digest) != 64 || get(sources, path) != digest {
			t.Fatal("admission manifest mismatch", path)
		}
		b, e := os.ReadFile(filepath.Join("../../..", path))
		must(t, e)
		if fmt.Sprintf("%x", sha256.Sum256(b)) != digest {
			t.Fatal("stale admission source", path)
		}
	}
	if get(fixture, "sourceTimezone") != "UTC" {
		t.Fatal("unexpected oracle timezone")
	}
	if len(arr(get(fixture, "cases"))) != 68 {
		t.Fatal("admission fixture coverage unexpectedly reduced")
	}
	requiredCases := map[string]bool{"clock-advancing-eligible": false, "clock-advancing-auto-off": false, "clock-advancing-conflict": false, "clock-advancing-read-dependency": false, "clock-advancing-conditional": false, "mixed-stance-backing-conflict-before-subject": false}
	for _, v := range arr(get(fixture, "cases")) {
		if _, ok := requiredCases[str(get(v, "name"))]; ok {
			requiredCases[str(get(v, "name"))] = true
		}
	}
	for name, found := range requiredCases {
		if !found {
			t.Fatal("required admission case missing", name)
		}
	}
	for _, v := range arr(get(fixture, "cases")) {
		t.Run(str(get(v, "name")), func(t *testing.T) {
			f := setup(t)
			run := func(s *Store) error {
				for _, seed := range arr(get(v, "seed")) {
					must(t, s.Put(object(clone(get(seed, "edge"))), str(get(seed, "key"))))
				}
				clockValues := []any{}
				clock := func() int64 {
					v := int64(num(get(v, "now"))) + int64(len(clockValues))*int64(num(get(v, "nowStep")))
					clockValues = append(clockValues, float64(v))
					return v
				}
				r := &Reader{storage: s, ready: func() error { return nil }, dates: utcDate, now: clock, enabled: func(Context) (bool, error) { return get(v, "enabled") == true, nil }, current: func(_ Context, ref Reference) (*Assertion, error) {
					if ref.Version != 1 || ref.DeletionEpoch != 0 {
						return nil, nil
					}
					view := get(get(v, "views"), ref.ID)
					if view == nil {
						return nil, nil
					}
					return &Assertion{Record: object(clone(get(view, "record"))), Proofs: arr(clone(get(view, "proofs")))}, nil
				}}
				spans := &admissionOracleSpans{t: t, calls: arr(get(v, "spanCalls"))}
				sequence := int(num(get(v, "idStart")))
				result, err := r.Admit(s, spans, object(clone(get(v, "ctx"))), object(clone(get(v, "backing"))), arr(clone(get(v, "inputs"))), AdmissionPolicy{LearningEnabled: get(v, "learning") == true, AutoAdmission: get(v, "auto") == true, ID: func() string { sequence++; return fmt.Sprintf("id-%d", sequence) }})
				if has(v, "error") {
					if err == nil || err.Error() != get(v, "error") {
						t.Fatalf("error Go %v TS %v", err, get(v, "error"))
					}
					return nil
				}
				must(t, err)
				created := []any{}
				for _, edge := range result.Created {
					created = append(created, edge)
				}
				rejected := []any{}
				for _, rejection := range result.Rejected {
					rejected = append(rejected, obj("index", float64(rejection.Index), "reason", rejection.Reason))
				}
				actual := obj("created", created, "rejected", rejected)
				if !deepEqual(actual, get(v, "result")) {
					t.Fatalf("admission mismatch\nGo %s\nTS %s", jsonText(t, actual), jsonText(t, get(v, "result")))
				}
				if sequence-int(num(get(v, "idStart"))) != int(num(get(v, "idCalls"))) {
					t.Fatal("ID consumption mismatch")
				}
				if !deepEqual(clockValues, get(v, "clockValues")) {
					t.Fatalf("clock trace Go %v TS %v", clockValues, get(v, "clockValues"))
				}
				if spans.next != len(spans.calls) {
					t.Fatal("source span call count mismatch")
				}
				rows, e := s.Rows(nil, nil)
				must(t, e)
				if len(rows) != len(arr(get(v, "rows"))) {
					t.Fatal("persisted row count mismatch")
				}
				for _, entry := range arr(get(v, "rows")) {
					row, e := s.ByKey(str(get(entry, "key")))
					must(t, e)
					if !deepEqual(row, get(entry, "edge")) {
						t.Fatalf("persisted key/edge mismatch\nGo %s\nTS %s", jsonText(t, row), jsonText(t, get(entry, "edge")))
					}
				}
				return nil
			}
			if get(v, "transaction") == false {
				must(t, run(f.storage))
			} else {
				must(t, f.storage.Transaction(context.Background(), run))
			}
		})
	}
}
