package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

func TestEmbeddingIndexActionsInstalledWorker(t *testing.T) {
	type row map[string]any
	var source struct {
		Results []struct {
			Name     string
			Outcomes []struct {
				Label  string
				Status int
				Body   row
			}
		}
	}
	raw, err := os.ReadFile("testdata/embedding-rebuild-http-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &source); err != nil || len(source.Results) != 7 {
		t.Fatalf("source fixture: %v", err)
	}
	sourceBody := func(scenario, label string) row {
		t.Helper()
		for _, test := range source.Results {
			if test.Name == scenario {
				for _, phase := range test.Outcomes {
					if phase.Label == label {
						return phase.Body
					}
				}
			}
		}
		t.Fatalf("no source phase %s/%s", scenario, label)
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	block := make(chan struct{}, 8)
	var hold atomic.Bool
	calls := make(chan string, 16)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string
			Model string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.URL.Path != "/v1/embeddings" || len(request.Input) != 1 {
			t.Errorf("isolated embedding request: %s %+v %v", r.URL.Path, request, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls <- request.Model + ":" + request.Input[0]
		if hold.Load() {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer provider.Close()
	options := applicationOptions(t)
	options.Entries = []string{}
	options.HTTP = provider.Client()
	a, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(block)
		wait, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := a.Shutdown(wait); err != nil {
			t.Error(err)
		}
	}()
	db := a.models.Client.Budgets.(*providertransport.BudgetService).DB
	appRequest(t, a, "PUT", "/api/scenes/s", json.RawMessage(`{"id":"s","kind":"group","adapterId":"fixture","accountId":"fixture","platformSceneId":"s","name":"Synthetic","participationEnabled":false,"learningEnabled":false,"proactiveEnabled":false}`))
	configure := func(name string, width bool) {
		t.Helper()
		dim := ""
		if width {
			dim = `,"embeddingDimensions":2`
		}
		appRequest(t, a, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(
			`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":%q,"capabilities":["embedding"],"embeddingBatchSize":1%s}],"routes":[{"role":"embedding","orderedModelIds":["m"],"timeoutMs":10000}]}`,
			provider.URL+"/v1", name, dim)))
	}
	httpCall := func(action string, in any, want int) row {
		t.Helper()
		method, path := "GET", "/api/embedding-index"
		var request string
		if action != "" {
			body, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			method, path, request = "POST", path+"/"+action, string(body)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(request)).WithContext(ctx)
		if action != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		sourceFixtureAuth(req, true)
		out := httptest.NewRecorder()
		a.ServeHTTP(out, req)
		if out.Code != want || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s HTTP %d: %s", action, out.Code, out.Body)
		}
		var value row
		if err := json.Unmarshal(out.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	getStatus := func() row { return httpCall("", nil, 200) }
	object := func(in any) row {
		if r, ok := in.(map[string]any); ok {
			return r
		}
		return nil
	}
	start := func() row {
		before := getStatus()
		id := object(before["job"])["id"]
		revision := object(before["selected"])["encoderRevision"]
		return httpCall("start", row{"expectedJobId": id, "expectedEncoderRevision": revision}, 200)
	}
	compare := func(got row, scenario, label string) {
		t.Helper()
		want := sourceBody(scenario, label)
		job, sourceJob := object(got["job"]), object(want["job"])
		if got["state"] != want["state"] || job["state"] != sourceJob["state"] || job["error"] != sourceJob["error"] ||
			job["requiresRetryConfirmation"] != sourceJob["uncertainty"] || job["processed"] != sourceJob["processed"] ||
			job["total"] != sourceJob["total"] {
			t.Fatalf("%s/%s status mismatch: %v source %v", scenario, label, got, want)
		}
	}
	configure("native-width", false)
	compare(start(), "empty-and-unknown-width", "unknown-width-paused-no-probe")
	if len(calls) != 0 {
		t.Fatal("empty unknown width reached provider")
	}
	configure("declared-width", true)
	actual := start()
	compare(actual, "empty-and-unknown-width", "known-empty-completed-no-probe")
	old := object(actual["active"])["generationId"]
	if old == nil || len(calls) != 0 {
		t.Fatal("empty declared width did not atomically publish")
	}
	for _, text := range []string{"older", "newer"} {
		seed := new(jsjson.Object)
		_ = seed.Set("scope", memoryPatchObject(t, []byte(`{"kind":"scene","sceneId":"s"}`)))
		_ = seed.Set("kind", "fact")
		_ = seed.Set("content", text)
		if _, err := a.Semantic.Memory.Create(ctx, seed); err != nil {
			t.Fatal(err)
		}
	}
	configure("two", true)
	hold.Store(true)
	// The action must return after the first durable uncertainty reservation.
	// The fake provider gate is still closed and can complete only below.
	admitted := start()
	compare(admitted, "atomic-cutover-and-prune", "replacement-start")
	if object(admitted["active"])["generationId"] != old {
		t.Fatal("new generation published before any result")
	}
	call := func(want string) {
		t.Helper()
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("provider got %s want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal("provider call not received")
		}
	}
	call("two:newer")
	block <- struct{}{}
	call("two:older")
	staged := getStatus()
	compare(staged, "atomic-cutover-and-prune", "old-active-with-staging-checkpoint")
	if object(staged["active"])["generationId"] != old {
		t.Fatal("old active pointer changed while staging")
	}
	block <- struct{}{}
	waitTerminal := func(state string) row {
		t.Helper()
		for ctx.Err() == nil {
			status := getStatus()
			if object(status["job"])["state"] == state {
				return status
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("worker did not settle at %s", state)
		return nil
	}
	actual = waitTerminal("completed")
	compare(actual, "atomic-cutover-and-prune", "replacement-committed")
	if object(actual["active"])["generationId"] == old {
		t.Fatal("successful completion retained old active pointer")
	}
	var previous string
	if err := db.QueryRowContext(ctx, `SELECT json_extract(s.value,'$.generation') FROM vector_spaces s JOIN config c ON c.key='embedding.previousSpace' AND s.id=json_extract(c.value,'$')`).Scan(&previous); err != nil || previous != old {
		t.Fatalf("wrong prior generation %s: %v", previous, err)
	}
	// A later held action can be paused via the same HTTP owner, without writing
	// the vector or losing the already published generation.
	configure("three", true)
	third := start()
	call("three:newer")
	id := object(third["job"])["id"]
	paused := httpCall("pause", row{"expectedJobId": id}, 200)
	if object(paused["job"])["state"] != "paused" || object(paused["job"])["requiresRetryConfirmation"] != true {
		t.Fatal("pause lost in-flight uncertainty")
	}
	block <- struct{}{}
	httpCall("resume", row{"expectedJobId": id, "confirmUnknownRequest": false}, 400)
	httpCall("cancel", row{"expectedJobId": id}, 200)
}
