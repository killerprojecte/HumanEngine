package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/v1adapterruntime"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestActualV1ManagedOutgoingUsage(t *testing.T) {
	for _, name := range []string{"sent", "no-id", "failed", "unknown", "recall-after-ack", "learning-off-after-ack", "shutdown-held-sql", "real-recall"} {
		t.Run(name, func(t *testing.T) { runActualV1ManagedOutgoing(t, name) })
	}
}
func runActualV1ManagedOutgoing(t *testing.T, name string) {
	_, db, engine := memeHTTP(t)
	db.SetMaxOpenConns(1) // Deliberate test-only ownership/deadlock probe.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	settings, e := jsjson.Parse(string(contract.DefaultSettingsInput()))
	if e != nil {
		t.Fatal(e)
	}
	settings.(*jsjson.Object).Set("sourceEventRevisionsEnabled", true)
	encoded, e := jsjson.Stringify(settings)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", encoded); e != nil {
		t.Fatal(e)
	}
	var diagnostic adapterDiagnosticBuffer
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal("Node required for actual V1 runtime fixture", e)
	}
	bridge, e := v1adapterruntime.New(engine, node, &diagnostic, plugins.V1HostOptions{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := bridge.CloseAndWait(context.Background()); e != nil {
			t.Error(e)
		}
	})
	zero, timeout := float64(0), float64(10000)
	const protocol = "outgoing-fixture:deterministic"
	engine.Models.Policy, e = modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: protocol, RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "tools"}}}, Routes: []modelservice.Route{{Role: "planner", OrderedModelIDs: []string{"m"}}, {Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	engine.Models.Client = &providertransport.Client{Credentials: memeHTTPKeys{}, Budgets: providertransport.ExplicitOff{}}
	engine.Models.Providers = map[string]providertransport.Provider{"p": {ID: "p", Protocol: protocol, BaseURL: "https://synthetic.invalid"}}
	engine.Models.ProviderSnapshots = map[string]map[string]any{"p": {"id": "p", "protocol": protocol, "baseUrl": "https://synthetic.invalid"}}
	engine.Models.ModelSnapshots = map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic"}}
	path, e := filepath.Abs("testdata/outgoing-runtime.mjs")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	arrivedPath, resumePath := filepath.Join(dir, "arrived"), filepath.Join(dir, "resume")
	pluginConfig := map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "status": name}
	if name != "failed" && name != "unknown" {
		pluginConfig["status"] = "sent"
	}
	if name == "no-id" {
		pluginConfig["noID"] = true
	}
	blocked := name == "real-recall" || name == "recall-after-ack" || name == "learning-off-after-ack" || name == "shutdown-held-sql"
	if blocked {
		pluginConfig["beforeArrived"] = arrivedPath
		pluginConfig["beforeResume"] = resumePath
	}
	defer os.WriteFile(resumePath, []byte("release"), 0600)
	raw, e := bridge.Host.Enable(ctx, "outgoing-fixture", path, pluginConfig)
	if e != nil {
		t.Fatalf("enable: %v %s", e, diagnostic.String())
	}
	var loaded struct {
		Registrations []struct{ Kind, Name, Handle string }
	}
	if e = json.Unmarshal(raw, &loaded); e != nil {
		t.Fatal(e)
	}
	actions := map[string]string{}
	for _, r := range loaded.Registrations {
		if r.Kind == "action" {
			actions[r.Name] = r.Handle
		}
	}
	call := func(name string, args ...any) json.RawMessage {
		t.Helper()
		v, e := bridge.Host.Call(ctx, "outgoing-fixture", actions[name], args)
		if e != nil {
			t.Fatalf("%s: %v %s", name, e, diagnostic.String())
		}
		return v
	}
	var inspection struct {
		Scene      dispatch.SceneRef
		Source     string
		Sent       []json.RawMessage
		ModelCalls int
		Resolved   []struct {
			AssetID, Type string
			Bytes         int
		}
	}
	if e = json.Unmarshal(call("inspect"), &inspection); e != nil {
		t.Fatal(e)
	}
	snapshot, e := engine.Runtime.Snapshot(inspection.Scene.ID)
	if e != nil || len(snapshot.Messages) != 1 {
		t.Fatal(snapshot, e)
	}
	if _, e = db.Exec("UPDATE scenes SET value=json_set(value,'$.learningEnabled',json('true'),'$.memoryReadEnabled',json('true'),'$.personalizationEnabled',json('true')) WHERE id=?", inspection.Scene.ID); e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile("testdata/meme-history-image.json")
	if e != nil {
		t.Fatal(e)
	}
	var imageFixture struct{ Image string }
	if e = json.Unmarshal(raw, &imageFixture); e != nil {
		t.Fatal(e)
	}
	image, e := base64.StdEncoding.DecodeString(imageFixture.Image)
	if e != nil {
		t.Fatal(e)
	}
	root, release, e := engine.RootContext(ctx)
	if e != nil {
		t.Fatal(e)
	}
	asset, e := engine.Import(root, inspection.Scene.ID, inspection.Source, image)
	release()
	if e != nil {
		t.Fatal(e)
	}
	assetID := memeString(memeGet(asset, "id"))
	// The fixture seeds an already analyzed library item; it does not pretend to
	// exercise vision analysis. Search, authorization, planner receipts and send
	// all use the actual production owners.
	if _, e = db.Exec("UPDATE meme_assets SET value=json_set(value,'$.analyzed',json('true'),'$.available',json('true')) WHERE id=?", assetID); e != nil {
		t.Fatal(e)
	}
	call("asset", assetID)
	if _, e = db.Exec("UPDATE scenes SET value=json_set(value,'$.participationEnabled',json('true')) WHERE id=?", inspection.Scene.ID); e != nil {
		t.Fatal(e)
	}
	scene := snapshot.Scene
	scene.ParticipationEnabled = true
	scene.LearningEnabled = true
	if e = engine.Runtime.RegisterScene(scene); e != nil {
		t.Fatal(e)
	}
	snapshot, e = engine.Runtime.Snapshot(scene.ID)
	if e != nil || len(snapshot.Topics) != 1 {
		t.Fatal(snapshot, e)
	}
	ack := make(chan struct{}, 1)
	engine.Runtime.Observe(func(event botruntime.Event) {
		if event.Stage == "message.sent" {
			select {
			case ack <- struct{}{}:
			default:
			}
		}
	})
	turn, e := engine.Runtime.Run(scene.ID, snapshot.Topics[0].ID, false)
	if e != nil {
		t.Fatal(e)
	}
	var held *sql.Conn
	if blocked {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
	waitTransport:
		for {
			select {
			case <-ticker.C:
				if _, e = os.Stat(arrivedPath); e == nil {
					break waitTransport
				}
			case <-turn.Done:
				t.Fatalf("turn ended before actual V1 transport: %+v %s", turn.Wait(), diagnostic.String())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if name == "real-recall" {
			var recalled bool
			if e = json.Unmarshal(call("recall"), &recalled); e != nil || !recalled {
				t.Fatalf("actual adapter recall failed: %v %v", recalled, e)
			}
			if e = os.WriteFile(resumePath, []byte("release"), 0600); e != nil {
				t.Fatal(e)
			}
			var tombstones int
			if e = db.QueryRow("SELECT count(*) FROM recall_tombstones WHERE scene_id=? AND message_id=?", scene.ID, inspection.Source).Scan(&tombstones); e != nil || tombstones != 1 {
				t.Fatalf("actual recall owner did not persist source tombstone: %d %v", tombstones, e)
			}
		} else {
			held, e = db.Conn(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer held.Close()
			if e = os.WriteFile(resumePath, []byte("release"), 0600); e != nil {
				t.Fatal(e)
			}
			select {
			case <-ack:
			case <-turn.Done:
				if turn.Wait().Err != nil {
					t.Fatal(turn.Wait())
				}
				select {
				case <-ack:
				case <-ctx.Done():
					t.Fatal("no ACK trace", ctx.Err())
				}
			case <-ctx.Done():
				t.Fatal("ACK waits on usage SQL", ctx.Err())
			}
			switch name {
			case "recall-after-ack":
				// A separate accepted recall's durable tombstone becomes visible before
				// the waiting event can perform its execution-time source read.
				if e = json.Unmarshal(call("inspect"), &inspection); e != nil {
					t.Fatal(e)
				}
				var sentID string
				if e = json.Unmarshal(call("sent-id"), &sentID); e != nil {
					t.Fatal(e)
				}
				messageID := sentID
				if _, e = held.ExecContext(ctx, "INSERT INTO recall_tombstones VALUES(?,?,?)", scene.ID, messageID, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
					t.Fatal(e)
				}
			case "learning-off-after-ack":
				if _, e = held.ExecContext(ctx, "UPDATE scenes SET value=json_set(value,'$.learningEnabled',json('false')) WHERE id=?", scene.ID); e != nil {
					t.Fatal(e)
				}
			case "shutdown-held-sql":
				cancelled, cancelledDone := context.WithCancel(context.Background())
				cancelledDone()
				if e = engine.CloseAndWait(cancelled); !errors.Is(e, context.Canceled) {
					t.Fatalf("shutdown released physical SQL: %v", e)
				}
			}
			if e = held.Close(); e != nil {
				t.Fatal(e)
			}
		}
	}
	select {
	case <-turn.Done:
	case <-ctx.Done():
		t.Fatal(ctx.Err(), diagnostic.String())
	}
	result := turn.Wait()
	if !blocked && name != "failed" && name != "unknown" && result.Err != nil {
		t.Fatal(result, diagnostic.String())
	}
	if e = json.Unmarshal(call("inspect"), &inspection); e != nil {
		t.Fatal(e)
	}
	wantSent := 1
	if name == "real-recall" {
		wantSent = 0
	}
	if len(inspection.Sent) != wantSent || inspection.ModelCalls < 4 || len(inspection.Resolved) != 1 || inspection.Resolved[0].AssetID != assetID || inspection.Resolved[0].Type != "image" || inspection.Resolved[0].Bytes != len(image) {
		t.Fatalf("V1 full route not executed: %+v %+v", inspection, result)
	}
	if e = bridge.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = engine.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	var usage int
	if e = db.QueryRow("SELECT json_extract(value,'$.usageCount') FROM meme_assets WHERE id=?", assetID).Scan(&usage); e != nil {
		t.Fatal(e)
	}
	want := 1
	if name == "failed" || name == "unknown" || name == "recall-after-ack" || name == "learning-off-after-ack" || name == "real-recall" {
		want = 0
	}
	if usage != want {
		t.Fatalf("actual outgoing usage=%d want%d; result=%+v", usage, want, result)
	}
}
