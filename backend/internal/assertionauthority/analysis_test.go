package assertionauthority

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type analysisReads struct {
	readPort
	proofs map[string]int
}

func (r *analysisReads) Proofs(id string) ([]any, error) {
	r.proofs[id]++
	return r.readPort.Proofs(id)
}
func TestAnalyzeSupportedPrivatePositiveMemoFreshRootBudgets(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	options := testOptions(t)
	var escaped *Authority
	e := f.store.Transaction(ctx, func(s *assertions.Store) error {
		records := map[string]*Object{}
		put := func(id string, count int, parents ...string) error {
			record := parseObject(t, `{"id":"`+id+`","scope":{"kind":"scene","sceneId":"s"},"content":"`+id+`","status":"active","lineageStatus":"verified","sources":[]}`)
			records[id] = record
			sources := []any{}
			for i := 0; i < count; i++ {
				sources = append(sources, parseObject(t, `{"sceneId":"s","messageId":"source","senderId":"p"}`))
			}
			parentIDs := []any{}
			for _, id := range parents {
				parentIDs = append(parentIDs, id)
			}
			proof := new(Object)
			_ = proof.Set("sources", sources)
			_ = proof.Set("parents", parentIDs)
			raw, e := jsjson.Stringify(record)
			if e != nil {
				return e
			}
			proofRaw, e := jsjson.Stringify([]any{proof})
			if e != nil {
				return e
			}
			return s.WithWriteSession(func(session *recovery.WriteSession) error {
				if _, e := session.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", id, "scene:s", nil, id, id, raw); e != nil {
					return e
				}
				_, e := session.Exec("INSERT INTO memory_supports VALUES(?,?)", id, proofRaw)
				return e
			})
		}
		for _, id := range []string{"one", "two"} {
			if e := put(id, 2500); e != nil {
				return e
			}
		}
		if e := put("shared", 2000); e != nil {
			return e
		}
		if e := put("child-a", 1500, "shared"); e != nil {
			return e
		}
		if e := put("child-b", 1500, "shared"); e != nil {
			return e
		}
		return s.Read(ctx, func(r *assertions.Reader) error {
			return r.WithSupportReadStatements(func(r *assertions.Reader) error {
				a, e := New(r, options)
				if e != nil {
					return e
				}
				escaped = a
				counted := &analysisReads{readPort: a.read, proofs: map[string]int{}}
				a.read = counted
				results, e := a.AnalyzeSupported([]*Object{records["one"], records["two"], records["child-a"], records["child-b"]})
				if e != nil {
					return e
				}
				for _, yes := range results {
					if !yes {
						t.Fatal(results)
					}
				}
				if counted.proofs["shared"] != 1 {
					t.Fatalf("shared positive recomputed %v", counted.proofs)
				}
				// A second call in the same SQL transaction must reread the mutation; the
				// first call's successful provenance is not a reusable authorization cache.
				if e = s.WithWriteSession(func(session *recovery.WriteSession) error {
					_, e := session.Exec("UPDATE memory_supports SET value='[]' WHERE memory_id='shared'")
					return e
				}); e != nil {
					return e
				}
				results, e = a.AnalyzeSupported([]*Object{records["child-a"], records["child-b"]})
				if e != nil {
					return e
				}
				if results[0] || results[1] || counted.proofs["shared"] != 3 {
					t.Fatalf("stale positive/negative memo: %v %v", results, counted.proofs)
				}
				return nil
			})
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = escaped.AnalyzeSupported(nil); e == nil {
		t.Fatal("expired analysis accepted")
	}
}
func TestAnalyzeSupportedReturnsNoPartialPlan(t *testing.T) {
	f := scopedSetup(t)
	e := f.store.Transaction(context.Background(), func(s *assertions.Store) error {
		return s.Read(context.Background(), func(r *assertions.Reader) error {
			a, e := New(r, testOptions(t))
			if e != nil {
				return e
			}
			record := parseObject(t, `{"id":"unsupported","status":"active","lineageStatus":"verified"}`)
			results, e := a.AnalyzeSupported([]*Object{record, nil})
			if e == nil || results != nil || !strings.Contains(e.Error(), "assertion_record_object_required") {
				return errors.New("partial_analysis_plan_exposed")
			}
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
}
