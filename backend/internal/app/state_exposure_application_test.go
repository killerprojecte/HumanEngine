package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Use a short synthetic plugin ID: actual source canonical proof IDs include
// SDK scene/message IDs and the Commitment schema caps proofId at 512.
// The prior runtime-fixture ID produces 518 and is rejected by TS too.
func TestInstalledApplicationStateExposureReceiptsAndRestart(t *testing.T) {
	options := applicationOptions(t)
	fixture, err := os.ReadFile("testdata/state-exposure-application.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(options.PluginRoot, "application.mjs"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	app, err := Open(ctx, options)
	if err != nil {
		if app != nil {
			app.Shutdown(context.Background())
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if app != nil {
			if err := app.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	bootstrapWitness, err := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_relation_candidates", "memory_relation_entities", "memory_relation_tombstones", "memory_commitment_heads", "memory_commitment_revisions", "memory_commitment_commands"} {
		var count int
		if err = bootstrapWitness.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			bootstrapWitness.Close()
			t.Fatalf("real owner schema %s: %d %v", table, count, err)
		}
	}
	if err = bootstrapWitness.Close(); err != nil {
		t.Fatal(err)
	}
	turnFailures := make(chan error, 8)
	app.Engine.Runtime.Observe(func(e botruntime.Event) {
		app.observeRuntime(e)
		if e.Cause != nil {
			select {
			case turnFailures <- e.Cause:
			default:
			}
		}
	})
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("registration left idle process")
	}
	settings := appRequest(t, app, http.MethodGet, "/api/settings", nil)
	raw, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	input["sourceEventRevisionsEnabled"] = true
	input["memoryHistoricalReadEnabled"] = true
	input["memoryCommitmentsEnabled"] = true
	input["memoryCommitmentsAnswerEnabled"] = true
	input["debugModelIO"] = true
	// Source promptWindowAuthority separately invalidates a live turn on model
	// writes. Disable that source option here to isolate Router snapshot lifetime.
	input["promptContextWindowEnabled"] = false
	input["debounceMs"] = float64(0)
	input["groupTurnCooldownMs"] = float64(0)
	appRequest(t, app, http.MethodPut, "/api/settings", input)
	models := json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"state:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`)
	appRequest(t, app, http.MethodPut, "/api/models", models)
	marker := filepath.Join(options.PluginRoot, "received.marker")
	arrived, resume := filepath.Join(options.PluginRoot, "planner.arrived"), filepath.Join(options.PluginRoot, "planner.resume")
	t.Cleanup(func() { _ = os.WriteFile(resume, []byte("resume"), 0600) })
	appRequest(t, app, http.MethodPut, "/api/plugins/state/config", map[string]any{"memoryMode": true, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "receivedMarker": marker, "plannerArrived": arrived, "plannerResume": resume})
	appRequest(t, app, http.MethodPut, "/api/plugins/state/enabled", map[string]any{"enabled": true})
	// Source ingress defaults to participation disabled. Enable the discovered
	// scene through the actual administration boundary before new platform input.
	inspectedScene := appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	sceneID := field(field(inspectedScene, "scene"), "id").(string)
	scenes := appRequest(t, app, http.MethodGet, "/api/scenes", nil).([]any)
	var savedScene *jsjson.Object
	for _, v := range scenes {
		if field(v, "id") == sceneID {
			savedScene = v.(*jsjson.Object)
			break
		}
	}
	if savedScene == nil {
		t.Fatal("adapter did not persist discovered scene")
	}
	// The source default learning=false must be a successful real HTTP path.
	// Admit an actual task that ignores cancellation until its cleanup is released;
	// scene pause cancels its signal but must not pretend physical completion.
	mediaEntered, mediaCancelled, mediaRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mediaReleaseOnce sync.Once
	releaseMedia := func() { mediaReleaseOnce.Do(func() { close(mediaRelease) }) }
	t.Cleanup(releaseMedia)
	mediaTask, err := app.MediaTasks.Accept(sceneID, "synthetic-scene-pause", func(job context.Context) error {
		close(mediaEntered)
		<-job.Done()
		close(mediaCancelled)
		<-mediaRelease
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-mediaEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	savedScene.Set("learningEnabled", false)
	pausedBody, err := jsjson.Marshal(savedScene)
	if err != nil {
		t.Fatal(err)
	}
	paused := appRequest(t, app, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(pausedBody))
	if field(paused, "learningEnabled") != false {
		t.Fatalf("pause response: %#v", paused)
	}
	select {
	case <-mediaCancelled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-mediaTask.Done():
		t.Fatal("scene cancellation falsely completed physical media task")
	default:
	}
	releaseMedia()
	if err = mediaTask.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	savedScene.Set("participationEnabled", true)
	savedScene.Set("learningEnabled", true)
	sceneBody, err := jsjson.Marshal(savedScene)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(sceneBody))
	var evidence []any
	initialSource := field(inspectedScene, "source").(string)
	initialSnapshot, err := app.Engine.Runtime.Snapshot(sceneID)
	if err != nil {
		t.Fatal(err)
	}
	err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		message, e := app.Engine.Runtime.PreviewMessage(sceneID, initialSource, reads)
		if e != nil {
			return e
		}
		evidence, e = sourceadmission.TrustedEvidence([]*jsjson.Object{message}, func(scene, id string) (bool, error) {
			return reads.IsRecalled(initialSnapshot.Scene, id)
		})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := memoryToolObject("sceneId", sceneID, "simulation", false, "provenance", "source-only", "sourceEvidence", evidence)
	candidate := memoryToolObject("content", "Prefers concise replies.", "kind", "fact", "epistemicKind", "explicit", "sources", []any{memoryToolObject("sceneId", sceneID, "messageId", initialSource, "senderId", "synthetic-user")}, "subject", memoryToolObject("kind", "self", "sourceMessageId", initialSource))
	// A commitment proposal intentionally stores an internal declaration. Seed
	// ordinary person memory separately through the real outer proposal API.
	ordinary, err := app.Semantic.Memory.Propose(ctx, proposal, candidate.Clone(), memoryservice.ProposalOptions{})
	if err != nil || ordinary.Status != "saved" {
		t.Fatalf("ordinary memory seed: %+v %v", ordinary, err)
	}
	if field(ordinary.Memory, "internalUse") != nil {
		t.Fatal("ordinary seed became a declaration")
	}
	candidate.Set("commitment", memoryToolObject("kind", "propose", "terms", memoryToolObject("kind", "plan", "title", "Synthetic plan", "participantIds", []any{"synthetic-user"}, "temporalScope", "future", "polarity", "positive", "modality", "asserted", "conditions", []any{})))
	savedMemory, err := app.Semantic.Memory.Propose(ctx, proposal, candidate, memoryservice.ProposalOptions{})
	if err != nil || savedMemory.Status != "saved" {
		t.Fatalf("real memory seed: %+v %v", savedMemory, err)
	}
	if savedMemory.Commitment == nil || savedMemory.Commitment.Status != "saved" {
		t.Fatalf("real commitment seed: %+v", savedMemory.Commitment)
	}
	selectedMemory := make(chan int, 4)
	selectedTurns := make(chan *botruntime.TurnContext, 4)
	app.Engine.Runtime.AddExtension(&botruntime.Extension{ContextSelected: func(c *botruntime.TurnContext, _ *jsjson.Object) error {
		if !app.IdentityExposure.HasExposure(c) {
			return errors.New("identity_receipt_not_delivered")
		}
		if err := checkApplicationStateReceipts(app, ctx, c); err != nil {
			return err
		}
		selectedTurns <- c
		selectedMemory <- len(app.MemoryExposure.ReadExposed(c))
		return nil
	}})
	experienceView := appRequest(t, app, http.MethodGet, "/api/memory-experience?sceneId="+url.QueryEscape(sceneID), nil)
	if field(experienceView, "sceneId") != sceneID {
		t.Fatal("Experience response bound to wrong scene", experienceView)
	}
	if _, ok := field(experienceView, "items").([]any); !ok {
		t.Fatal("missing actual Experience items", experienceView)
	}

	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	for {
		if _, e := os.Stat(arrived); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("planner did not enter captured router")
		case <-time.After(time.Millisecond):
		}
	}
	// HTTP commit happens while the existing turn owns its original router.
	updated := json.RawMessage(strings.ReplaceAll(string(models), `"m"`, `"m2"`))
	appRequest(t, app, http.MethodPut, "/api/models", updated)
	if err = os.WriteFile(resume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}

	var inspected any
	for {
		inspected = appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
		sent, _ := field(inspected, "sent").([]any)
		if len(sent) == 1 {
			break
		}
		select {
		case failure := <-turnFailures:
			t.Fatalf("synthetic actual turn failure: %+v; inspect=%v", failure, inspected)
		case <-ctx.Done():
			t.Fatal("native send not completed", inspected)
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case count := <-selectedMemory:
		if count < 1 {
			t.Fatal("memory tool did not deliver selected original record")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, name := range []string{"memory.commitments", "memory.commitmentHistory", "memory.commitmentTimeline", "memory.history"} {
		found := false
		for _, actual := range field(inspected, "modelTools").([]any) {
			if actual == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("model did not call %s", name)
		}
	}
	if field(inspected, "modelCalls").(float64) < 3 {
		t.Fatal("did not run native planner/replyer/tool", inspected)
	}
	for _, id := range field(inspected, "modelIDs").([]any) {
		if id != "m" {
			t.Fatal("midturn PUT replaced captured router", inspected)
		}
	}
	for app.Engine.Runtime.ActiveTasks() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("first turn did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	for {
		inspected = appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
		if len(field(inspected, "sent").([]any)) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("new turn did not send", inspected)
		case <-time.After(time.Millisecond):
		}
	}
	selections := field(inspected, "planSelections").([]any)
	if len(selections) != 2 {
		t.Fatalf("expected two real selected plans: %v", selections)
	}
	expectedSelections := [][]string{{"memory.identities", "memory.commitments", "memory.commitmentHistory", "memory.commitmentTimeline"}, {"memory.commitments", "memory.commitmentHistory", "memory.commitmentTimeline", "memory.history"}}
	for i, want := range expectedSelections {
		got := selections[i].([]any)
		if len(got) != len(want) {
			t.Fatalf("selection count %v", got)
		}
		for j, name := range want {
			if got[j] != name {
				t.Fatalf("model plan selection %d: %v", i, got)
			}
		}
	}
	ids := field(inspected, "modelIDs").([]any)
	if ids[len(ids)-1] != "m2" {
		t.Fatal("new turn failed to adopt committed models", inspected)
	}
	for app.Engine.Runtime.ActiveTasks() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	turn := <-selectedTurns
	guard := app.StateExposures.current.CaptureGuard(turn)
	if guard == nil {
		t.Fatal("missing current guard")
	}
	// A committed real HTTP settings change must invalidate cached state.
	err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, e := guard.Check(reads)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("current_guard_initially_invalid")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reach the actual native adapter send boundary with all receipts already
	// selected, then withdraw answer permission before its beforeTransmit check.
	beforeArrived, beforeResume := filepath.Join(options.PluginRoot, "state-send.arrived"), filepath.Join(options.PluginRoot, "state-send.resume")
	t.Cleanup(func() { _ = os.WriteFile(beforeResume, []byte("resume"), 0600) })
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/hold-send", map[string]any{"arrived": beforeArrived, "resume": beforeResume})
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	for {
		if _, e := os.Stat(beforeArrived); e == nil {
			break
		}
		select {
		case failure := <-turnFailures:
			t.Fatalf("pending turn failed before send boundary: %v", failure)
		case <-ctx.Done():
			t.Fatal("pending send did not reach boundary")
		case <-time.After(time.Millisecond):
		}
	}
	pending := appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	if len(field(pending, "sent").([]any)) != 2 || field(pending, "sendAttempts") != float64(3) {
		t.Fatal("pending send was not physically held", pending)
	}
	input["memoryCommitmentsAnswerEnabled"] = false
	appRequest(t, app, http.MethodPut, "/api/settings", input)
	if err = os.WriteFile(beforeResume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}
	for app.Engine.Runtime.ActiveTasks() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("revoked turn did not physically finish")
		case <-time.After(time.Millisecond):
		}
	}
	revoked := appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	if len(field(revoked, "sent").([]any)) != 2 || field(revoked, "sendAttempts") != float64(3) {
		t.Fatal("revoked pending native send transmitted", revoked)
	}

	err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, e := guard.Check(reads)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("revoked_current_guard_passed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := appRequest(t, app, http.MethodGet, "/api/model-calls", nil)
	// Query exposes actual metadata rows from the newly PUT model configuration.
	encoded, _ := jsjson.Marshal(calls)
	if !strings.Contains(string(encoded), `"modelId":"m"`) {
		t.Fatal("model calls not bound", string(encoded))
	}
	appRequest(t, app, http.MethodGet, "/api/diagnostics", nil)
	sourceID, _ := field(inspected, "source").(string)
	if sourceID == "" {
		t.Fatal("missing actual source ID")
	}
	if err = app.Engine.Runtime.Recall(ctx, sceneID, sourceID); err != nil {
		t.Fatal(err)
	}
	// Independent read of the committed native tombstone proves the Experience
	// callback joined the real source recall writer instead of a no-op callback.
	witness, err := sql.Open("sqlite3", options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	var tombstones int
	err = witness.QueryRowContext(ctx, "SELECT count(*) FROM memory_experience_tombstones WHERE key=?", experienceservice.SourceKey(sceneID, sourceID)).Scan(&tombstones)
	closeErr := witness.Close()
	if err != nil || closeErr != nil || tombstones != 1 {
		t.Fatalf("Experience recall not committed: %d %v %v", tombstones, err, closeErr)
	}

	historicalGuard := app.StateExposures.historical.CaptureGuard(turn)
	if historicalGuard == nil {
		t.Fatal("missing historical guard")
	}
	if err = app.Engine.Runtime.Recall(ctx, sceneID, initialSource); err != nil {
		t.Fatal(err)
	}
	err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, e := historicalGuard.Check(reads)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("recalled_historical_guard_passed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("plugin process survived shutdown")
	}
	app = nil
	// Source adapter deliberately has no fresh platform message on its next start.
	// A replay here could only come from application/learning startup reading DB.
	app, err = Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	inspected = appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	if field(inspected, "modelCalls") != float64(0) || len(field(inspected, "sent").([]any)) != 0 {
		t.Fatal("startup replayed persisted message", inspected)
	}
	appRequest(t, app, http.MethodPut, "/api/plugins/state/enabled", map[string]any{"enabled": false})
	if err = app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
