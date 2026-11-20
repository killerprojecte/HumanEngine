package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

func memoryPatchObject(t *testing.T, raw []byte) *jsjson.Object {
	t.Helper()
	result, err := jsjson.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result.(*jsjson.Object)
}

func TestMemoryPatchActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/memory-patch-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node          string
		Scene, Source json.RawMessage
		Seeds         map[string]json.RawMessage
		Cases         []struct {
			Name, Target, Raw string
			Authorized        *bool
			Recall            bool
			Status            int
			Body              map[string]any
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" {
		t.Fatalf("source fixture: %s %v", fixture.Node, err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	appRequest(t, app, "PUT", "/api/scenes/scene-a", fixture.Scene)
	if _, err := app.Engine.Runtime.Ingest(ctx, "scene-a", memoryPatchObject(t, fixture.Source), false); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{"missing": "synthetic-missing"}
	for _, target := range []string{"ordinary", "recalled"} {
		seed, err := app.Semantic.Memory.Create(ctx, memoryPatchObject(t, fixture.Seeds[target]))
		if err != nil {
			t.Fatal(err)
		}
		ids[target] = field(seed, "id").(string)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Recall {
				if err := app.Engine.Runtime.Recall(ctx, "scene-a", "source"); err != nil {
					t.Fatal(err)
				}
			}
			id := ids[tc.Target]
			req := httptest.NewRequest("PATCH", "/api/memories/"+url.PathEscape(id), strings.NewReader(tc.Raw))
			req.Header.Set("Content-Type", "application/json")
			if tc.Authorized == nil || *tc.Authorized {
				req.Header.Set("Authorization", "Bearer synthetic-token")
			}
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || res.Code != tc.Status {
				t.Fatalf("got %d %s; want %d %+v: %v", res.Code, res.Body, tc.Status, tc.Body, err)
			}
			if tc.Status == 200 {
				if body["id"] != id {
					t.Fatalf("acknowledged wrong assertion: %s", res.Body)
				}
				var version, epoch int
				if err := db.QueryRow("SELECT version,deletion_epoch FROM memory_assertion_state WHERE assertion_id=?", id).Scan(&version, &epoch); err != nil {
					t.Fatal(err)
				}
				actual := map[string]any{"version": float64(version), "deletionEpoch": float64(epoch)}
				if !reflect.DeepEqual(body["revision"], actual) {
					t.Fatalf("revision not committed: %s != %+v", res.Body, actual)
				}
				delete(body, "id")
				delete(body, "learnedAt")
			}
			delete(body, "requestId")
			if !reflect.DeepEqual(body, tc.Body) {
				t.Fatalf("got %+v want %+v", body, tc.Body)
			}
		})
	}
	var receipts int
	if err := db.QueryRow("SELECT count(*) FROM memory_command_receipts").Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("guarded replays/conflicts changed receipt count: %d %v", receipts, err)
	}
}

func TestMemoryPatchAcknowledgementRejectsSupersededBodyAtSameRevision(t *testing.T) {
	app, _ := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	m := appRequest(t, app, http.MethodPost, "/api/memories", json.RawMessage(`{"scope":{"kind":"sharedPublicKnowledge"},"content":"original","kind":"fact"}`))
	id := field(m, "id").(string)
	memory := app.Semantic.Memory
	// Inactive records are not retained as revisions in either implementation.
	ack, err := memory.UpdateForAcknowledgement(ctx, id, memoryPatchObject(t, []byte(`{"status":"invalidated"}`)), nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := memory.BindAdminRead(app.readPublisher)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	emit := func([]byte) error { published++; return nil }
	if err = reader.PublishUpdated(ctx, id, ack, emit); err != nil || published != 1 {
		t.Fatalf("fresh acknowledgement was not accepted: %d %v", published, err)
	}
	next, err := memory.UpdateForAcknowledgement(ctx, id, memoryPatchObject(t, []byte(`{"content":"superseding invalidated text"}`)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ack.State != next.State {
		t.Fatalf("fixture no longer exercises the matching-revision race: %+v != %+v", ack.State, next.State)
	}
	if err = reader.PublishUpdated(ctx, id, ack, emit); err == nil || published != 1 {
		t.Fatalf("stale acknowledgement was published after a same-revision write: %d %v", published, err)
	}
	if err = reader.PublishUpdated(context.Background(), id, next, emit); err != nil || published != 2 {
		t.Fatalf("fresh acknowledgement failed: %d %v", published, err)
	}
}

func TestMemoryPatchReplayIndexesButLateVectorCannotSurviveInvalidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	type call struct {
		content string
		release chan struct{}
	}
	calls := make(chan call, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Input) != 1 || r.URL.Path != "/v1/embeddings" {
			t.Errorf("wrong isolated PATCH index request: %s %+v %v", r.URL.Path, input, err)
			w.WriteHeader(400)
			return
		}
		request := call{input.Input[0], make(chan struct{})}
		calls <- request
		select {
		case <-request.release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer provider.Close()
	options := applicationOptions(t)
	options.Entries = []string{}
	options.HTTP = provider.Client()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		provider.CloseClientConnections()
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	db := app.models.Client.Budgets.(*providertransport.BudgetService).DB
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"encoder","displayName":"Synthetic Encoder","providerId":"p","apiModelName":"synthetic","capabilities":["embedding"],"embeddingDimensions":2}],"routes":[{"role":"embedding","orderedModelIds":["encoder"],"timeoutMs":10000}]}`, provider.URL+"/v1")))
	seed, err := app.Semantic.Memory.Create(ctx, memoryPatchObject(t, []byte(`{"scope":{"kind":"sharedPublicKnowledge"},"content":"original","kind":"fact"}`)))
	if err != nil {
		t.Fatal(err)
	}
	id := field(seed, "id").(string)
	path := "/api/memories/" + url.PathEscape(id)
	patch := func(raw string) *httptest.ResponseRecorder {
		t.Helper()
		out := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest("PATCH", path, strings.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer synthetic-token")
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			out <- res
		}()
		select {
		case res := <-out:
			if res.Code != 200 {
				t.Fatalf("PATCH not acknowledged: %d %s", res.Code, res.Body)
			}
			return res
		case <-ctx.Done():
			t.Fatal("PATCH waited on asynchronous indexing:", ctx.Err())
			return nil
		}
	}
	awaitModel := func() call {
		t.Helper()
		select {
		case request := <-calls:
			if request.content != "edited" {
				t.Fatalf("wrong committed text sent to encoder: %q", request.content)
			}
			return request
		case <-ctx.Done():
			t.Fatal("PATCH indexer never contacted encoder:", ctx.Err())
			return call{}
		}
	}
	vectorCount := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM memory_vectors WHERE memory_id=?", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	const action = `{"content":"edited","guard":{"expectedVersion":1,"expectedDeletionEpoch":0,"idempotencyKey":"synthetic-patch"}}`
	first := patch(action)
	close(awaitModel().release)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for vectorCount() == 0 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("first vector never became visible:", ctx.Err())
		}
	}
	replayed := patch(action)
	if first.Body.String() != replayed.Body.String() || vectorCount() != 1 {
		t.Fatal("replay mutated the acknowledged content/version or existing vector")
	}
	late := awaitModel() // Source starts another provider call even with an existing matching vector.
	patch(`{"status":"invalidated"}`)
	if vectorCount() != 0 {
		t.Fatal("status invalidation did not synchronously remove the old vector")
	}
	close(late.release)
	if err = app.requests.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-calls:
		t.Fatalf("invalidated PATCH made an additional provider call: %q", request.content)
	default:
	}
	if vectorCount() != 0 {
		t.Fatal("late replay index recreated an invalidated vector")
	}
}
