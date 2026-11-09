package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
)

func TestInstalledSemanticModelReceiptSendAndRevocation(t *testing.T) {
	options := applicationOptions(t)
	fixture, e := os.ReadFile("testdata/semantic-application.mjs")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(options.PluginRoot, "application.mjs"), fixture, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	app, e := Open(ctx, options)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if app != nil {
			if e := app.Shutdown(context.Background()); e != nil {
				t.Error(e)
			}
		}
	})
	settings := appRequest(t, app, http.MethodGet, "/api/settings", nil)
	raw, e := jsjson.Marshal(settings)
	if e != nil {
		t.Fatal(e)
	}
	var input map[string]any
	if e = json.Unmarshal(raw, &input); e != nil {
		t.Fatal(e)
	}
	for _, k := range []string{"sourceEventRevisionsEnabled", "memorySemanticEnabled", "memorySemanticAnswerEnabled", "memorySemanticAutoAdmissionEnabled"} {
		input[k] = true
	}
	input["promptContextWindowEnabled"] = false
	input["debounceMs"] = 0
	input["groupTurnCooldownMs"] = 0
	appRequest(t, app, http.MethodPut, "/api/settings", input)
	appRequest(t, app, http.MethodPut, "/api/models", json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"state:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`))
	arrived, resume := filepath.Join(options.PluginRoot, "planner.arrived"), filepath.Join(options.PluginRoot, "planner.resume")
	t.Cleanup(func() { _ = os.WriteFile(resume, []byte("resume"), 0600) })
	appRequest(t, app, http.MethodPut, "/api/plugins/state/config", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "plannerArrived": arrived, "plannerResume": resume})
	appRequest(t, app, http.MethodPut, "/api/plugins/state/enabled", map[string]any{"enabled": true})
	inspect := func() any {
		return appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/inspect", map[string]any{})
	}
	first := inspect()
	sceneID := field(field(first, "scene"), "id").(string)
	sourceID := field(first, "source").(string)
	var scene *jsjson.Object
	for _, v := range appRequest(t, app, http.MethodGet, "/api/scenes", nil).([]any) {
		if field(v, "id") == sceneID {
			scene = v.(*jsjson.Object)
		}
	}
	if scene == nil {
		t.Fatal("missing scene")
	}
	_ = scene.Set("participationEnabled", true)
	_ = scene.Set("learningEnabled", true)
	body, e := jsjson.Marshal(scene)
	if e != nil {
		t.Fatal(e)
	}
	appRequest(t, app, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(body))
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	wait := func(label string, done func() bool) {
		t.Helper()
		for !done() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s: %v", label, ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	wait("planner boundary", func() bool { _, e := os.Stat(arrived); return e == nil })
	snap, e := app.Engine.Runtime.Snapshot(sceneID)
	if e != nil {
		t.Fatal(e)
	}
	witness, e := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer witness.Close()
	refs := []memoryservice.Reference{}
	secondSource := field(inspect(), "source").(string)
	if secondSource == sourceID {
		t.Fatal("second real source missing")
	}
	for index, content := range []string{"Original concise reply declaration.", "Original synthetic task declaration."} {
		seedSource := []string{sourceID, secondSource}[index]
		var evidence []any
		e = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			message, e := app.Engine.Runtime.PreviewMessage(sceneID, seedSource, reads)
			if e != nil {
				return e
			}
			evidence, e = sourceadmission.TrustedEvidence([]*jsjson.Object{message}, func(scene, id string) (bool, error) { return reads.IsRecalled(snap.Scene, id) })
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		proposal := memoryToolObject("sceneId", sceneID, "simulation", false, "provenance", "source-only", "sourceEvidence", evidence)
		candidate := memoryToolObject("content", content, "kind", "fact", "epistemicKind", "explicit", "sources", []any{memoryToolObject("sceneId", sceneID, "messageId", seedSource, "senderId", "synthetic-user")}, "subject", memoryToolObject("kind", "self", "sourceMessageId", seedSource))
		result, e := app.Semantic.Memory.Propose(ctx, proposal, candidate, memoryservice.ProposalOptions{})
		if e != nil || result.Status != "saved" {
			t.Fatalf("real seed %+v %v", result, e)
		}
		ref := memoryservice.Reference{ID: field(result.Memory, "id").(string)}
		if e = witness.QueryRowContext(ctx, "SELECT version,deletion_epoch FROM memory_assertion_state WHERE assertion_id=?", ref.ID).Scan(&ref.Version, &ref.DeletionEpoch); e != nil {
			t.Fatal(e)
		}
		refs = append(refs, ref)
	}
	current, e := app.Semantic.Memory.SemanticCurrent(ctx, memoryservice.Context{SceneID: sceneID}, refs[0])
	if e != nil || current == nil {
		t.Fatal(current, e)
	}
	ticket, e := app.Engine.Read.Require(ctx, sceneID)
	if e != nil {
		t.Fatal(e)
	}
	resolved, e := app.Semantic.ResolveDirty(ctx, []semantic.Dirty{{SceneID: sceneID, Trigger: memoryToolObject("ref", memoryToolObject("id", refs[0].ID, "version", float64(refs[0].Version), "deletionEpoch", float64(refs[0].DeletionEpoch)), "dependencyKey", field(current, "dependencyKey")), Reason: "assertion_committed"}}, ticket)
	if e != nil || resolved == nil {
		t.Fatal(resolved, e)
	}
	rootSet := map[string]bool{}
	for _, entry := range resolved.Entries {
		for _, root := range field(entry.Assertion, "roots").([]any) {
			rootSet[root.(string)] = true
		}
	}
	t.Logf("actual semantic resolved: coverage=%s entries=%d roots=%d sourceIDs=%v", resolved.Coverage, len(resolved.Entries), len(rootSet), []string{sourceID, secondSource})
	if resolved.Coverage != "complete" || len(resolved.Entries) != 2 || len(rootSet) != 2 {
		t.Fatal("actual two-source fixture did not produce complete two-root partition")
	}
	captured := semantic.CaptureInput(sceneID, *resolved)
	if captured == nil {
		t.Fatal("incomplete semantic capture")
	}
	started, e := app.Semantic.BeginAttempt(ctx, captured)
	if e != nil || !started {
		t.Fatal(started, e)
	}
	result, e := app.Semantic.Commit(ctx, captured, []*jsjson.Object{memoryToolObject("kind", "duplicate_candidate", "selectedParentIds", []any{refs[0].ID, refs[1].ID}, "text", "Possible duplicate declarations, not merged.", "hypothesis", true)}, func(ws *recovery.WriteSession) error {
		if !app.Semantic.ValidInSession(ctx, captured, ws) {
			return context.Canceled
		}
		return nil
	})
	if e != nil || result.Created != 1 {
		t.Fatalf("real admission %+v %v", result, e)
	}
	if e = app.Semantic.FinishAttempt(ctx, captured, "finished"); e != nil {
		t.Fatal(e)
	}
	observed := make(chan string, 4)
	app.Engine.Runtime.AddExtension(&botruntime.Extension{ContextSelected: func(c *botruntime.TurnContext, brief *jsjson.Object) error {
		if app.SemanticExposure.CaptureGuard(c) == nil {
			return errors.New("actual_semantic_guard_missing")
		}
		receipts, _ := field(brief, "actionReceipts").([]any)
		if len(receipts) != 1 || field(receipts[0], "toolName") != "learning.propose" || field(receipts[0], "status") != "rejected" || field(receipts[0], "reason") != "semantic_read_not_learning_authority" {
			return errors.New("actual_semantic_learning_rejection_missing")
		}
		targets, _ := field(field(brief, "plan"), "targetMessageIds").([]any)
		if len(targets) == 0 {
			return errors.New("actual_semantic_target_missing")
		}
		id, _ := targets[0].(string)
		found := false
		for _, source := range c.Sources() {
			if field(source, "id") == id {
				found = true
			}
		}
		if !found {
			return errors.New("actual_semantic_target_not_original")
		}
		observed <- id
		return nil
	}})
	if e = os.WriteFile(resume, []byte("resume"), 0600); e != nil {
		t.Fatal(e)
	}
	wait("native semantic send", func() bool { return len(field(inspect(), "sent").([]any)) == 1 })
	wait("foreground physical idle", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("missing first semantic action receipt", ctx.Err())
	}
	if e = app.Contextual.WhenIdle(ctx); e != nil {
		t.Fatal(e)
	}
	if app.Contextual.Stats().Accepted != 0 {
		t.Fatal("semantic learning entered queue")
	}
	var count int
	if e = witness.QueryRowContext(ctx, "SELECT count(*) FROM memories WHERE json_extract(value,'$.content')='Must not persist derived semantic learning.'").Scan(&count); e != nil || count != 0 {
		t.Fatal("semantic-derived learning persisted", count, e)
	}
	if len(field(inspect(), "planSelections").([]any)) != 1 {
		t.Fatal("model did not select original semantic receipt")
	}
	pending, released := filepath.Join(options.PluginRoot, "semantic-send.arrived"), filepath.Join(options.PluginRoot, "semantic-send.resume")
	t.Cleanup(func() { _ = os.WriteFile(released, []byte("resume"), 0600) })
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/hold-send", map[string]any{"arrived": pending, "resume": released})
	appRequest(t, app, http.MethodPost, "/api/plugins/state/actions/new-source", map[string]any{})
	wait("pending semantic send", func() bool { _, e := os.Stat(pending); return e == nil })
	input["memorySemanticAnswerEnabled"] = false
	appRequest(t, app, http.MethodPut, "/api/settings", input)
	if e = os.WriteFile(released, []byte("resume"), 0600); e != nil {
		t.Fatal(e)
	}
	wait("revoked turn physical idle", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 })
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("missing second semantic action receipt", ctx.Err())
	}
	if e = app.Contextual.WhenIdle(ctx); e != nil {
		t.Fatal(e)
	}
	if app.Contextual.Stats().Accepted != 0 {
		t.Fatal("revoked semantic learning entered queue")
	}
	final := inspect()
	if len(field(final, "sent").([]any)) != 1 || field(final, "sendAttempts") != float64(2) {
		t.Fatalf("revoked send escaped: %#v", final)
	}
	if e = witness.QueryRowContext(ctx, "SELECT count(*) FROM memories WHERE json_extract(value,'$.content')='Must not persist derived semantic learning.'").Scan(&count); e != nil || count != 0 {
		t.Fatal("revoked learning persisted", count, e)
	}
	if e = app.Shutdown(ctx); e != nil {
		t.Fatal(e)
	}
	app = nil
}
