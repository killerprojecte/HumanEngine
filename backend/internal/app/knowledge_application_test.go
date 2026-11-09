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
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Use a short synthetic plugin ID: actual source canonical proof IDs include
// SDK scene/message IDs and the Commitment schema caps proofId at 512.
// The prior runtime-fixture ID produces 518 and is rejected by TS too.
func TestInstalledKnowledgeLearningModelActions(t *testing.T) {
	options := applicationOptions(t)
	fixture, err := os.ReadFile("testdata/knowledge-application.mjs")
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
	for _, table := range []string{"memory_commitment_heads", "memory_commitment_revisions", "memory_commitment_commands"} {
		var count int
		if err = bootstrapWitness.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			bootstrapWitness.Close()
			t.Fatalf("real owner schema %s: %d %v", table, count, err)
		}
	}
	var lazyKnowledgeTables int
	if err = bootstrapWitness.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_knowledge_edges'").Scan(&lazyKnowledgeTables); err != nil || lazyKnowledgeTables != 0 {
		bootstrapWitness.Close()
		t.Fatalf("knowledge source initializes only on first write: %d %v", lazyKnowledgeTables, err)
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
	input["memoryKnowledgeEnabled"] = true
	input["memoryKnowledgeAnswerEnabled"] = true
	input["memoryKnowledgeAutoAdmissionEnabled"] = true
	input["memoryGraphEnabled"] = true
	input["memoryGraphAnswerEnabled"] = true
	input["memoryGraphAutoAdmissionEnabled"] = true
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
	candidate.Set("knowledge", []any{memoryToolObject("registryVersion", float64(1), "predicate", "prefers_topic", "subject", memoryToolObject("kind", "self", "personId", "synthetic-user"), "object", memoryToolObject("kind", "new", "entityKind", "topic", "title", "concise replies"), "polarity", "positive", "modality", "asserted", "temporalScope", "current", "conditions", []any{}, "sourceSpans", []any{memoryToolObject("messageId", initialSource, "partIndex", float64(0), "start", float64(9), "end", float64(24), "stance", "self")})})
	savedMemory, err := app.Semantic.Memory.Propose(ctx, proposal, candidate, memoryservice.ProposalOptions{KnowledgeEnabled: true, KnowledgeAutoAdmission: true})
	if err != nil || savedMemory.Status != "saved" || savedMemory.Knowledge == nil || len(savedMemory.Knowledge.Created) != 1 {
		t.Fatalf("actual knowledge seed: %+v %v", savedMemory, err)
	}
	seedWitness, err := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var seedRows int
	err = seedWitness.QueryRowContext(ctx, "SELECT count(*) FROM memory_knowledge_edges").Scan(&seedRows)
	seedCloseErr := seedWitness.Close()
	if err != nil || seedCloseErr != nil || seedRows != 1 {
		t.Fatalf("first real proposal must initialize and persist knowledge edge: %d %v %v", seedRows, err, seedCloseErr)
	}
	seededTarget := field(field(field(savedMemory.Knowledge.Created[0], "object"), "entity"), "id").(string)

	if err = os.WriteFile(resume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}
	seedSnapshot := knowledgeSQLSnapshot(t, ctx, options.Installation.Database)
	var ordinarySnapshot map[string]string
	type observation struct {
		mode           string
		status         string
		reason         string
		statsAccepted  uint64
		restoredSource string
	}
	observations := make(chan observation, 8)
	mode := "ordinary"
	pendingEntered, pendingRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releasePending := func() { releaseOnce.Do(func() { close(pendingRelease) }) }
	t.Cleanup(releasePending)
	app.Engine.Runtime.AddExtension(&botruntime.Extension{ContextSelected: func(c *botruntime.TurnContext, brief *jsjson.Object) error {
		entities, err := app.KnowledgeExposure.ReadEntities(ctx, c)
		if err != nil {
			return err
		}
		if len(entities) != 1 || field(entities[0], "entityId") != seededTarget {
			return errors.New("actual_knowledge_entity_not_exposed")
		}
		if app.KnowledgeExposure.CaptureGuard(c) == nil {
			return errors.New("actual_knowledge_guard_missing")
		}
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
		restoredTargets, ok := field(field(brief, "plan"), "targetMessageIds").([]any)
		if !ok || len(restoredTargets) == 0 {
			return errors.New("restored_plan_target_missing")
		}
		restoredSource, ok := restoredTargets[0].(string)
		if !ok {
			return errors.New("restored_plan_target_invalid")
		}
		available := false
		for _, original := range c.Sources() {
			if field(original, "id") == restoredSource {
				available = true
			}
		}
		if !available {
			return errors.New("restored_plan_target_not_original_source")
		}
		observations <- observation{mode, status, reason, app.Contextual.Stats().Accepted, restoredSource}
		if mode == "ordinary" && status == "queued" && app.Contextual.Stats().Completed != 0 {
			return errors.New("queued_was_claimed_saved_during_foreground")
		}
		if mode == "revoked" {
			close(pendingEntered)
			<-pendingRelease
		}
		return nil
	}})
	ordinarySource := ""
	for index, want := range []struct{ mode, status, reason string }{{"ordinary", "queued", ""}} {
		mode = want.mode
		appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/mode", map[string]any{"mode": mode})
		appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
		select {
		case got := <-observations:
			ordinarySource = got.restoredSource
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
		choices := field(inspected, "planSources").([]any)
		if len(choices) != index+1 {
			t.Fatal("actual model source observation missing", inspected)
		}
		chosen := choices[index]
		// Model IDs are per-turn aliases. The original was captured from the
		// native restored brief before foreground work completed.
		t.Logf("actual model source selection: %v; native restored original: %s", chosen, ordinarySource)
		ordinarySnapshot = knowledgeSQLSnapshot(t, ctx, options.Installation.Database)
		// Check both directions: comparing only surviving after-keys would
		// miss deletion of an original memory or its original backing edge.
		for key, before := range seedSnapshot {
			after, exists := ordinarySnapshot[key]
			if !exists || after != before {
				t.Fatalf("original seed row removed or changed: %s (exists=%v)", key, exists)
			}
		}
		var newMemory, newEdge *jsjson.Object
		memoryAdds, edgeAdds := 0, 0
		for key, raw := range ordinarySnapshot {
			if old, ok := seedSnapshot[key]; ok {
				if old != raw {
					t.Fatalf("seed state changed unexpectedly %s", key)
				}
				continue
			}
			v, parseErr := jsjson.Parse(raw)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if strings.HasPrefix(key, "memories:") {
				memoryAdds++
				newMemory = v.(*jsjson.Object)
			} else {
				edgeAdds++
				newEdge = v.(*jsjson.Object)
			}
		}
		if memoryAdds != 1 || edgeAdds != 1 {
			t.Fatalf("actual typed commit delta memories=%d edges=%d", memoryAdds, edgeAdds)
		}
		content, _ := field(newMemory, "content").(string)
		if field(newMemory, "kind") != "claim" || field(newMemory, "internalUse") != "knowledge_declaration" || !regexp.MustCompile(`^Model-extracted structured knowledge declaration; reference [0-9a-f]{64}\. Not independently verified\.$`).MatchString(content) {
			t.Fatal("typed declaration boundary", newMemory)
		}
		if field(field(newEdge, "backing"), "id") != field(newMemory, "id") || field(field(field(newEdge, "object"), "entity"), "id") != seededTarget {
			t.Fatal("new edge not bound to actual saved declaration and existing target", newEdge, newMemory)
		}
		foundSource := false
		for _, source := range field(newMemory, "sources").([]any) {
			if field(source, "messageId") == ordinarySource {
				foundSource = true
			}
		}
		if !foundSource {
			t.Fatal("declaration lacks immutable model-selected original source", ordinarySource, newMemory)
		}

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
	if len(field(inspected, "sent").([]any)) != 2 {
		t.Fatal("learning-only revocation incorrectly suppressed reply", inspected)
	}
	revokedSnapshot := knowledgeSQLSnapshot(t, ctx, options.Installation.Database)
	if !reflect.DeepEqual(ordinarySnapshot, revokedSnapshot) {
		t.Fatal("learning-revoked turn changed complete memory/edge state", ordinarySnapshot, revokedSnapshot)
	}
	t.Logf("learning revoke actual model choices: %v", field(inspected, "planSources"))
	// Revoke only the knowledge answer flag at the physical beforeTransmit
	// boundary. A planned reply must not send using the revoked graph receipt.
	savedScene.Set("learningEnabled", true)
	sceneBody, err = jsjson.Marshal(savedScene)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(sceneBody))
	mode = "pending-send"
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/mode", map[string]any{"mode": mode})
	sendArrived, sendResume := filepath.Join(options.PluginRoot, "knowledge-send.arrived"), filepath.Join(options.PluginRoot, "knowledge-send.resume")
	t.Cleanup(func() { _ = os.WriteFile(sendResume, []byte("resume"), 0600) })
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/hold-send", map[string]any{"arrived": sendArrived, "resume": sendResume})
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	for {
		if _, err = os.Stat(sendArrived); err == nil {
			break
		}
		select {
		case failure := <-turnFailures:
			t.Fatal(failure)
		case <-ctx.Done():
			t.Fatal("knowledge pending send boundary", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case observed := <-observations:
		if observed.mode != "pending-send" || observed.status != "queued" || observed.restoredSource == "" {
			t.Fatalf("pending-send action not actually queued with restored source: %+v", observed)
		}
		t.Logf("pending-send action before revoke: %+v", observed)
	default:
		t.Fatal("pending-send restored action observation missing")
	}
	if stats := app.Contextual.Stats(); stats.Accepted != 3 || stats.Completed != 1 || stats.Queued != 1 || stats.Running != 0 {
		t.Fatalf("pending-send physical foreground did not retain queued action: %+v", stats)
	}
	input["memoryKnowledgeAnswerEnabled"] = false
	appRequest(t, app, http.MethodPut, "/api/settings", input)
	if err = os.WriteFile(sendResume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}
	for app.Engine.Runtime.ActiveTasks() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("knowledge pending send did not physically drain", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err = app.Contextual.WhenIdle(ctx); err != nil {
		t.Fatal(err)
	}
	inspected = appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	if len(field(inspected, "sent").([]any)) != 2 || field(inspected, "sendAttempts") != float64(3) {
		t.Fatal("revoked graph was sent or physical boundary was not exercised", inspected)
	}
	pendingSnapshot := knowledgeSQLSnapshot(t, ctx, options.Installation.Database)
	if !reflect.DeepEqual(ordinarySnapshot, pendingSnapshot) {
		t.Fatal("knowledge-revoked pending send changed complete memory/edge state", ordinarySnapshot, pendingSnapshot)
	}
	t.Logf("pending-send revoke actual model choices: %v", field(inspected, "planSources"))
}

func knowledgeSQLSnapshot(t *testing.T, ctx context.Context, database string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite3", database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]string{}
	for _, table := range []string{"memories", "memory_knowledge_edges"} {
		statement := "SELECT id,value FROM " + table + " ORDER BY id"
		rows, err := db.QueryContext(ctx, statement)
		if err != nil {
			t.Fatal(statement, err)
		}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table+":"+id] = raw
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
		t.Logf("SQL witness statement: %s", statement)
	}
	encoded, _ := json.Marshal(out)
	t.Logf("SQL witness complete immutable rows: %s", encoded)
	return out
}
