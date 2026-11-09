package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

type applicationLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *applicationLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *applicationLogBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func applicationOptions(t *testing.T) Options {
	t.Helper()
	collator, e := topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("native ICU profile required for actual application graph")
	}
	if e != nil {
		t.Fatal(e)
	}
	if e = collator.Close(); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"typing-delay", "web-read", "web-search", "snowluma"} {
		artifact, e := plugins.BuiltinArtifact(id)
		if e != nil || len(artifact) == 0 {
			t.Fatalf("native application profile requires built WASM %s: %v", id, e)
		}
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugins")
	if err = os.Mkdir(pluginRoot, 0700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/application.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(pluginRoot, "application.mjs"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("HE_SOURCE_ROOT")
	if source == "" {
		source, err = filepath.Abs("../../..")
		if err != nil {
			t.Fatal(err)
		}
	}
	extension := filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so")
	if _, err = os.Stat(extension); err != nil {
		t.Fatal("genuine sqlite-vec required", err)
	}
	diagnostics := new(applicationLogBuffer)
	logs := observability.New(diagnostics, io.Discard)
	t.Cleanup(func() {
		if err := logs.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if t.Failed() {
			t.Log("synthetic application diagnostics:", diagnostics.snapshot())
		}
	})
	return Options{Installation: installation.Options{Database: filepath.Join(root, "data/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), VectorExtension: extension, SourceTimezone: "UTC"}, ImageLibrary: os.Getenv("HE_IMAGE_LIBRARY"), Locale: "en-US", PluginRoot: pluginRoot, Node: node, AdminToken: "synthetic-token", Entries: []string{"application.mjs"}, Diagnostics: diagnostics, Logs: logs}
}
func appRequest(t *testing.T, app *Application, method, path string, body any) any {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer synthetic-token")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	value, err := jsjson.Parse(response.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func field(v any, key string) any {
	o, _ := v.(*jsjson.Object)
	if o == nil {
		return nil
	}
	x, _ := o.Get(key)
	return x
}
func TestInstalledApplicationConfigurationIngressShutdownAndRestart(t *testing.T) {
	options := applicationOptions(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	input["debugModelIO"] = true
	// Source promptWindowAuthority separately invalidates a live turn on model
	// writes. Disable that source option here to isolate Router snapshot lifetime.
	input["promptContextWindowEnabled"] = false
	input["debounceMs"] = float64(0)
	input["groupTurnCooldownMs"] = float64(0)
	appRequest(t, app, http.MethodPut, "/api/settings", input)
	models := json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"runtime-fixture:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`)
	appRequest(t, app, http.MethodPut, "/api/models", models)
	marker := filepath.Join(options.PluginRoot, "received.marker")
	arrived, resume := filepath.Join(options.PluginRoot, "planner.arrived"), filepath.Join(options.PluginRoot, "planner.resume")
	t.Cleanup(func() { _ = os.WriteFile(resume, []byte("resume"), 0600) })
	appRequest(t, app, http.MethodPut, "/api/plugins/runtime-fixture/config", map[string]any{"memoryMode": true, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "receivedMarker": marker, "plannerArrived": arrived, "plannerResume": resume})
	appRequest(t, app, http.MethodPut, "/api/plugins/runtime-fixture/enabled", map[string]any{"enabled": true})
	// Source ingress defaults to participation disabled. Enable the discovered
	// scene through the actual administration boundary before new platform input.
	inspectedScene := appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/inspect", map[string]any{})
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
	savedMemory, err := app.Semantic.Memory.Propose(ctx, proposal, candidate, memoryservice.ProposalOptions{})
	if err != nil || savedMemory.Status != "saved" {
		t.Fatalf("real memory seed: %+v %v", savedMemory, err)
	}
	selectedMemory := make(chan int, 4)
	app.Engine.Runtime.AddExtension(&botruntime.Extension{ContextSelected: func(c *botruntime.TurnContext, _ *jsjson.Object) error {
		if !app.IdentityExposure.HasExposure(c) {
			return errors.New("identity_receipt_not_delivered")
		}
		selectedMemory <- len(app.MemoryExposure.ReadExposed(c))
		return nil
	}})
	experienceView := appRequest(t, app, http.MethodGet, "/api/memory-experience?sceneId="+url.QueryEscape(sceneID), nil)
	if field(experienceView, "sceneId") != sceneID {
		t.Fatal("Experience response bound to wrong scene", experienceView)
	}
	if _, ok := field(experienceView, "items").([]any); !ok {
		t.Fatal("missing actual Experience items", experienceView)
	}

	appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/new-source", map[string]any{})
	for {
		if _, e := os.Stat(arrived); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("planner did not enter captured router")
		case <-time.After(time.Millisecond):
		}
	}
	// HTTP commit happens while the existing turn owns its original router.
	updated := json.RawMessage(strings.ReplaceAll(string(models), `"m"`, `"m2"`))
	appRequest(t, app, http.MethodPut, "/api/models", updated)
	if err = os.WriteFile(resume, []byte("resume"), 0600); err != nil {
		t.Fatal(err)
	}

	var inspected any
	for {
		inspected = appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/inspect", map[string]any{})
		sent, _ := field(inspected, "sent").([]any)
		if len(sent) == 1 {
			break
		}
		select {
		case failure := <-turnFailures:
			t.Fatalf("synthetic actual turn failure: %+v; inspect=%v", failure, inspected)
		case <-ctx.Done():
			t.Fatal("native send not completed", inspected)
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case count := <-selectedMemory:
		if count < 1 {
			t.Fatal("memory tool did not deliver selected original record")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if field(inspected, "modelCalls").(float64) < 3 {
		t.Fatal("did not run native planner/replyer/tool", inspected)
	}
	for _, id := range field(inspected, "modelIDs").([]any) {
		if id != "m" {
			t.Fatal("midturn PUT replaced captured router", inspected)
		}
	}
	for app.Engine.Runtime.ActiveTasks() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("first turn did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/new-source", map[string]any{})
	for {
		inspected = appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/inspect", map[string]any{})
		if len(field(inspected, "sent").([]any)) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("new turn did not send", inspected)
		case <-time.After(time.Millisecond):
		}
	}
	ids := field(inspected, "modelIDs").([]any)
	if ids[len(ids)-1] != "m2" {
		t.Fatal("new turn failed to adopt committed models", inspected)
	}
	calls := appRequest(t, app, http.MethodGet, "/api/model-calls", nil)
	// Query exposes actual metadata rows from the newly PUT model configuration.
	encoded, _ := jsjson.Marshal(calls)
	if !strings.Contains(string(encoded), `"modelId":"m"`) {
		t.Fatal("model calls not bound", string(encoded))
	}
	appRequest(t, app, http.MethodGet, "/api/diagnostics", nil)
	sourceID, _ := field(inspected, "source").(string)
	if sourceID == "" {
		t.Fatal("missing actual source ID")
	}
	if err = app.Engine.Runtime.Recall(ctx, sceneID, sourceID); err != nil {
		t.Fatal(err)
	}
	// Independent read of the committed native tombstone proves the Experience
	// callback joined the real source recall writer instead of a no-op callback.
	witness, err := sql.Open("sqlite3", options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	var tombstones int
	err = witness.QueryRowContext(ctx, "SELECT count(*) FROM memory_experience_tombstones WHERE key=?", experienceservice.SourceKey(sceneID, sourceID)).Scan(&tombstones)
	closeErr := witness.Close()
	if err != nil || closeErr != nil || tombstones != 1 {
		t.Fatalf("Experience recall not committed: %d %v %v", tombstones, err, closeErr)
	}

	if err = app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("plugin process survived shutdown")
	}
	app = nil
	// Source adapter deliberately has no fresh platform message on its next start.
	// A replay here could only come from application/learning startup reading DB.
	app, err = Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	inspected = appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/inspect", map[string]any{})
	if field(inspected, "modelCalls") != float64(0) || len(field(inspected, "sent").([]any)) != 0 {
		t.Fatal("startup replayed persisted message", inspected)
	}
	appRequest(t, app, http.MethodPut, "/api/plugins/runtime-fixture/enabled", map[string]any{"enabled": false})
	if err = app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationStartupFailureReleasesInstallation(t *testing.T) {
	options := applicationOptions(t)
	// Failure after genuine enrollment but before any producer starts must release
	// installation locks. Reopening the exact installation proves actual cleanup.
	broken := options
	broken.Locale = ""
	if partial, err := Open(context.Background(), broken); err == nil || partial != nil {
		t.Fatalf("expected cleaned startup failure: %v %v", partial, err)
	}
	app, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal("installation left unusable after failed startup", err)
	}
	if err = app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
