package learningowner

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"testing"
)

func TestConcreteSettingsAndSourceRevisionReads(t *testing.T) {
	f := scopedSetup(t)
	o := &Owner{d: Dependencies{Main: f.main, Authority: f.recovery, Assertions: f.store, Sources: f.events}}
	ctx := context.Background()
	settings, e := o.Settings(ctx)
	if e != nil || settings.Timezone == "" {
		t.Fatalf("default settings: %+v %v", settings, e)
	}
	var configured map[string]any
	if e = json.Unmarshal(contract.DefaultSettingsInput(), &configured); e != nil {
		t.Fatal(e)
	}
	configured["timezone"] = "UTC"
	for _, k := range []string{"memoryGraphEnabled", "memoryCommitmentsEnabled", "memoryKnowledgeEnabled", "memoryGraphAutoAdmissionEnabled", "sourceEventRevisionsEnabled"} {
		configured[k] = true
	}
	raw, _ := json.Marshal(configured)
	if _, e = f.main.Exec("INSERT INTO config VALUES('settings',?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	settings, e = o.Settings(ctx)
	if e != nil || !settings.GraphEnabled || !settings.CommitmentsEnabled || !settings.KnowledgeEnabled || !settings.Proposal.GraphAutoAdmission {
		t.Fatalf("fresh settings: %+v %v", settings, e)
	}
	enabled, e := o.Enabled(ctx, "s")
	if e != nil || !enabled {
		t.Fatalf("enabled %v %v", enabled, e)
	}
	a, e := sourceevents.NewRuntimeAuthority(f.events)
	if e != nil {
		t.Fatal(e)
	}
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "adapter", AccountID: "account", PlatformSceneID: "s"}, LearningEnabled: true}
	m := parseObject(t, `{"id":"m","sceneId":"s","senderId":"p","origin":"live","status":"available","parts":[{"type":"text","text":"Synthetic original"}],"sentAt":"2026-10-28T12:00:00.000Z","receivedAt":"2026-10-28T12:00:00.000Z"}`)
	_, original, e := a.Observe(ctx, scene, m, nil)
	if e != nil {
		t.Fatal(e)
	}
	if original == nil {
		t.Fatal("trusted source missing")
	}
	current, e := o.CurrentSource(ctx, original)
	if e != nil || !current {
		t.Fatalf("current %v %v", current, e)
	}
	changed := m.Clone()
	_ = changed.Set("parts", []any{obj("type", "text", "text", "Synthetic replacement")})
	edit := obj("editId", "edit-1", "order", obj("kind", "observed_cas", "expectedBase", get(original, "sourceEventRef")))
	_, replacement, e := a.Observe(ctx, scene, changed, edit)
	if e != nil {
		t.Fatal(e)
	}
	current, e = o.CurrentSource(ctx, original)
	if e != nil || current {
		t.Fatalf("old source survived edit %v %v", current, e)
	}
	current, e = o.CurrentSource(ctx, replacement)
	if e != nil || !current {
		t.Fatalf("replacement %v %v", current, e)
	}
	if _, e = f.main.Exec(`INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T12:00:02.000Z')`); e != nil {
		t.Fatal(e)
	}
	recalled, e := o.Recalled(ctx, "s", "m")
	if e != nil || !recalled {
		t.Fatalf("recall %v %v", recalled, e)
	}
	current, e = o.CurrentSource(ctx, replacement)
	if e != nil || current {
		t.Fatalf("recalled source current %v %v", current, e)
	}
	if _, e = f.main.Exec(`UPDATE scenes SET value=json_set(value,'$.learningEnabled',json('false')) WHERE id='s'`); e != nil {
		t.Fatal(e)
	}
	enabled, e = o.Enabled(ctx, "s")
	if e != nil || enabled {
		t.Fatalf("disabled %v %v", enabled, e)
	}
}
