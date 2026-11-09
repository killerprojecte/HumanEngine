package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
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
func TestInstalledCommitmentWriteSingleConnection(t *testing.T) {
	options := applicationOptions(t)
	fixture, err := os.ReadFile("testdata/application.mjs")
	if err != nil {
		t.Fatal(err)
	}
	fixture = []byte(strings.ReplaceAll(string(fixture), "runtime-fixture", "state"))
	if err = os.WriteFile(filepath.Join(options.PluginRoot, "application.mjs"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	candidate.Set("commitment", memoryToolObject("kind", "propose", "terms", memoryToolObject("kind", "plan", "title", "Synthetic plan", "participantIds", []any{"synthetic-user"}, "temporalScope", "future", "polarity", "positive", "modality", "asserted", "conditions", []any{})))
	savedMemory, err := app.Semantic.Memory.Propose(ctx, proposal, candidate, memoryservice.ProposalOptions{})
	if err != nil || savedMemory.Status != "saved" {
		t.Fatalf("real memory seed: %+v %v", savedMemory, err)
	}
	if savedMemory.Commitment == nil || savedMemory.Commitment.Status != "saved" {
		t.Fatalf("real commitment seed: %+v", savedMemory.Commitment)
	}
}
