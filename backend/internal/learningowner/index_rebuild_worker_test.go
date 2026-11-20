package learningowner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func rebuildTestRouter(o *Owner, sched *scheduler.Scheduler) indexRouterBuilder {
	return func(_ context.Context, cfg *Object) (*providertransport.NativeModels, error) {
		selected, err := selectEncoder(cfg)
		if err != nil {
			return nil, err
		}
		noRetry := float64(0)
		policy, err := modelservice.New(modelservice.Config{
			Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &noRetry}},
			Models:    []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: selected.model.APIModelName, Capabilities: []string{"embedding"}}},
			Routes:    []modelservice.Route{{Role: "embedding", OrderedModelIDs: []string{"m"}}},
		}, modelservice.Options{Scheduler: sched})
		if err != nil {
			return nil, err
		}
		provider := selected.provider
		pinned := indexObject(arr(get(cfg, "providers"))[0])
		provider.CredentialConfigured = get(pinned, "credentialConfigured") == true
		provider.CredentialEnv = str(pinned, "credentialEnv")
		return &providertransport.NativeModels{Policy: policy, Client: o.d.Models.Client, Providers: map[string]providertransport.Provider{"p": provider}}, nil
	}
}

func setRebuildTestModels(t *testing.T, o *Owner, f scopedFixture, name string, width bool) indexState {
	t.Helper()
	if _, err := credentials.NewStore(t.Context(), f.main, f.recovery, ""); err != nil {
		t.Fatal(err)
	}
	provider := o.d.Models.Providers["p"]
	model := obj("id", "m", "displayName", "Synthetic", "providerId", "p", "apiModelName", name,
		"capabilities", []any{"embedding"}, "embeddingBatchSize", float64(1))
	if width {
		_ = model.Set("embeddingDimensions", float64(2))
	}
	config := obj("providers", []any{obj("id", "p", "name", "Synthetic", "protocol", "openai-chat", "baseUrl", provider.BaseURL)},
		"models", []any{model}, "routes", []any{obj("role", "embedding", "orderedModelIds", []any{"m"}, "timeoutMs", float64(5000))})
	raw, err := jsjson.Stringify(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.main.Exec("UPDATE config SET value=? WHERE key='models'", raw); err != nil {
		t.Fatal(err)
	}
	state, err := o.indexState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func awaitRebuildTestTask(t *testing.T, o *Owner) {
	t.Helper()
	o.rebuild.mu.Lock()
	task := o.rebuild.task
	o.rebuild.mu.Unlock()
	if task == nil {
		return
	}
	select {
	case <-task.done:
		if task.err != nil {
			t.Fatal(task.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rebuild did not settle")
	}
}

func mustRebuildTestState(t *testing.T, o *Owner) indexState {
	t.Helper()
	state, err := o.indexState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func startRebuildTest(t *testing.T, o *Owner, builder indexRouterBuilder, resume, confirm bool) *Object {
	t.Helper()
	state := mustRebuildTestState(t, o)
	var expected *string
	if state.job != nil {
		id := str(state.job, "id")
		expected = &id
	}
	job, err := o.dispatchIndexWork(t.Context(), expected, state.selected.revision, resume, confirm, builder)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func seedRebuildTestMemory(t *testing.T, o *Owner, text string) *Object {
	t.Helper()
	row, err := o.d.Memory.Create(t.Context(), obj("scope", obj("kind", "scene", "sceneId", "s"), "kind", "fact", "content", text))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestIndexRebuildWorkerSourceFixturePinned(t *testing.T) {
	path := filepath.Join("../app/testdata", "embedding-rebuild-http-source.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node         string
		SourceHashes map[string]string
		Results      []json.RawMessage
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Results) != 7 {
		t.Fatalf("source oracle %v", err)
	}
	for path, digest := range fixture.SourceHashes {
		raw, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != digest {
			t.Fatalf("source drift %s: %v", path, err)
		}
	}
}

func TestIndexRebuildWorkerAtomicCutoverAndPrune(t *testing.T) {
	calls := make(chan []string, 12)
	var hold atomic.Bool
	release := make(chan struct{}, 12)
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls <- req.Input
		if hold.Load() {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	})
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := o.closeIndexWork(ctx); err != nil {
			t.Error(err)
		}
	}()
	builder := rebuildTestRouter(o, scheduler.NewModelScheduler())
	setRebuildTestModels(t, o, f, "native", false)
	startRebuildTest(t, o, builder, false, false)
	awaitRebuildTestTask(t, o)
	unknown := mustRebuildTestState(t, o)
	if str(unknown.job, "error") != "embedding_empty_native_dimension_unknown" || unknown.active != nil || len(calls) != 0 {
		t.Fatalf("empty unknown: %+v", unknown)
	}
	setRebuildTestModels(t, o, f, "one", true)
	startRebuildTest(t, o, builder, false, false)
	awaitRebuildTestTask(t, o)
	old := mustRebuildTestState(t, o).active
	if old == nil || len(calls) != 0 {
		t.Fatal("empty known did not publish without a provider")
	}
	seedRebuildTestMemory(t, o, "older")
	seedRebuildTestMemory(t, o, "newer")
	setRebuildTestModels(t, o, f, "two", true)
	hold.Store(true)
	replacement := startRebuildTest(t, o, builder, false, false)
	nextCall := func(want string) {
		t.Helper()
		select {
		case got := <-calls:
			if len(got) != 1 || got[0] != want {
				t.Fatalf("provider got %v want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			state := mustRebuildTestState(t, o)
			t.Fatalf("provider not entered job=%v", state.job)
		}
	}
	nextCall("newer")
	if state := mustRebuildTestState(t, o); str(state.active, "generationId") != str(old, "generationId") || get(state.job, "requiresRetryConfirmation") != true {
		t.Fatal("before dispatch staging changed the active generation")
	}
	release <- struct{}{}
	nextCall("older")
	if state := mustRebuildTestState(t, o); str(state.active, "generationId") != str(old, "generationId") || get(state.job, "processed") != float64(1) {
		t.Fatal("staging checkpoint not committed under old active")
	}
	release <- struct{}{}
	awaitRebuildTestTask(t, o)
	if state := mustRebuildTestState(t, o); str(state.active, "generationId") != str(replacement, "id") || str(state.job, "state") != "completed" {
		t.Fatal("complete coverage did not atomically publish")
	}
	hold.Store(false)
	setRebuildTestModels(t, o, f, "three", true)
	startRebuildTest(t, o, builder, false, false)
	awaitRebuildTestTask(t, o)
	var removed, retained int
	if err := f.main.QueryRow("SELECT (SELECT count(*) FROM vector_spaces WHERE id=?),count(*) FROM vector_spaces", str(old, "spaceId")).Scan(&removed, &retained); err != nil || removed != 0 || retained != 2 {
		t.Fatalf("retention remove=%d keep=%d err=%v", removed, retained, err)
	}
	if indexed := indexCount(t, f); indexed != 4 {
		t.Fatalf("retained indexed vectors %d", indexed)
	}
	checkIndexIntegrity(t, f)
}

func TestIndexRebuildWorkerUnknownRetryAndHeldPause(t *testing.T) {
	const badShape = 1
	const knownHTTP = 2
	const holdProvider = 3
	var mode atomic.Int32
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch mode.Load() {
		case knownHTTP:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"synthetic"}}`)
			return
		case badShape:
			fmt.Fprint(w, `{"data":[]}`)
			return
		case holdProvider:
			entered <- struct{}{}
			<-release
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := o.closeIndexWork(ctx); err != nil {
			t.Error(err)
		}
	}()
	builder := rebuildTestRouter(o, scheduler.NewModelScheduler())
	setRebuildTestModels(t, o, f, "one", true)
	seedRebuildTestMemory(t, o, "synthetic")
	mode.Store(knownHTTP)
	startRebuildTest(t, o, builder, false, false)
	awaitRebuildTestTask(t, o)
	known := mustRebuildTestState(t, o)
	if str(known.job, "error") != "provider_http_400" || get(known.job, "requiresRetryConfirmation") != false {
		t.Fatalf("known %v", known.job)
	}
	mode.Store(badShape)
	startRebuildTest(t, o, builder, true, false)
	awaitRebuildTestTask(t, o)
	unknown := mustRebuildTestState(t, o)
	if str(unknown.job, "state") != "failed" || get(unknown.job, "requiresRetryConfirmation") != true {
		t.Fatalf("unknown %v", unknown.job)
	}
	id := str(unknown.job, "id")
	if _, err := o.dispatchIndexWork(t.Context(), &id, "", true, false, builder); err == nil || err.Error() != "embedding_retry_confirmation_required" {
		t.Fatalf("unconfirmed response %v", err)
	}
	mode.Store(holdProvider)
	startRebuildTest(t, o, builder, true, true)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed provider call missing")
	}
	if _, err := o.stopIndexWork(t.Context(), &id, false); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitRebuildTestTask(t, o)
	state := mustRebuildTestState(t, o)
	if str(state.job, "state") != "paused" || state.active != nil || calls.Load() != 3 || indexCount(t, f) != 0 {
		t.Fatalf("late result changed stopped job %v (%d calls)", state.job, calls.Load())
	}
}

func TestIndexRebuildWorkerRequalifiesEditedAndRecalledBatch(t *testing.T) {
	calls := make(chan []string, 4)
	release := make(chan struct{}, 4)
	o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls <- req.Input
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	})
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := o.closeIndexWork(ctx); err != nil {
			t.Error(err)
		}
	}()
	builder := rebuildTestRouter(o, scheduler.NewModelScheduler())
	setRebuildTestModels(t, o, f, "one", true)
	if _, err := o.d.Memory.Create(t.Context(), obj("scope", obj("kind", "scene", "sceneId", "s"),
		"kind", "fact", "content", "will be recalled", "sources", []any{obj("sceneId", "s", "messageId", "source", "senderId", "other")})); err != nil {
		t.Fatal(err)
	}
	edited := seedRebuildTestMemory(t, o, "old edit body")
	startRebuildTest(t, o, builder, false, false)
	nextCall := func(want string) {
		t.Helper()
		select {
		case got := <-calls:
			if len(got) != 1 || got[0] != want {
				t.Fatalf("provider got %v want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("provider never entered", mustRebuildTestState(t, o).job)
		}
	}
	nextCall("old edit body")
	if _, err := o.d.Memory.Update(t.Context(), str(edited, "id"), obj("content", "replacement edit body")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.d.Memory.Recall(t.Context(), "s", "source"); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	nextCall("replacement edit body")
	if indexCount(t, f) != 0 || get(mustRebuildTestState(t, o).job, "processed") != float64(0) {
		t.Fatal("stale or recalled vector survived fresh qualification")
	}
	release <- struct{}{}
	awaitRebuildTestTask(t, o)
	job := mustRebuildTestState(t, o).job
	if indexCount(t, f) != 1 || get(job, "total") != float64(1) || str(job, "state") != "completed" {
		t.Fatalf("replacement not committed: %v", job)
	}
}

func TestIndexRebuildWorkerCapturedCredentialRemoval(t *testing.T) {
	for _, withEnv := range []bool{false, true} {
		t.Run(fmt.Sprintf("env-%t", withEnv), func(t *testing.T) {
			var calls atomic.Int32
			o, f := ownerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !withEnv || r.Header.Get("Authorization") != "Bearer synthetic-env-only" {
					t.Error("unexpected credential-less or non-fallback provider request")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
			})
			setRebuildTestModels(t, o, f, "one", true)
			ctx := t.Context()
			if err := f.recovery.WithWriteSession(ctx, f.main, func(s *recovery.WriteSession) error {
				return s.EnsureSecurityStateSchema()
			}); err != nil {
				t.Fatal(err)
			}
			secret, err := credentials.NewStore(ctx, f.main, f.recovery, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := secret.Apply(ctx, "model", "p", &credentials.Mutation{Action: "set", Value: "synthetic-stored-only"}); err != nil {
				t.Fatal(err)
			}
			if withEnv {
				if _, err := f.main.ExecContext(ctx, "UPDATE config SET value=json_set(value,'$.providers[0].credentialEnv','SYNTHETIC_REBUILD_ENV') WHERE key='models'"); err != nil {
					t.Fatal(err)
				}
			}
			o.d.Models.Client.Credentials = providertransport.StoredCredentials{Store: secret, LookupEnv: func(name string) (string, bool) {
				return "synthetic-env-only", name == "SYNTHETIC_REBUILD_ENV"
			}}
			seedRebuildTestMemory(t, o, "protected record")
			proceed := make(chan struct{})
			entered := make(chan bool, 1)
			defer func() {
				select {
				case <-proceed:
				default:
					close(proceed)
				}
				wait, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := o.closeIndexWork(wait); err != nil {
					t.Error(err)
				}
			}()
			build := rebuildTestRouter(o, scheduler.NewModelScheduler())
			startRebuildTest(t, o, func(ctx context.Context, cfg *Object) (*providertransport.NativeModels, error) {
				pinned := indexObject(arr(get(cfg, "providers"))[0])
				entered <- get(pinned, "credentialConfigured") == true
				<-proceed
				return build(ctx, cfg)
			}, false, false)
			select {
			case captured := <-entered:
				if !captured {
					t.Fatal("job did not capture the configured credential bit")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pinned router builder never reached")
			}
			// The installed credential owner now removes the secret. The pinned
			// boolean must not turn a configured request into an anonymous call.
			if _, err := secret.Apply(ctx, "model", "p", &credentials.Mutation{Action: "clear"}); err != nil {
				t.Fatal(err)
			}
			close(proceed)
			awaitRebuildTestTask(t, o)
			state := mustRebuildTestState(t, o)
			if withEnv {
				if calls.Load() != 1 || str(state.job, "state") != "completed" || state.active == nil {
					t.Fatalf("legitimate fallback failed: calls=%d job=%v", calls.Load(), state.job)
				}
			} else if calls.Load() != 0 || str(state.job, "error") != "credential_not_configured" || state.active != nil {
				t.Fatalf("removed stored credential did not fail closed: calls=%d job=%v", calls.Load(), state.job)
			}
		})
	}
}
