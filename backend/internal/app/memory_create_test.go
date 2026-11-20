package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

func TestMemoryCreateActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/memory-create-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node  string
		Cases []struct {
			Name, Raw, ContentType string
			Authorized             bool
			Status                 int
			Body                   map[string]any
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" {
		t.Fatalf("source fixture: %s %v", fixture.Node, err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	created := 0
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/memories", strings.NewReader(tc.Raw))
			req.Header.Set("Content-Type", tc.ContentType)
			if tc.Authorized {
				req.Header.Set("Authorization", "Bearer synthetic-token")
			}
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || res.Code != tc.Status {
				t.Fatalf("got %d %s; want %d %+v: %v", res.Code, res.Body, tc.Status, tc.Body, err)
			}
			if tc.Status == 200 {
				created++
				id, _ := body["id"].(string)
				learned, _ := body["learnedAt"].(string)
				if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(id) {
					t.Fatal("missing persisted identity", id)
				}
				if _, err := time.Parse(time.RFC3339Nano, learned); err != nil {
					t.Fatal(err)
				}
				var persisted string
				var version, epoch int
				if err := db.QueryRow("SELECT m.value,s.version,s.deletion_epoch FROM memories m JOIN memory_assertion_state s ON s.assertion_id=m.id WHERE m.id=?", id).Scan(&persisted, &version, &epoch); err != nil {
					t.Fatal(err)
				}
				var row map[string]any
				if err := json.Unmarshal([]byte(persisted), &row); err != nil {
					t.Fatal(err)
				}
				row["revision"] = map[string]any{"version": float64(version), "deletionEpoch": float64(epoch)}
				if !reflect.DeepEqual(body, row) {
					t.Fatalf("response diverges from committed row: %+v != %+v", body, row)
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
	ctx, release := context.WithTimeout(t.Context(), 10*time.Second)
	defer release()
	if err := app.requests.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	var records, vectors int
	if err := db.QueryRow("SELECT (SELECT count(*) FROM memories), (SELECT count(*) FROM memory_vectors)").Scan(&records, &vectors); err != nil {
		t.Fatal(err)
	}
	if created != 3 || records != created || vectors != 0 {
		t.Fatalf("disabled index or duplicate write mismatch: created=%d persisted=%d vectors=%d", created, records, vectors)
	}
}

func TestMemoryCreateIndexesAfterCommitWithoutBlockingResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	calls := make(chan string, 8)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) != 1 || r.URL.Path != "/v1/embeddings" {
			t.Errorf("wrong isolated index request: %s %+v %v", r.URL.Path, request, err)
			w.WriteHeader(400)
			return
		}
		calls <- request.Input[0]
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer provider.Close()
	defer unblock()
	options := applicationOptions(t)
	options.Entries = []string{}
	options.HTTP = provider.Client()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		unblock()
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	db := app.models.Client.Budgets.(*providertransport.BudgetService).DB
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"encoder","displayName":"Synthetic Encoder","providerId":"p","apiModelName":"synthetic","capabilities":["embedding"],"embeddingDimensions":2}],"routes":[{"role":"embedding","orderedModelIds":["encoder"],"timeoutMs":10000}]}`, provider.URL+"/v1")))
	appRequest(t, app, "PUT", "/api/scenes/blocked", json.RawMessage(`{"id":"blocked","kind":"group","adapterId":"synthetic","accountId":"bot","platformSceneId":"blocked","name":"Synthetic","participationEnabled":false,"learningEnabled":false,"proactiveEnabled":false,"memoryReadEnabled":false}`))
	start := func(body string) <-chan *httptest.ResponseRecorder {
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			request := httptest.NewRequest("POST", "/api/memories", bytes.NewBufferString(body)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer synthetic-token")
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			result <- response
		}()
		return result
	}
	const public = `{"scope":{"kind":"sharedPublicKnowledge"},"content":" synthetic index entry ","kind":"fact"}`
	pending := start(public)
	select {
	case input := <-calls:
		if input != "synthetic index entry" {
			t.Fatal(input)
		}
	case <-ctx.Done():
		t.Fatal("index owner did not call provider", ctx.Err())
	}
	select {
	case response := <-pending:
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body)
		}
	case <-ctx.Done():
		t.Fatal("committed create response waited for embedding", ctx.Err())
	}
	var stored int
	if err := db.QueryRow("SELECT count(*) FROM memories WHERE content='synthetic index entry'").Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("model started before committed record: %d %v", stored, err)
	}
	select {
	case response := <-start(public):
		if response.Code != 503 {
			t.Fatal("duplicate within the same scope was accepted", response.Code, response.Body)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case response := <-start(`{"scope":{"kind":"scene","sceneId":"blocked","personId":"synthetic"},"content":"private unindexable entry","kind":"fact"}`):
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := app.requests.Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("blocked embedding was not tracked through shutdown lifetime", err)
	}
	unblock()
	if err = app.requests.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-calls:
		t.Fatalf("duplicate or private embedding leaked: %q", input)
	default:
	}
	var vectors int
	if err := db.QueryRow("SELECT count(*) FROM memory_vectors WHERE memory_id IN (SELECT id FROM memories WHERE content='synthetic index entry')").Scan(&vectors); err != nil || vectors != 1 {
		t.Fatalf("one public index publication required: %d %v", vectors, err)
	}
}
