package app

import (
	"encoding/json"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
)

func TestInstalledRuntimeRecallRedactsMemoryAndRetiresReceipt(t *testing.T) {
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	const scene, source = "recall-scene", "synthetic-recalled-source"
	appRequest(t, app, "PUT", "/api/scenes/"+scene, json.RawMessage(`{
		"id":"recall-scene","kind":"group","adapterId":"fixture","accountId":"bot",
		"platformSceneId":"group","name":"Synthetic","participationEnabled":false,
		"learningEnabled":false,"proactiveEnabled":false}`))
	object := func(raw string) *jsjson.Object {
		t.Helper()
		value, err := jsjson.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return value.(*jsjson.Object)
	}
	msg := object(`{"id":"synthetic-recalled-source","sceneId":"recall-scene","senderId":"synthetic-person",
		"sentAt":"2099-01-01T00:00:00Z","receivedAt":"2099-01-01T00:00:00Z",
		"origin":"live","status":"available","parts":[{"type":"text","text":"Synthetic"}]}`)
	if _, err := app.Engine.Runtime.Ingest(ctx, scene, msg, false); err != nil {
		t.Fatal(err)
	}
	memory := app.Semantic.Memory
	seed, err := memory.Create(ctx, object(`{"scope":{"kind":"scene","sceneId":"recall-scene"},
		"content":"Synthetic source-backed fact","kind":"fact","sources":[
		{"sceneId":"recall-scene","messageId":"synthetic-recalled-source","senderId":"synthetic-person"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := field(seed, "id").(string)
	lock := object(`{"locked":true}`)
	guard := memorymanual.Guard{ExpectedVersion: 1, IdempotencyKey: "synthetic-lock"}
	if _, err := memory.UpdateGuarded(ctx, id, lock, guard); err != nil {
		t.Fatal(err)
	}
	if err := app.Engine.Runtime.Recall(ctx, scene, source); err != nil {
		t.Fatal(err)
	}
	var sourceTombstone, experienceTombstone, sourceRefs, historyRefs int
	err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?),
		EXISTS(SELECT 1 FROM memory_experience_tombstones WHERE key=?),
		(SELECT count(*) FROM memory_sources WHERE memory_id=?),
		(SELECT count(*) FROM memory_assertion_revision_sources WHERE assertion_id=?)`,
		scene, source, experienceservice.SourceKey(scene, source), id, id).
		Scan(&sourceTombstone, &experienceTombstone, &sourceRefs, &historyRefs)
	if err != nil || sourceTombstone != 1 || experienceTombstone != 1 || sourceRefs != 0 || historyRefs != 0 {
		t.Fatalf("recall owner outcomes: source=%d experience=%d current refs=%d history refs=%d: %v",
			sourceTombstone, experienceTombstone, sourceRefs, historyRefs, err)
	}
	record, err := memory.LearningRecord(ctx, id)
	if err != nil || field(record, "status") != "invalidated" || len(field(record, "sources").([]any)) != 0 {
		t.Fatalf("current assertion retained recalled evidence: %+v %v", record, err)
	}
	if _, err := memory.UpdateGuarded(ctx, id, lock, guard); err == nil || err.Error() != "memory_command_superseded" {
		t.Fatalf("recall did not retire exact guarded replay: %v", err)
	}
	eligible, err := memory.IndexableByIDs(ctx, []string{id})
	if err != nil || len(eligible) != 0 {
		t.Fatalf("recalled assertion remained indexable: %d %v", len(eligible), err)
	}
}
