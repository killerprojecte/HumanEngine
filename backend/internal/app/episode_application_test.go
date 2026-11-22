package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
)

func TestInstalledEpisodesWholeReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	options := applicationOptions(t)
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	req := func(method, path string, value any) any { t.Helper(); return appRequest(t, app, method, path, value) }
	wire := func(o *jsjson.Object) json.RawMessage {
		t.Helper()
		b, err := jsjson.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	setSettings := func(changes map[string]any) {
		t.Helper()
		settings := req("GET", "/api/settings", nil).(*jsjson.Object)
		for k, v := range changes {
			_ = settings.Set(k, v)
		}
		req("PUT", "/api/settings", wire(settings))
	}
	setSettings(map[string]any{"sourceEventRevisionsEnabled": true, "memoryEpisodesEnabled": false, "memoryCommitmentsEnabled": true, "memoryCommitmentsAnswerEnabled": true, "promptContextWindowEnabled": false, "debounceMs": float64(0), "groupTurnCooldownMs": float64(0)})
	req("PUT", "/api/models", json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"runtime-fixture:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`))
	plugin := "/api/plugins/runtime-fixture/"
	req("PUT", plugin+"config", map[string]any{"timestamp": now})
	req("PUT", plugin+"enabled", map[string]bool{"enabled": true})
	action := func(name string, value any) any { t.Helper(); return req("POST", plugin+"actions/"+name, value) }
	inspect := func() any { return action("inspect", map[string]any{}) }
	started := inspect()
	sceneID := field(field(started, "scene"), "id").(string)
	scenePath := "/api/scenes/" + url.PathEscape(sceneID)
	var scene *jsjson.Object
	for _, row := range req("GET", "/api/scenes", nil).([]any) {
		if field(row, "id") == sceneID {
			scene = row.(*jsjson.Object)
			break
		}
	}
	if scene == nil {
		t.Fatal("fixture scene was not registered")
	}
	sceneFlags := func(changes map[string]bool) {
		t.Helper()
		for k, v := range changes {
			_ = scene.Set(k, v)
		}
		req("PUT", scenePath, wire(scene))
	}
	sceneFlags(map[string]bool{"learningEnabled": true, "participationEnabled": false})
	record := func(id, sender, kind, content string, commitment *jsjson.Object) memoryservice.ProposalResult {
		t.Helper()
		snapshot, err := app.Engine.Runtime.Snapshot(sceneID)
		if err != nil {
			t.Fatal(err)
		}
		var evidence []any
		err = app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			message, e := app.Engine.Runtime.PreviewMessage(sceneID, id, reads)
			if e != nil {
				return e
			}
			evidence, e = sourceadmission.TrustedEvidence([]*jsjson.Object{message}, func(_, id string) (bool, error) { return reads.IsRecalled(snapshot.Scene, id) })
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		candidate := memoryToolObject("content", content, "kind", kind, "epistemicKind", "explicit", "sources", []any{memoryToolObject("sceneId", sceneID, "messageId", id, "senderId", sender)}, "subject", memoryToolObject("kind", "self", "sourceMessageId", id))
		if commitment != nil {
			_ = candidate.Set("commitment", commitment)
		}
		out, err := app.Semantic.Memory.Propose(ctx, memoryToolObject("sceneId", sceneID, "simulation", false, "provenance", "source-only", "sourceEvidence", evidence), candidate, memoryservice.ProposalOptions{})
		if err != nil || out.Status != "saved" {
			t.Fatalf("fixture candidate did not save: %+v, %v", out, err)
		}
		return out
	}
	ingest := func(id, sender, text string, extra ...any) {
		t.Helper()
		parts := append([]any{memoryToolObject("type", "text", "text", text)}, extra...)
		_, err := app.Engine.Runtime.Ingest(ctx, sceneID, memoryToolObject("id", id, "sceneId", sceneID, "senderId", sender, "sentAt", now, "receivedAt", now, "origin", "live", "status", "available", "parts", parts), false)
		if err != nil {
			t.Fatal(err)
		}
	}
	source := field(started, "source").(string)
	first := record(source, "synthetic-user", "experience", "EPISODE_ORIGINAL: I attended one qualified fixture event.", nil)
	const secondMessage = "episode-qualified-second"
	const secondContent = "EPISODE_SECOND: I attended a separate qualified fixture event."
	ingest(secondMessage, "second", secondContent)
	second := record(secondMessage, "second", "experience", secondContent, nil)
	const planMessage = "episode-qualified-plan"
	ingest(planMessage, "synthetic-user", "I propose Synthetic full plan with the named participant. Time: 18:00; exact date unknown. Conditions: Only if participants confirm.", memoryToolObject("type", "mention", "userId", "second"))
	plan := record(planMessage, "synthetic-user", "fact", "Synthetic declaration proposal", memoryToolObject("kind", "propose", "terms", memoryToolObject("kind", "plan", "title", "Synthetic full plan", "participantIds", []any{"synthetic-user", "second"}, "temporalScope", "future", "polarity", "positive", "modality", "asserted", "conditions", []any{"Only if participants confirm"}, "timeQualification", "18:00; exact date unknown")))
	if plan.Commitment == nil || plan.Commitment.Status != "saved" {
		t.Fatalf("qualified plan did not save: %+v", plan.Commitment)
	}
	wait := func(label string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal(label, ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	var detached atomic.Pointer[botruntime.ToolResultGuard]
	var exposed atomic.Bool
	app.Engine.Runtime.AddExtension(&botruntime.Extension{ContextSelected: func(c *botruntime.TurnContext, _ *jsjson.Object) error {
		if len(app.MemoryExposure.ReadExposed(c)) != 0 {
			exposed.Store(true)
		}
		if g := app.EpisodeExposure.CaptureGuard(c); g != nil {
			detached.CompareAndSwap(nil, g)
		}
		return nil
	}})
	setSettings(map[string]any{"memoryEpisodesEnabled": true})
	sceneFlags(map[string]bool{"participationEnabled": true, "memoryReadEnabled": true, "personalizationEnabled": true})
	inspectList := func(key string) []any { return field(inspect(), key).([]any) }
	last := func(key string) any {
		t.Helper()
		values := inspectList(key)
		if len(values) == 0 {
			t.Fatalf("missing %s", key)
		}
		return values[len(values)-1]
	}
	begin := func(mode string) {
		t.Helper()
		before := len(inspectList("episodeAds"))
		action("episode-mode", map[string]any{"mode": mode})
		action("new-source", map[string]any{})
		wait("Planner for "+mode, func() bool { return len(inspectList("episodeAds")) > before })
	}
	end := func() { wait("episode turn settled", func() bool { return app.Engine.Runtime.ActiveTasks() == 0 }) }
	checkPage := func(value any, want ...string) []any {
		t.Helper()
		states, ok := value.([]any)
		if !ok || len(states) != 1 || field(states[0], "stateKind") != "experience_view" {
			t.Fatalf("expected a single qualified state page, got %+v", value)
		}
		page := field(states[0], "page")
		if field(page, "interpretation") != "independent_qualified_records_not_synthesized_episodes" {
			t.Fatal("episode qualification was dropped")
		}
		items := field(page, "items").([]any)
		if len(items) != len(want) {
			t.Fatalf("qualified page has %d entries, want %d", len(items), len(want))
		}
		for i, entry := range items {
			if field(entry, "kind") != want[i] {
				t.Fatalf("episode entry %d has kind %v, want %s", i, field(entry, "kind"), want[i])
			}
			if want[i] == "reported_experience" && field(field(entry, "time"), "occurredAt") != nil {
				t.Fatal("unknown occurrence time was synthesized")
			}
			if want[i] == "shared_plan_declaration" && (field(entry, "completionBasis") != "participant_self_report_not_observed_event" || field(field(entry, "view"), "qualification") != "declared_not_verified") {
				t.Fatal("qualified plan was presented as an observed event")
			}
		}
		return items
	}
	witness, err := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer witness.Close()
	count := func(record memoryservice.ProposalResult) int {
		t.Helper()
		var n int
		if err := witness.QueryRowContext(ctx, `SELECT count(*) FROM memory_working_set_receipts WHERE json_extract(value, '$.selection.ref.id')=?`, field(record.Memory, "id")).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	begin("selected")
	end()
	reads := last("episodeReads").([]any)
	if len(reads) != 2 {
		t.Fatalf("read %d episode receipts, want two", len(reads))
	}
	for _, value := range reads {
		checkPage(value, "reported_experience", "reported_experience", "shared_plan_declaration")
	}
	bundle := last("episodeBundles")
	kept := field(bundle, "evidence").([]any)
	if len(kept) != 2 {
		t.Fatalf("Replyer got %d selected pages, want two", len(kept))
	}
	for _, receipt := range kept {
		checkPage(field(receipt, "states"), "reported_experience", "reported_experience", "shared_plan_declaration")
	}
	actions := field(bundle, "actions").([]any)
	if len(actions) != 1 || field(actions[0], "status") != "rejected" || field(actions[0], "reason") != "workspace_read_not_learning_authority" ||
		app.Contextual.Stats().Accepted != 0 || exposed.Load() || len(inspectList("sent")) != 1 || count(first) != 1 || count(second) != 1 {
		t.Fatal("whole receipts did not preserve exact selection, authority, or ACK behavior")
	}
	setSettings(map[string]any{"memoryCommitmentsEnabled": false, "memoryCommitmentsAnswerEnabled": false})
	begin("person")
	end()
	reads = last("episodeReads").([]any)
	entries := checkPage(reads[0], "reported_experience")
	if count(first) != 2 || count(second) != 1 || field(field(entries[0], "assertionRef"), "id") == nil {
		t.Fatal("person-filtered episode or retained references differed with commitments off")
	}
	budget := field(req("GET", "/api/settings", nil), "replyEvidenceMaxBytes")
	oversizedContent := "OMITTED_EPISODE " + strings.Repeat("限定内容", 700)
	ingest("episode-qualified-large", "synthetic-user", "I reported the long experience in this fixture.")
	oversized := record("episode-qualified-large", "synthetic-user", "experience", oversizedContent, nil)
	setSettings(map[string]any{"replyEvidenceMaxBytes": float64(8192)})
	beforeBundles := len(inspectList("episodeBundles"))
	begin("person")
	end()
	if len(inspectList("episodeBundles")) != beforeBundles+1 {
		t.Fatal("omission fixture did not reach the Replyer")
	}
	bundle = last("episodeBundles")
	if len(field(bundle, "evidence").([]any)) != 0 || field(field(bundle, "coverage"), "status") != "omitted" || count(first) != 2 || count(oversized) != 0 {
		t.Fatalf("oversized page: retained=%d coverage=%v initial=%d oversized=%d", len(field(bundle, "evidence").([]any)), field(field(bundle, "coverage"), "status"), count(first), count(oversized))
	}
	setSettings(map[string]any{"replyEvidenceMaxBytes": budget})
	for _, flag := range []string{"personalizationEnabled", "memoryReadEnabled"} {
		beforeReads, beforeBundles := len(inspectList("episodeReads")), len(inspectList("episodeBundles"))
		sceneFlags(map[string]bool{flag: false})
		begin("person")
		end()
		if len(inspectList("episodeReads")) != beforeReads || len(inspectList("episodeBundles")) != beforeBundles {
			t.Fatalf("episode reached model delivery after %s was disabled", flag)
		}
		sceneFlags(map[string]bool{flag: true})
	}
	settings := req("GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("memoryEpisodesEnabled", false)
	disableBody := wire(settings)
	var lateStatus atomic.Int32
	var gate atomic.Bool
	gate.Store(true)
	app.Engine.Runtime.Observe(func(event botruntime.Event) {
		app.observeRuntime(event)
		if event.SceneID != sceneID || event.Stage != "tool" || event.Status != "completed" || event.ToolName != episodeTool || event.Counts["results"] != 1 || !gate.CompareAndSwap(true, false) {
			return
		}
		request := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(disableBody)).WithContext(ctx)
		sourceFixtureAuth(request, true)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)
		lateStatus.Store(int32(recorder.Code))
	})
	before := count(first)
	beforeReads, beforeReplies := len(inspectList("episodeReads")), len(inspectList("episodeBundles"))
	begin("person")
	end()
	if lateStatus.Load() != 200 || count(first) != before {
		t.Fatal("late feature revocation did not suppress episode selection")
	}
	for _, fresh := range inspectList("episodeReads")[beforeReads:] {
		reads = fresh.([]any)
		if len(reads) != 1 || len(reads[0].([]any)) != 0 {
			t.Fatal("late feature revocation returned a prepared episode")
		}
	}
	for _, fresh := range inspectList("episodeBundles")[beforeReplies:] {
		if len(field(fresh, "evidence").([]any)) != 0 {
			t.Fatal("late feature revocation reached the Replyer")
		}
	}
	begin("person")
	end()
	if last("episodeAds") != false {
		t.Fatal("disabled episodes were advertised on a fresh turn")
	}
	setSettings(map[string]any{"memoryEpisodesEnabled": true})
	detached.Store(nil)
	arrived, resume := filepath.Join(options.PluginRoot, "episodeSendArrived"), filepath.Join(options.PluginRoot, "episodeSendResume")
	release := func() { _ = os.WriteFile(resume, []byte("resume"), 0600) }
	defer release()
	action("episode-hold-send", map[string]any{"arrived": arrived, "resume": resume})
	sent := len(inspectList("sent"))
	begin("person")
	wait("physical adapter reached its boundary", func() bool { _, err := os.Stat(arrived); return err == nil })
	guard := detached.Load()
	if guard == nil {
		t.Fatal("missing detached episode guard")
	}
	checkGuard := func() bool {
		t.Helper()
		var active bool
		err := app.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { var e error; active, e = guard.Check(reads); return e })
		if err != nil {
			t.Fatal(err)
		}
		return active
	}
	if !checkGuard() {
		t.Fatal("episode guard was invalid before recall")
	}
	if err := app.Engine.Runtime.Recall(ctx, sceneID, source); err != nil {
		t.Fatal(err)
	}
	release()
	end()
	if len(inspectList("sent")) != sent || checkGuard() {
		t.Fatal("recalled qualified episode reached the wire")
	}
}
