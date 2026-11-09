package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
	"time"
)

func TestKnowledgeExposureActualProposalOriginalAndWriter(t *testing.T) {
	s, f, options := outerSetup(t)
	f.main.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	input, candidate := outerInput(t, "knowledge-seed")
	candidate.Set("knowledge", []any{outerKnowledge(t, "knowledge-seed")})
	result, err := s.Propose(ctx, input, candidate, options)
	requireOuterSaved(t, result, err)
	turn := exposureLiveTurn(t, s, f)
	raw, err := jsjson.Parse(string(contract.DefaultSettingsInput()))
	exposureMust(t, err)
	settings := raw.(*Object)
	settings.Set("memoryKnowledgeEnabled", true)
	settings.Set("memoryKnowledgeAnswerEnabled", true)
	encoded, err := jsjson.Stringify(settings)
	exposureMust(t, err)
	exposureWrite(t, f, "INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", encoded)
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	policy, err := modelread.NewRuntime(s.d.Assertions, f.events, f.main, f.recovery)
	exposureMust(t, err)
	exposure, err := NewKnowledgeExposure(port, policy, func() int64 { return time.Now().UnixMilli() })
	exposureMust(t, err)
	query := relationObject("anchor", relationObject("kind", "person", "personId", "p"), "predicates", []any{"prefers_topic"}, "depth", float64(1))
	values, err := exposure.Query(ctx, turn, query)
	exposureMust(t, err)
	if len(values) != 1 {
		t.Fatalf("actual proposal has no graph receipt: %v", values)
	}
	edges := value(object(value(values[0], "page")), "edges").([]any)
	if len(edges) != 1 {
		t.Fatalf("actual persisted edge count %d", len(edges))
	}
	if exposure.CaptureGuard(turn) != nil {
		t.Fatal("prepared graph grants learning authority")
	}
	copied, err := exposure.Deliver(ctx, turn, []any{values[0].Clone()})
	exposureMust(t, err)
	if len(copied) != 0 {
		t.Fatal("copy accepted")
	}
	foreign := exposureLiveTurn(t, s, f)
	copied, err = exposure.Deliver(ctx, foreign, []any{values[0]})
	exposureMust(t, err)
	if len(copied) != 0 {
		t.Fatal("foreign turn accepted")
	}
	got, err := exposure.Deliver(ctx, turn, []any{values[0]})
	exposureMust(t, err)
	if len(got) != 1 || got[0] != values[0] {
		t.Fatal("original receipt identity lost")
	}
	entities, err := exposure.ReadEntities(ctx, turn)
	exposureMust(t, err)
	if len(entities) != 1 {
		t.Fatalf("delivered entity count %d", len(entities))
	}
	selected := object(entities[0])
	entityQuery := relationObject("anchor", relationObject("kind", "entity", "entityId", value(selected, "entityId"), "backingRef", value(selected, "backingRef")), "predicates", []any{"prefers_topic"}, "depth", float64(1))
	denied, err := exposure.Query(ctx, foreign, entityQuery)
	exposureMust(t, err)
	if len(denied) != 0 {
		t.Fatal("model-supplied foreign entity accepted")
	}
	entityPage, err := exposure.Query(ctx, turn, entityQuery)
	exposureMust(t, err)
	if len(entityPage) != 1 {
		t.Fatal("delivered entity anchor unavailable")
	}
	guard := exposure.CaptureGuard(turn)
	if guard == nil || !exposureCheck(t, f, guard) {
		t.Fatal("current graph guard missing")
	}
	var expired botruntime.ReadAuthority
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { expired = reads; return nil }))
	if valid, err := guard.Check(expired); err == nil || valid {
		t.Fatal("expired authority accepted", valid, err)
	}
	_, other, _ := outerSetup(t)
	exposureMust(t, other.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		valid, err := guard.Check(reads)
		if err == nil || valid {
			t.Fatal("foreign authority accepted", valid, err)
		}
		return nil
	}))
	rollback := errors.New("test rollback")
	err = f.recovery.WithWriteSession(ctx, f.main, func(writer *recovery.WriteSession) error {
		return f.events.WithSession(writer).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, err := guard.Check(reads)
			if err != nil {
				return err
			}
			if !valid {
				t.Fatal("same-writer graph guard rejected")
			}
			settings.Set("memoryKnowledgeAnswerEnabled", false)
			encoded, err := jsjson.Stringify(settings)
			if err != nil {
				return err
			}
			if _, err = writer.Exec("UPDATE config SET value=? WHERE key='settings'", encoded); err != nil {
				return err
			}
			valid, err = guard.Check(reads)
			if err != nil {
				return err
			}
			if valid {
				t.Fatal("uncommitted knowledge revocation ignored")
			}
			return rollback
		})
	})
	if !errors.Is(err, rollback) {
		t.Fatal("writer rollback outcome", err)
	}
	if !exposureCheck(t, f, guard) {
		t.Fatal("rolled back flag leaked across writers")
	}
}
