package httpapi

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	"testing"
	"time"
)

func TestMemeEngineCacheGuardAcrossActualTurns(t *testing.T) {
	_, db, engine := memeHTTP(t)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "a", AccountID: "account", PlatformSceneID: "synthetic"}, ParticipationEnabled: true}
	if _, e := db.Exec(`INSERT INTO scenes VALUES('s','{"id":"s","kind":"group","adapterId":"a","accountId":"account","platformSceneId":"synthetic","participationEnabled":true,"memoryReadEnabled":true,"personalizationEnabled":true,"memoryReadRevision":0}')`); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	msg := memeObject("id", "m", "sceneId", "s", "senderId", "person", "sentAt", now, "receivedAt", now, "origin", "live", "status", "available", "parts", []any{memeObject("type", "text", "text", "Synthetic cache source")})
	if _, e := engine.Runtime.IngestScene(ctx, scene, msg, false); e != nil {
		t.Fatal(e)
	}
	snapshot, e := engine.Runtime.Snapshot("s")
	if e != nil || len(snapshot.Topics) != 1 {
		t.Fatal(snapshot, e)
	}
	stop := errors.New("synthetic_capture_complete")
	captured := make(chan []*botruntime.ToolResultGuard, 1)
	engine.Runtime.AddExtension(&botruntime.Extension{ContextPrompt: func(turn *botruntime.TurnContext) (string, error) {
		if _, e := engine.TurnRead.Begin(ctx, turn); e != nil {
			return "", e
		}
		// Ordinary turn validation is deliberately not promoted into cached guards.
		turn.AddValidation(func() bool { return false })
		captured <- engine.Runtime.CaptureToolResultGuards(turn)
		return "", stop
	}})
	run := func() []*botruntime.ToolResultGuard {
		t.Helper()
		h, e := engine.Runtime.Run("s", snapshot.Topics[0].ID, false)
		if e != nil {
			t.Fatal(e)
		}
		select {
		case <-h.Done:
		case <-time.After(5 * time.Second):
			t.Fatal("turn failed to settle")
		}
		result := h.Wait()
		if !errors.Is(result.Err, stop) {
			t.Fatalf("capture not reached: %+v", result)
		}
		select {
		case guards := <-captured:
			if len(guards) != 1 {
				t.Fatalf("capture registered %d times", len(guards))
			}
			return guards
		default:
			t.Fatal("capture missing")
			return nil
		}
	}
	old := run()
	cache := toolresults.New(toolresults.Config{TempDir: t.TempDir()})
	defer cache.Dispose()
	owner := toolresults.Owner{Scope: "same-scene", SceneID: "s", PluginID: "synthetic"}
	for _, guard := range old {
		g := guard
		owner.Validations = append(owner.Validations, func() (bool, error) { return engine.Runtime.CheckToolResultGuard(g) })
	}
	saved, e := cache.Store(ctx, toolresults.Text("synthetic cached data"), owner, toolresults.Options{})
	if e != nil {
		t.Fatal(e)
	}
	// A second actual turn and same-scope read keep the original captured ticket.
	fresh := run()
	if _, e = cache.Read(ctx, toolresults.ReadInput{ResultID: saved.ResultID}, owner.Scope, nil); e != nil {
		t.Fatal(e)
	}
	if e = engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, e := old[0].Check(reads)
		if e == nil && !ok {
			return errors.New("same-owner guard rejected")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE scenes SET value=json_set(value,'$.memoryReadEnabled',json('false'),'$.memoryReadRevision',1) WHERE id='s'`); e != nil {
		t.Fatal(e)
	}
	if _, e = cache.Read(ctx, toolresults.ReadInput{ResultID: saved.ResultID}, owner.Scope, nil); !errors.Is(e, toolresults.ErrInvalidated) {
		t.Fatalf("cache survived revoke: %v", e)
	}
	if _, e = db.Exec(`UPDATE scenes SET value=json_set(value,'$.memoryReadEnabled',json('true')) WHERE id='s'`); e != nil {
		t.Fatal(e)
	}
	newest := run()
	for _, g := range []*botruntime.ToolResultGuard{old[0], fresh[0]} {
		ok, e := engine.Runtime.CheckToolResultGuard(g)
		if e != nil || ok {
			t.Fatal("old ticket renewed", ok, e)
		}
	}
	ok, e := engine.Runtime.CheckToolResultGuard(newest[0])
	if e != nil || !ok {
		t.Fatal("new ticket unavailable", ok, e)
	}
}
