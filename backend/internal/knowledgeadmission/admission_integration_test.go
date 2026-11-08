package knowledgeadmission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func value(o *Object, k string) any { v, _ := o.Get(k); return v }
func admissionFixture(t *testing.T) (scopedFixture, *knowledge.Store, *commitments.Store, *assertions.Coordinator, Options) {
	t.Helper()
	f := scopedSetup(t)
	ctx := context.Background()
	a := testOptions(t)
	graph, e := knowledge.NewStore(f.main, f.recovery)
	must(t, e)
	cs, e := commitments.New(f.main, f.recovery, commitments.Options{DateParser: a.DateParser, Compare: a.Compare})
	must(t, e)
	co, e := assertions.NewCoordinator(f.store, assertions.CoordinatorOptions{HistorySupports: func(s *assertions.Store, id string) (out []any, err error) {
		err = s.Read(ctx, func(r *assertions.Reader) error {
			a, e := assertionauthority.New(r, a)
			if e != nil {
				return e
			}
			out, e = a.HistorySupports(id)
			return e
		})
		return
	}, Retain: func(*assertions.Store, *Object) (bool, error) { return true, nil }, RemoveProjection: func(*assertions.Store, string) error { return nil }, Committed: func(*assertions.Store, string, int64, int64) error { return nil }})
	must(t, e)
	n := 0
	options := Options{ReadOptions: knowledge.ReadOptions{KnowledgeEnabled: true, Authority: a}, AutoAdmission: true, ID: func() string { n++; return fmt.Sprintf("admission-%d", n) }}
	return f, graph, cs, co, options
}
func backingInput(t *testing.T, id string, personal bool) (*Object, *Object) {
	t.Helper()
	person := ""
	if personal {
		person = `,"personId":"p"`
	}
	record := parseObject(t, fmt.Sprintf(`{"id":%q,"scope":{"kind":"scene","sceneId":"s"%s},"content":%q,"kind":"fact","epistemicKind":"explicit","sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"importance":"low","confidence":0.5,"learnedAt":"2026-10-28T00:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified"}`, id, person, "Synthetic admission backing "+id))
	proof := parseObject(t, `{"sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"parents":[],"parentReferences":[],"subjectWitness":{"kind":"self","personId":"p","sourceMessageId":"m"}}`)
	return record, proof
}
func sourceContext(t *testing.T) *Object {
	return parseObject(t, `{"sceneId":"s","simulation":false,"provenance":"source-only","sourceEvidence":[{"sceneId":"s","messageId":"m","senderId":"p","available":true,"isBot":false,"parts":[{"type":"image","url":"synthetic"},{"type":"text","text":"I like 🦊 tools"}]}]}`)
}
func proposal(t *testing.T) *Object {
	return parseObject(t, `{"registryVersion":1,"predicate":"prefers_topic","subject":{"kind":"self","personId":"p"},"object":{"kind":"new","entityKind":"topic","title":"tools"},"polarity":"positive","modality":"asserted","temporalScope":"current","conditions":[],"sourceSpans":[{"messageId":"m","partIndex":1,"start":7,"end":9,"stance":"self"}]}`)
}
func exactRef(t *testing.T, id string) *Object {
	return parseObject(t, fmt.Sprintf(`{"id":%q,"version":1,"deletionEpoch":0}`, id))
}
func writeBacking(t *testing.T, tx *assertions.RevisionTxn, id string, personal bool) error {
	r, p := backingInput(t, id, personal)
	if e := tx.Repository.Write(r); e != nil {
		return e
	}
	if e := tx.Repository.SaveSupports(id, []any{p}); e != nil {
		return e
	}
	return tx.Checkpoint()
}
func TestConcreteAdmissionReadbackPrivacyAndExpiry(t *testing.T) {
	f, graph, cs, co, options := admissionFixture(t)
	ctx := context.Background()
	var result knowledge.AdmissionResult
	var held *knowledge.Reader
	var bound *knowledge.Store
	var active *assertions.Store
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Transaction(ctx, func(child *assertions.RevisionTxn) error {
			active = child.Store
			if e := writeBacking(t, child, "backing", true); e != nil {
				return e
			}
			var e error
			result, e = AdmitInAssertionScope(ctx, graph, child.Store, cs, options, sourceContext(t), exactRef(t, "backing"), []any{proposal(t)})
			if e != nil {
				return e
			}
			if len(result.Created) != 1 || len(result.Rejected) != 0 || value(result.Created[0], "status") != "accepted" {
				t.Fatalf("admit %#v", result)
			}
			return child.Store.WithWriteSession(func(session *recovery.WriteSession) error {
				bound = graph.WithSession(session)
				return bound.ReadAssertionCurrent(ctx, child.Store, cs, options.ReadOptions, func(r *knowledge.Reader) error {
					held = r
					projected, e := r.Project(knowledge.Context{SceneID: "s"}, result.Created[0])
					if e == nil && projected == nil {
						t.Fatal("exact current graph missing")
					}
					return e
				})
			})
		})
	}))
	if e := held.AssertReady(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("reader escaped %v", e)
	}
	_, e := AdmitInAssertionScope(ctx, graph, active, cs, options, sourceContext(t), exactRef(t, "backing"), []any{proposal(t)})
	if !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("child escaped %v", e)
	}
	must(t, graph.Transaction(ctx, func(s *knowledge.Store) error {
		rows, e := s.Rows(nil, nil)
		if e != nil {
			return e
		}
		if len(rows) != 1 {
			t.Fatalf("rows %d", len(rows))
		}
		return nil
	}))
	// An exact source tombstone makes ordinary reads unavailable; maintenance
	// removes persisted proof and edge without rebinding the backing to latest.
	_, e = f.main.Exec(`INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T12:00:00.000Z')`)
	must(t, e)
	must(t, f.store.Transaction(ctx, func(s *assertions.Store) error {
		return s.WithWriteSession(func(session *recovery.WriteSession) error {
			g := graph.WithSession(session)
			if e := g.ReadAssertionCurrent(ctx, s, cs, options.ReadOptions, func(r *knowledge.Reader) error {
				v, e := r.Project(knowledge.Context{SceneID: "s"}, result.Created[0])
				if v != nil {
					t.Fatal("recalled source remained visible")
				}
				return e
			}); e != nil {
				return e
			}
			return knowledge.NewLifecycle(g).PurgeSource("s", "m")
		})
	}))
	if e := bound.RequireTransaction(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("store escaped %v", e)
	}
	must(t, graph.Transaction(ctx, func(s *knowledge.Store) error {
		rows, e := s.Rows(nil, nil)
		if len(rows) != 0 {
			t.Fatal("privacy purge retained graph")
		}
		return e
	}))
}
func TestSemanticRejectionKeepsBackingAndPlansBeforePut(t *testing.T) {
	f, graph, cs, co, options := admissionFixture(t)
	ctx := context.Background()
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := writeBacking(t, tx, "semantic", true); e != nil {
			return e
		}
		bad := proposal(t)
		_ = bad.Set("object", parseObject(t, `{"kind":"new","entityKind":"task","title":"wrong type"}`))
		out, e := AdmitInAssertionScope(ctx, graph, tx.Store, cs, options, sourceContext(t), exactRef(t, "semantic"), []any{proposal(t), bad})
		if e != nil {
			return e
		}
		if len(out.Created) != 0 || len(out.Rejected) != 1 || out.Rejected[0].Index != 1 || out.Rejected[0].Reason != "knowledge_endpoint_type" {
			t.Fatal(out)
		}
		return nil
	}))
	must(t, f.store.Read(ctx, func(r *assertions.Reader) error {
		v, e := r.Record("semantic")
		if v == nil {
			t.Fatal("semantic reject rolled backing back")
		}
		return e
	}))
	var n int
	must(t, f.main.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='memory_knowledge_edges'`).Scan(&n))
	if n != 0 {
		t.Fatal("planning created graph storage before all validation")
	}
}
func TestGraphSecondInsertFailureRollsBackOuterAndNestedTracking(t *testing.T) {
	f, graph, cs, co, options := admissionFixture(t)
	ctx := context.Background()
	// Establish real graph schema before the failure trigger, then remove seed.
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := writeBacking(t, tx, "seed", true); e != nil {
			return e
		}
		out, e := AdmitInAssertionScope(ctx, graph, tx.Store, cs, options, sourceContext(t), exactRef(t, "seed"), []any{proposal(t)})
		if e != nil {
			return e
		}
		return tx.Store.WithWriteSession(func(s *recovery.WriteSession) error {
			return graph.WithSession(s).Remove(value(out.Created[0], "id").(string))
		})
	}))
	_, e := f.main.Exec(`CREATE TRIGGER fail_second_graph BEFORE INSERT ON memory_knowledge_edges WHEN (SELECT count(*) FROM memory_knowledge_edges)>0 BEGIN SELECT RAISE(ABORT,'graph_second_insert'); END`)
	must(t, e)
	run := func(tx *assertions.RevisionTxn) error {
		if e := writeBacking(t, tx, "rollback", true); e != nil {
			return e
		}
		p2 := proposal(t)
		_ = p2.Set("object", parseObject(t, `{"kind":"literal","valueType":"text","value":"different"}`))
		_, e := AdmitInAssertionScope(ctx, graph, tx.Store, cs, options, sourceContext(t), exactRef(t, "rollback"), []any{proposal(t), p2})
		return e
	}
	e = co.Transaction(ctx, run)
	if e == nil || !strings.Contains(e.Error(), "graph_second_insert") {
		t.Fatalf("expected SQL error, got %v", e)
	}
	must(t, f.store.Read(ctx, func(r *assertions.Reader) error {
		v, e := r.Record("rollback")
		if v != nil {
			t.Fatal("SQL failure preserved backing")
		}
		return e
	}))
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		e := tx.Transaction(ctx, run)
		if e == nil || !strings.Contains(e.Error(), "graph_second_insert") {
			return fmt.Errorf("nested SQL error %v", e)
		}
		return writeBacking(t, tx, "survivor", true)
	}))
	must(t, f.store.Read(ctx, func(r *assertions.Reader) error {
		v, e := r.Record("rollback")
		if e != nil {
			return e
		}
		if v != nil {
			t.Fatal("nested failed backing leaked")
		}
		v, e = r.Record("survivor")
		if v == nil {
			t.Fatal("outer sibling lost")
		}
		return e
	}))
	must(t, graph.Transaction(ctx, func(s *knowledge.Store) error {
		rows, e := s.Rows(nil, nil)
		if len(rows) != 0 {
			t.Fatal("first graph insert leaked")
		}
		return e
	}))
}

func TestConcreteAdmissionSourceAndUTF16Boundaries(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*Object, *Object)
	}{
		{"unavailable", "knowledge_span_unavailable", func(c, p *Object) {
			e := value(c, "sourceEvidence").([]any)[0].(*Object)
			_ = e.Set("available", false)
		}},
		{"bot", "knowledge_span_unavailable", func(c, p *Object) { e := value(c, "sourceEvidence").([]any)[0].(*Object); _ = e.Set("isBot", true) }},
		{"foreign-scene", "knowledge_span_unavailable", func(c, p *Object) {
			e := value(c, "sourceEvidence").([]any)[0].(*Object)
			_ = e.Set("sceneId", "foreign")
		}},
		{"wrong-sender", "knowledge_span_unavailable", func(c, p *Object) {
			e := value(c, "sourceEvidence").([]any)[0].(*Object)
			_ = e.Set("senderId", "other")
		}},
		{"nontext-part", "knowledge_span_unavailable", func(c, p *Object) {
			span := value(p, "sourceSpans").([]any)[0].(*Object)
			_ = span.Set("partIndex", float64(0))
		}},
		{"surrogate-start", "knowledge_span_surrogate_boundary", func(c, p *Object) {
			span := value(p, "sourceSpans").([]any)[0].(*Object)
			_ = span.Set("start", float64(8))
		}},
		{"surrogate-end", "knowledge_span_surrogate_boundary", func(c, p *Object) {
			span := value(p, "sourceSpans").([]any)[0].(*Object)
			_ = span.Set("end", float64(8))
		}},
		{"past-end", "knowledge_span_unavailable", func(c, p *Object) {
			span := value(p, "sourceSpans").([]any)[0].(*Object)
			_ = span.Set("end", float64(100))
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, graph, cs, co, options := admissionFixture(t)
			ctx := context.Background()
			must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
				if e := writeBacking(t, tx, "backing", true); e != nil {
					return e
				}
				c, p := sourceContext(t), proposal(t)
				tt.change(c, p)
				out, e := AdmitInAssertionScope(ctx, graph, tx.Store, cs, options, c, exactRef(t, "backing"), []any{p})
				if e != nil {
					return e
				}
				if len(out.Rejected) != 1 || out.Rejected[0].Reason != tt.reason {
					t.Fatalf("got %#v want %s", out, tt.reason)
				}
				return nil
			}))
		})
	}
}
func TestConcreteGeneralAdmissionRealDefaultsAndExactChildBinding(t *testing.T) {
	_, graph, cs, co, options := admissionFixture(t)
	ctx := context.Background()
	options.ID = nil
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := writeBacking(t, tx, "general", false); e != nil {
			return e
		}
		p := parseObject(t, `{"registryVersion":1,"predicate":"has_property","property":"count","subject":{"kind":"new","entityKind":"tool","title":"Tool"},"object":{"kind":"literal","valueType":"number","value":2,"unit":null},"polarity":"positive","modality":"asserted","temporalScope":"current","conditions":[],"sourceSpans":[{"messageId":"m","partIndex":1,"start":7,"end":9,"stance":"asserted"}]}`)
		out, e := AdmitInAssertionScope(ctx, graph, tx.Store, cs, options, sourceContext(t), exactRef(t, "general"), []any{p})
		if e != nil {
			return e
		}
		if len(out.Created) != 1 || value(out.Created[0], "admission") != "direct_assertion_no_visible_conflict" {
			t.Fatal(out)
		}
		id := value(out.Created[0], "id").(string)
		if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("not UUID v4 %s", id)
		}
		return tx.Store.WithWriteSession(func(parent *recovery.WriteSession) error {
			bound := graph.WithSession(parent)
			boundCommitments := cs.WithSession(parent)
			return tx.Transaction(ctx, func(child *assertions.RevisionTxn) error {
				_, e := AdmitInAssertionScope(ctx, bound, child.Store, cs, options, sourceContext(t), exactRef(t, "general"), []any{p})
				if e == nil || !strings.Contains(e.Error(), "knowledge_store_session_binding") {
					t.Fatalf("graph parent rebound %v", e)
				}
				_, e = AdmitInAssertionScope(ctx, graph, child.Store, boundCommitments, options, sourceContext(t), exactRef(t, "general"), []any{p})
				if e == nil || !strings.Contains(e.Error(), "commitment_store_session_binding") {
					t.Fatalf("commitment parent rebound %v", e)
				}
				return nil
			})
		})
	}))
}

func TestDisabledAdmissionDoesNotConstructCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"simulation", "knowledge-off", "learning-off"} {
		t.Run(mode, func(t *testing.T) {
			f, graph, cs, co, options := admissionFixture(t)
			options.Authority.DateParser = nil
			options.Authority.Compare = nil
			options.Authority.NowMillis = func() int64 { t.Fatal("disabled admission read clock"); return 0 }
			c := sourceContext(t)
			if mode == "simulation" {
				_ = c.Set("simulation", true)
			}
			if mode == "knowledge-off" {
				options.KnowledgeEnabled = false
			}
			if mode == "learning-off" {
				_, e := f.main.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account","learningEnabled":false}' WHERE id='s'`)
				must(t, e)
			}
			must(t, co.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
				out, e := AdmitInAssertionScope(context.Background(), graph, tx.Store, cs, options, c, nil, []any{nil})
				if e != nil {
					return e
				}
				if len(out.Rejected) != 1 || out.Rejected[0].Reason != "knowledge_disabled" {
					t.Fatal(out)
				}
				return nil
			}))
		})
	}
}
