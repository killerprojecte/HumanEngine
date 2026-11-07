package sourceevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestRuntimeDerivedValidationUsesRecallTransaction(t *testing.T) {
	raw, err := os.ReadFile("testdata/runtime-authority-faults.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		RecallValidation struct {
			Error                      string
			SawTombstone               bool
			Tombstones, Ack, Authority int
		}
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	expected := oracle.RecallValidation
	if expected.Error != "synthetic_derived_validation" || !expected.SawTombstone || expected.Authority != 1 {
		t.Fatal("missing actual-source recall proof", expected)
	}
	f, a := runtimeFixture(t, Options{})
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group", AdapterID: "fixture", AccountID: "bot", PlatformSceneID: "group"}, ParticipationEnabled: true}
	runtimePut(t, f.main, "scenes", scene.ID, `{"id":"scene","kind":"group","participationEnabled":true}`)
	dates, err := jsdate.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
	policy, err := replyplan.New(compare, dates)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan func(botruntime.Completion, error), 1)
	wanted := errors.New("synthetic_derived_validation")
	var retained botruntime.ReadAuthority
	observed := false
	model := botruntime.ModelFunc(func(_ context.Context, request botruntime.ModelRequest, settle func(botruntime.Completion, error)) {
		request.Turn.AddScopedValidation(func(scope botruntime.ReadAuthority) (bool, error) {
			retained = scope
			recalled, e := scope.IsRecalled(scene, "unrelated")
			if e != nil {
				return false, e
			}
			observed = recalled
			return false, wanted
		})
		ready <- settle
	})
	cfg := botruntime.DefaultSettings()
	cfg.Persona = "Fixture persona"
	cfg.PromptContextWindowEnabled = false
	r, err := botruntime.New(cfg, botruntime.Services{Authority: a, Models: model, Clock: runtimeTestClock{}, Dates: dates, Policy: policy, Compare: compare})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	message := runtimeObject(t, `{"id":"source","sceneId":"scene","senderId":"user","origin":"live","status":"available","sentAt":"2026-10-28T00:00:00.000Z","receivedAt":"2026-10-28T00:00:00.000Z","parts":[{"type":"text","text":"Synthetic."}]}`)
	topic, err := r.Ingest(context.Background(), scene.ID, message, false)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := r.Run(scene.ID, topic, false)
	if err != nil {
		t.Fatal(err)
	}
	var settle func(botruntime.Completion, error)
	select {
	case settle = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	err = r.Recall(context.Background(), scene.ID, "unrelated")
	if !errors.Is(err, wanted) || !observed {
		t.Fatalf("scoped validation error=%v saw tombstone=%v", err, observed)
	}
	if err = retained.Ready(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("retained scope=%v", err)
	}
	var count, seq int
	if err = f.main.QueryRow("SELECT count(*) FROM recall_tombstones").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = f.main.QueryRow("SELECT json_extract(value,'$.head.sequence') FROM recovery_gate").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	head, headErr := f.authority.Head(context.Background())
	if headErr != nil {
		t.Fatal(headErr)
	}
	if count != expected.Tombstones || seq != expected.Ack || int(head.Sequence) != expected.Authority || a.Ready() == nil {
		t.Fatalf("rollback lost: tombstones=%d ack=%d", count, seq)
	}
	settle(botruntime.Completion{}, errors.New("synthetic_model_stop"))
	select {
	case <-handle.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not settle")
	}
}
