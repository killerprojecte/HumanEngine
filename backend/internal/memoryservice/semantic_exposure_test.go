package memoryservice

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

func TestSemanticExposureActualProposalsOriginalAndWriter(t *testing.T) {
	s, f, options := outerSetup(t)
	f.main.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	settingsRaw, e := jsjson.Parse(string(contract.DefaultSettingsInput()))
	exposureMust(t, e)
	settings := settingsRaw.(*Object)
	for _, key := range []string{"memorySemanticEnabled", "memorySemanticAnswerEnabled", "memorySemanticAutoAdmissionEnabled"} {
		_ = settings.Set(key, true)
	}
	encoded, e := jsjson.Stringify(settings)
	exposureMust(t, e)
	// Bootstrap fixture schema exactly as semanticOwnerSetup; application DDL
	// is not authorized inside the later runtime WriteSession.
	_, e = f.main.Exec("CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY,value TEXT NOT NULL)")
	exposureMust(t, e)
	exposureWrite(t, f, "INSERT OR REPLACE INTO config VALUES('settings',?)", encoded)
	refs := []*Object{}
	for _, id := range []string{"semantic-source-a", "semantic-source-b"} {
		input, candidate := outerInput(t, id)
		result, e := s.Propose(ctx, input, candidate, options)
		requireOuterSaved(t, result, e)
		refs = append(refs, outerRef(t, f, result))
	}
	policy, e := modelread.New(f.store)
	exposureMust(t, e)
	ticket, e := policy.Require(ctx, "s")
	exposureMust(t, e)
	current, e := s.SemanticCurrent(ctx, Context{SceneID: "s"}, Reference{ID: text(refs[0], "id"), Version: int64(value(refs[0], "version").(float64)), DeletionEpoch: int64(value(refs[0], "deletionEpoch").(float64))})
	exposureMust(t, e)
	if current == nil {
		t.Fatal("actual proposal has no semantic authority")
	}
	resolved, e := s.ResolveSemantic(ctx, []semantic.Dirty{{SceneID: "s", Trigger: epObj("ref", refs[0], "dependencyKey", value(current, "dependencyKey")), Reason: "assertion_committed"}}, ticket)
	exposureMust(t, e)
	if resolved == nil {
		t.Fatal("no full source partition")
	}
	snapshot := semantic.CaptureInput("s", *resolved)
	if snapshot == nil {
		t.Fatal("incomplete captured partition")
	}
	started, e := s.BeginSemanticAttempt(ctx, snapshot)
	exposureMust(t, e)
	if !started {
		t.Fatal("new attempt not started")
	}
	result, e := s.commitSemantic(ctx, snapshot, []*Object{epObj("kind", "summary_candidate", "selectedParentIds", []any{text(refs[0], "id"), text(refs[1], "id")}, "text", "Attributed original declarations", "hypothesis", false)}, func(*assertions.RevisionTxn, *recovery.WriteSession) error { return nil })
	exposureMust(t, e)
	if result.Created != 1 {
		t.Fatalf("actual admission: %+v", result)
	}
	exposureMust(t, s.FinishSemanticAttempt(ctx, snapshot, "finished"))
	turn := exposureLiveTurn(t, s, f)
	port, e := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, e)
	runtimePolicy, e := modelread.NewRuntime(s.d.Assertions, f.events, f.main, f.recovery)
	exposureMust(t, e)
	exposure, e := NewSemanticExposure(port, runtimePolicy, func() int64 { return time.Now().UnixMilli() })
	exposureMust(t, e)
	values, e := exposure.Query(ctx, turn)
	exposureMust(t, e)
	if len(values) != 1 || len(epArr(epGet(value(values[0], "page"), "items"))) != 1 {
		t.Fatalf("missing persisted semantic item: %v", values)
	}
	if exposure.CaptureGuard(turn) != nil {
		t.Fatal("prepared receipt grants guard")
	}
	copied, e := exposure.Deliver(ctx, turn, []any{values[0].Clone()})
	exposureMust(t, e)
	if len(copied) != 0 {
		t.Fatal("copy accepted")
	}
	foreign := exposureLiveTurn(t, s, f)
	copied, e = exposure.Deliver(ctx, foreign, []any{values[0]})
	exposureMust(t, e)
	if len(copied) != 0 {
		t.Fatal("foreign turn accepted")
	}
	// Prepare A then B, but deliver B before A. This test-only guard probe
	// isolates the first-delivered leaf from their shared actual policy/page.
	sibling, e := exposure.Query(ctx, turn)
	exposureMust(t, e)
	if len(sibling) != 1 {
		t.Fatal("missing sibling B")
	}
	b, e := exposure.Deliver(ctx, turn, []any{sibling[0]})
	exposureMust(t, e)
	if len(b) != 1 || b[0] != sibling[0] {
		t.Fatal("B was not delivered first")
	}
	bGuard := exposure.CaptureGuard(turn)
	if bGuard == nil {
		t.Fatal("missing B leaf")
	}
	originalB := bGuard.Check
	var bAllowed atomic.Bool
	bAllowed.Store(true)
	bGuard.Check = func(reads botruntime.ReadAuthority) (bool, error) {
		if !bAllowed.Load() {
			return false, nil
		}
		return originalB(reads)
	}
	trace := []string{"prepare A", "prepare B", "deliver B"}
	got, e := exposure.Deliver(ctx, turn, []any{values[0]})
	exposureMust(t, e)
	if len(got) != 1 || got[0] != values[0] {
		t.Fatal("original identity not retained")
	}
	guard := exposure.CaptureGuard(turn)
	if guard == nil {
		t.Fatal("missing delivered guard")
	}
	trace = append(trace, "deliver A")
	if guard == bGuard {
		t.Fatal("A did not become latest tail")
	}
	checkTail := func(want bool) {
		t.Helper()
		exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, e := guard.Check(reads)
			if e == nil && valid != want {
				t.Fatalf("latest A tail valid=%v want=%v trace=%v", valid, want, trace)
			}
			return e
		}))
	}
	checkTail(true)
	bAllowed.Store(false)
	trace = append(trace, "deny B test probe")
	checkTail(false)
	bAllowed.Store(true)
	trace = append(trace, "restore B test probe")
	checkTail(true)
	t.Logf("test-only leaf composition trace: %v; actual writer policy revoke follows", trace)
	rollback := errors.New("rollback semantic answer change")
	e = f.recovery.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		changed := settings.Clone()
		_ = changed.Set("memorySemanticAnswerEnabled", false)
		raw, e := jsjson.Stringify(changed)
		if e != nil {
			return e
		}
		if _, e = ws.Exec("UPDATE config SET value=? WHERE key='settings'", raw); e != nil {
			return e
		}
		e = f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, e := guard.Check(reads)
			if e != nil {
				return e
			}
			if valid {
				t.Fatal("same writer uncommitted answer revoke ignored")
			}
			return nil
		})
		if e != nil {
			return e
		}
		return rollback
	})
	if !errors.Is(e, rollback) {
		t.Fatal(e)
	}
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		valid, e := guard.Check(reads)
		if e == nil && !valid {
			t.Fatal("rollback changed captured authority")
		}
		return e
	}))
}
