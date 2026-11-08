package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type unusedAdmissionSpans struct{ t *testing.T }

func (s unusedAdmissionSpans) KnowledgeSpans(*Object, *Object, []any) ([]any, error) {
	s.t.Fatal("unexpected spans after failed current lookup")
	return nil, nil
}
func TestAdmissionPlanningSQLCatchAndInitialShortCircuit(t *testing.T) {
	for _, readDependency := range []bool{false, true} {
		name := "proposal"
		if readDependency {
			name = "read-dependency"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			must(t, f.storage.Transaction(context.Background(), func(s *Store) error {
				r := &Reader{storage: s, ready: s.RequireTransaction, dates: utcDate, now: func() int64 { return 0 }, enabled: func(Context) (bool, error) { return true, nil }, current: func(Context, Reference) (*Assertion, error) {
					_, e := s.session.Query("SELECT * FROM synthetic_missing_table")
					return nil, e
				}}
				ctx := obj("sceneId", "s", "provenance", "contextual")
				backing := obj("id", "backing", "version", float64(1), "deletionEpoch", float64(0))
				if readDependency {
					_ = ctx.Set("exposedAssertions", []any{backing})
				}
				p := parse(t, `{"registryVersion":1,"predicate":"prefers_topic","subject":{"kind":"self","personId":"p"},"object":{"kind":"literal","valueType":"text","value":"synthetic"},"polarity":"positive","modality":"asserted","temporalScope":"current","conditions":[],"sourceSpans":[{"messageId":"m","partIndex":0,"start":0,"end":1,"stance":"self"}]}`)
				out, e := r.Admit(s, unusedAdmissionSpans{t}, ctx, backing, []any{p}, AdmissionPolicy{LearningEnabled: true})
				if e != nil {
					return e
				}
				reason, index := "knowledge_proposal_invalid", 0
				if readDependency {
					reason, index = "knowledge_read_dependencies_invalid", -1
				}
				if len(out.Rejected) != 1 || out.Rejected[0].Reason != reason || out.Rejected[0].Index != index {
					t.Fatal(out)
				}
				// Ordinary statement errors do not lose the active transaction.
				return s.RequireTransaction()
			}))
		})
	}
	f := setup(t)
	must(t, f.storage.Transaction(context.Background(), func(s *Store) error {
		r := &Reader{storage: s, ready: func() error { t.Fatal("simulation called current readiness"); return nil }, enabled: func(Context) (bool, error) { t.Fatal("simulation evaluated enabled"); return false, nil }}
		out, e := r.Admit(s, nil, obj("simulation", true), nil, nil, AdmissionPolicy{})
		if e != nil {
			return e
		}
		if len(out.Rejected) != 1 || out.Rejected[0].Reason != "knowledge_disabled" {
			t.Fatal(out)
		}
		r.enabled = func(Context) (bool, error) { return false, errors.New("enabled-read-failed") }
		_, e = r.Admit(s, nil, obj(), nil, nil, AdmissionPolicy{})
		if e == nil || e.Error() != "enabled-read-failed" {
			t.Fatalf("initial enabled error swallowed %v", e)
		}
		return nil
	}))
}

// The source catches a planning failure, but the concrete native write owner
// must still observe automatic rollback and reject all subsequent operations.
func TestAdmissionCaughtPlanningRollbackCannotAutocommit(t *testing.T) {
	f := setup(t)
	_, e := f.main.Exec(`CREATE TABLE admission_loss_probe(value TEXT); INSERT INTO admission_loss_probe VALUES('original'); CREATE TRIGGER admission_loss BEFORE UPDATE ON admission_loss_probe WHEN NEW.value='abort' BEGIN SELECT RAISE(ROLLBACK,'admission_native_rollback'); END`)
	must(t, e)
	var first, after error
	e = f.storage.Transaction(context.Background(), func(s *Store) error {
		if _, e := s.session.Exec(`INSERT INTO admission_loss_probe VALUES('staged')`); e != nil {
			return e
		}
		r := &Reader{storage: s, ready: s.RequireTransaction, dates: utcDate, now: func() int64 { return 0 }, enabled: func(Context) (bool, error) { return true, nil }, current: func(Context, Reference) (*Assertion, error) {
			_, first = s.session.Exec(`UPDATE admission_loss_probe SET value='abort' WHERE value='original'`)
			return nil, first
		}}
		p := parse(t, `{"registryVersion":1,"predicate":"prefers_topic","subject":{"kind":"self","personId":"p"},"object":{"kind":"literal","valueType":"text","value":"synthetic"},"polarity":"positive","modality":"asserted","temporalScope":"current","conditions":[],"sourceSpans":[{"messageId":"m","partIndex":0,"start":0,"end":1,"stance":"self"}]}`)
		out, err := r.Admit(s, unusedAdmissionSpans{t}, obj("sceneId", "s"), obj("id", "backing", "version", float64(1), "deletionEpoch", float64(0)), []any{p}, AdmissionPolicy{LearningEnabled: true})
		if err != nil {
			return err
		}
		if len(out.Rejected) != 1 || out.Rejected[0].Reason != "knowledge_proposal_invalid" {
			t.Fatal(out)
		}
		_, after = s.session.Exec(`INSERT INTO admission_loss_probe VALUES('unacknowledged')`)
		return nil // Deliberately ignore the caught failure and capability failure.
	})
	if first == nil || !strings.Contains(first.Error(), "admission_native_rollback") || !errors.Is(after, sql.ErrTxDone) || e == nil {
		t.Fatalf("lost transaction escaped: first=%v after=%v owner=%v", first, after, e)
	}
	var count int
	must(t, f.main.QueryRow(`SELECT count(*) FROM admission_loss_probe`).Scan(&count))
	if count != 1 {
		t.Fatal("staged or post-rollback write escaped", count)
	}
	var original string
	must(t, f.main.QueryRow(`SELECT value FROM admission_loss_probe`).Scan(&original))
	if original != "original" {
		t.Fatal("rollback changed original", original)
	}
	var graphTables int
	must(t, f.main.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='memory_knowledge_edges'`).Scan(&graphTables))
	if graphTables != 0 {
		t.Fatal("planning loss created graph schema")
	}
}
