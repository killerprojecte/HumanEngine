package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Use a short synthetic plugin ID: actual source canonical proof IDs include
// SDK scene/message IDs and the Commitment schema caps proofId at 512.
// The prior runtime-fixture ID produces 518 and is rejected by TS too.
func TestInstalledContextualLearningModelActions(t *testing.T) {
	options := applicationOptions(t)
	fixture, err := os.ReadFile("testdata/contextual-application.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(options.PluginRoot, "application.mjs"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	if err = os.WriteFile(resume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}
	type observation struct {
		mode          string
		status        string
		reason        string
		statsAccepted uint64
	}
	observations := make(chan observation, 8)
	mode := "ordinary"
	pendingEntered, pendingRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releasePending := func() { releaseOnce.Do(func() { close(pendingRelease) }) }
	t.Cleanup(releasePending)
	app.Engine.Runtime.AddExtension(&botruntime.Extension{ContextSelected: func(c *botruntime.TurnContext, brief *jsjson.Object) error {
		receipts, _ := field(brief, "actionReceipts").([]any)
		if len(receipts) != 1 {
			return errors.New("contextual_action_receipt_missing")
		}
		receipt := receipts[0]
		if field(receipt, "toolName") != "learning.propose" {
			return errors.New("wrong_contextual_action")
		}
		value := receipt
		status, _ := field(value, "status").(string)
		reason, _ := field(value, "reason").(string)
		observations <- observation{mode, status, reason, app.Contextual.Stats().Accepted}
		if mode == "ordinary" && status == "queued" && app.Contextual.Stats().Completed != 0 {
			return errors.New("queued_was_claimed_saved_during_foreground")
		}
		if mode == "revoked" {
			close(pendingEntered)
			<-pendingRelease
		}
		return nil
	}})
	for index, want := range []struct{ mode, status, reason string }{{"ordinary", "queued", ""}, {"history", "rejected", "historical_read_not_learning_authority"}, {"identity", "rejected", "identity_read_not_learning_authority"}, {"current", "rejected", "commitment_state_not_freeform_learning_authority"}} {
		mode = want.mode
		appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/mode", map[string]any{"mode": mode})
		appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
		select {
		case got := <-observations:
			if got.mode != want.mode || got.status != want.status || got.reason != want.reason {
				t.Fatalf("contextual action receipt: %+v want %+v", got, want)
			}
		case failure := <-turnFailures:
			t.Fatal(failure)
		case <-ctx.Done():
			t.Fatal("contextual action missing", ctx.Err())
		}
		for app.Engine.Runtime.ActiveTasks() != 0 {
			select {
			case failure := <-turnFailures:
				t.Fatal(failure)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
		if err = app.Contextual.WhenIdle(ctx); err != nil {
			t.Fatal(err)
		}
		stats := app.Contextual.Stats()
		if stats.Accepted != 1 || stats.Completed != 1 || stats.Failed != 0 {
			t.Fatalf("real contextual queue after %s: %+v", mode, stats)
		}
		inspected := appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
		if len(field(inspected, "sent").([]any)) != index+1 {
			t.Fatal("native contextual reply missing", inspected)
		}
	}
	mode = "revoked"
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/mode", map[string]any{"mode": mode})
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	select {
	case <-pendingEntered:
	case failure := <-turnFailures:
		t.Fatal(failure)
	case <-ctx.Done():
		t.Fatal("pending contextual barrier missing")
	}
	select {
	case got := <-observations:
		if got.status != "queued" {
			t.Fatalf("pending submission: %+v", got)
		}
	default:
		t.Fatal("pending queued receipt missing")
	}
	if stats := app.Contextual.Stats(); stats.Accepted != 2 || stats.Completed != 1 || stats.Queued != 1 {
		t.Fatalf("queued is not yet committed: %+v", stats)
	}
	savedScene.Set("learningEnabled", false)
	pausedBody, err = jsjson.Marshal(savedScene)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(pausedBody))
	releasePending()
	for app.Engine.Runtime.ActiveTasks() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("learning-paused foreground did not drain")
		case <-time.After(time.Millisecond):
		}
	}
	if err = app.Contextual.WhenIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := app.Contextual.Stats(); stats.Accepted != 2 || stats.Completed != 1 || stats.Cancelled != 1 || stats.Queued != 0 || stats.Running != 0 {
		t.Fatalf("revoked contextual job: %+v", stats)
	}
	inspected := appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	// Source sceneChanged cancels learning only; participation is still enabled.
	// The queued candidate must be cancelled without suppressing the authorized reply.
	if len(field(inspected, "sent").([]any)) != 5 {
		t.Fatal("learning-only revocation incorrectly suppressed reply", inspected)
	}
	witness, err := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = witness.QueryRowContext(ctx, "SELECT count(*) FROM memories WHERE content=?", "Contextual synthetic ordinary preference.").Scan(&count)
	var revokedCount int
	if err == nil {
		err = witness.QueryRowContext(ctx, "SELECT count(*) FROM memories WHERE content=?", "Contextual synthetic revoked preference.").Scan(&revokedCount)
	}
	if revokedCount != 0 {
		t.Fatal("revoked queued learning was stored")
	}
	closeErr := witness.Close()
	if err != nil || closeErr != nil || count != 1 {
		t.Fatalf("genuine proposal commit: %d %v %v", count, err, closeErr)
	}
}
