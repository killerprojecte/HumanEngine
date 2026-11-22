package memoryservice

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"

	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

func TestWorkingSetModelPageWitness(t *testing.T) {
	ref := workingset.Reference{Kind: "assertion", ID: "record", Version: 1}
	snapshot := epObj("ref", epObj("id", "record", "version", float64(1), "deletionEpoch", float64(0)),
		"record", epObj("id", "record", "scope", epObj("kind", "scene", "sceneId", "scene")))
	current := workingset.ViewItem{Ref: ref, DependencyKey: "key", PinRevision: 1, PinStatus: "current", PolicyCoverage: "complete",
		Pinned: true, PinIntent: true, State: "hot", MetadataRevision: 1}
	page := workingSetModelPage(workspaceCandidates{assertions: []any{snapshot}}, workingset.ViewResult{Enabled: true, Items: []workingset.ViewItem{current}})
	proofs, ok := workingSetProofs("scene", page)
	if !ok || len(proofs) != 1 || !deepEqual(proofs[0].body, snapshot) || !workingSetPolicyMatches(proofs[0].access, current) {
		t.Fatalf("model page did not retain the raw assertion and its exact policy")
	}
	if _, ok := workingSetProofs("foreign", page); ok {
		t.Fatal("foreign scene accepted")
	}
	current.State = "cold"
	current.ReviewDue = true
	current.ContextSelectedCount++
	current.MetadataRevision++
	if !workingSetPolicyMatches(proofs[0].access, current) {
		t.Fatal("classification or bookkeeping revoked policy")
	}
	current.PinRevision++
	if workingSetPolicyMatches(proofs[0].access, current) {
		t.Fatal("pin policy change did not revoke")
	}
	repeated := workingSetModelPage(workspaceCandidates{assertions: []any{snapshot, snapshot}}, workingset.ViewResult{Enabled: true, Items: []workingset.ViewItem{current}})
	if _, ok := workingSetProofs("scene", repeated); ok {
		t.Fatal("duplicate entity was silently deduplicated")
	}
	empty := workingSetModelPage(workspaceCandidates{}, workingset.ViewResult{Enabled: true, Coverage: "limited", OmittedCold: 1})
	if proofs, ok := workingSetProofs("scene", empty); !ok || len(proofs) != 0 || text(object(value(empty, "page")), "coverage") != "limited" {
		t.Fatal("all-cold enabled page lost its qualified empty state")
	}
}

func TestWorkingSetExposureRealBorrowedPinRevocation(t *testing.T) {
	ctx := t.Context()
	s, f, opts := outerSetup(t)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixMilli())
	ws, err := workingset.New(ctx, f.main, f.recovery, workingset.Ports{
		Enabled:        func() bool { return true },
		Now:            func() float64 { return float64(clock.Load()) },
		ResolveCurrent: ResolveWorkingSet(s.d.Commitments, f.store, s.d.CommitmentsEnabled, s.d.Authority),
		ResolveView:    ResolveWorkingSetView(s.d.Commitments, f.store, s.d.CommitmentsEnabled, s.d.Authority),
	})
	exposureMust(t, err)
	s.d.WorkingSet = ws
	in, candidate := outerInput(t, "working-set-exposure")
	result, err := s.Propose(ctx, in, candidate, opts)
	requireOuterSaved(t, result, err)
	turn := exposureLiveTurn(t, s, f)
	settings, err := jsjson.Parse(string(contract.DefaultSettingsInput()))
	exposureMust(t, err)
	_ = settings.(*Object).Set(workingSetFeature, true)
	encoded, err := jsjson.Stringify(settings)
	exposureMust(t, err)
	exposureWrite(t, f, "INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", encoded)
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	tracker, err := NewWorkspaceSelectionTracker(s, func() bool { return true }, nil)
	exposureMust(t, err)
	exposure, err := NewWorkingSetExposure(port, tracker)
	exposureMust(t, err)
	values, err := exposure.Query(ctx, turn, WorkingSetToolQuery{IncludeCold: true})
	exposureMust(t, err)
	if len(values) != 1 {
		t.Fatalf("no real working-set witness, got %d", len(values))
	}
	proofs, ok := workingSetProofs("s", values[0])
	if !ok || len(proofs) != 1 || proofs[0].selection.Ref.ID != text(result.Memory, "id") {
		t.Fatal("wrong working-set backing assertion")
	}
	got, err := exposure.Deliver(ctx, turn, []any{values[0].Clone()})
	exposureMust(t, err)
	if len(got) != 0 {
		t.Fatal("unissued working-set copy was delivered")
	}
	got, err = exposure.Deliver(ctx, turn, []any{values[0]})
	exposureMust(t, err)
	guard := exposure.CaptureGuard(turn)
	if len(got) != 1 || guard == nil {
		t.Fatal("issued working-set page was not delivered")
	}
	check := func() bool {
		var valid bool
		exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			var err error
			valid, err = guard.Check(reads)
			return err
		}))
		return valid
	}
	if !check() {
		t.Fatal("real borrowed runtime guard invalid before mutation")
	}
	if text(proofs[0].access, "state") != "hot" {
		t.Fatal("expected working-set page hot before clock advance")
	}
	clock.Add(int64(31 * 86400000))
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return f.events.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			view, err := ws.ViewRuntime(scope, workingset.Context{SceneID: "s"}, []workingset.Selection{proofs[0].selection}, true)
			if err == nil && (len(view.Items) != 1 || view.Items[0].State != "cold") {
				t.Fatal("expected only classification to become cold")
			}
			return err
		})
	}))
	if !check() {
		t.Fatal("clock-only transition revoked exact policy/body witness")
	}
	changed := exposureClone(values[0]).(*Object)
	changedItem := object(workingSetItems(changed)[0])
	_ = object(value(object(value(changedItem, "snapshot")), "record")).Set("content", "different synthetic body")
	if ok, err := (workingSetExposurePort{port}).Valid(ctx, Context{SceneID: "s"}, []*Object{changed}); err != nil || ok {
		t.Fatalf("different raw body validated: %v, %v", ok, err)
	}
	_, err = s.d.WorkingSet.SetPinned(ctx, workingset.Context{SceneID: "s"}, proofs[0].selection, 0, true)
	exposureMust(t, err)
	if check() {
		t.Fatal("same assertion with a replaced pin policy was accepted")
	}
}
