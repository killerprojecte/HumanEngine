package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memeHTTPKeys struct{}

func (memeHTTPKeys) Resolve(context.Context, providertransport.Provider) (string, error) {
	return "synthetic-only", nil
}
func TestMemeNativeHTTPImportFilesAndCancelledAnalysis(t *testing.T) {
	server, db, engine := memeHTTP(t)
	ctx := context.Background()
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "s", Kind: "group", AdapterID: "a", AccountID: "account", PlatformSceneID: "synthetic"}, LearningEnabled: true}
	if _, e := db.Exec("INSERT INTO scenes VALUES('s',?)", `{"id":"s","kind":"group","adapterId":"a","accountId":"account","platformSceneId":"synthetic","learningEnabled":true,"participationEnabled":false}`); e != nil {
		t.Fatal(e)
	}
	message := memeObject("id", "m", "sceneId", "s", "senderId", "person", "sentAt", time.Now().UTC().Format(time.RFC3339Nano), "receivedAt", time.Now().UTC().Format(time.RFC3339Nano), "origin", "live", "status", "available", "parts", []any{memeObject("type", "text", "text", "Synthetic source")})
	if _, e := engine.Runtime.IngestScene(ctx, scene, message, false); e != nil {
		t.Fatal(e)
	}
	image := image.NewNRGBA(image.Rect(0, 0, 7, 5))
	image.SetNRGBA(1, 1, color.NRGBA{R: 150, G: 30, B: 100, A: 255})
	var pngBytes bytes.Buffer
	if e := png.Encode(&pngBytes, image); e != nil {
		t.Fatal(e)
	}
	request := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+fixtureToken)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	raw, _ := json.Marshal(map[string]any{"sceneId": "s", "messageId": "m", "base64": base64.StdEncoding.EncodeToString(pngBytes.Bytes())})
	w := request(ctx, "POST", "/api/memes/import", string(raw))
	if w.Code != 200 {
		root, release, rootErr := engine.RootContext(ctx)
		if rootErr == nil {
			imported, importErr := engine.Import(root, "s", "m", pngBytes.Bytes())
			t.Logf("direct import error=%v asset=%v", importErr, imported)
			release()
		} else {
			t.Log(rootErr)
		}
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	v, e := jsjson.Parse(w.Body.String())
	if e != nil {
		t.Fatal(e)
	}
	id := memeString(memeGet(v.(*jsjson.Object), "id"))
	if sources, ok := memeGet(v.(*jsjson.Object), "sources").([]any); !ok || len(sources) != 0 {
		t.Fatal("import must return sanitized sources")
	}
	w = request(ctx, "GET", "/api/memes/"+id+"/file", "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), pngBytes.Bytes()) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("file: %d %s", w.Code, w.Body.String())
	}
	w = request(ctx, "GET", "/api/memes/"+id+"/thumbnail", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/webp" || !bytes.HasPrefix(w.Body.Bytes(), []byte("RIFF")) {
		t.Fatalf("thumbnail: %d %s", w.Code, w.Body.String())
	}
	arrived, respond := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(respond) }) }
	defer release()
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		arrived <- struct{}{}
		select {
		case <-respond:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer func() { release(); endpoint.Close() }()
	zero, timeout := float64(0), float64(30000)
	stream := false
	engine.Models.Policy, e = modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"vision", "text"}, Streaming: &stream}}, Routes: []modelservice.Route{{Role: "vision", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	engine.Models.Providers = map[string]providertransport.Provider{"p": {ID: "p", Protocol: "openai-chat", BaseURL: endpoint.URL}}
	engine.Models.Client = &providertransport.Client{HTTP: endpoint.Client(), Credentials: memeHTTPKeys{}, Budgets: providertransport.ExplicitOff{}, Images: providertransport.NativeImageSerializer{Compressor: engine.Codec}}
	cancelled, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var analysis *httptest.ResponseRecorder
	go func() {
		defer close(done)
		analysis = request(cancelled, "POST", "/api/memes/"+id+"/analyze", `{"sceneId":"s"}`)
	}()
	select {
	case <-arrived:
	case <-done:
		t.Fatalf("analysis ended before provider: %d %s", analysis.Code, analysis.Body.String())
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("analysis did not reach native provider")
	}
	cancel()
	release()
	<-done
	asset, e := engine.Memes.AdminGet(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if memeGet(asset, "analysisState") != "cancelled" || memeGet(asset, "analyzed") == true || memeGet(asset, "shared") == true {
		t.Fatalf("cancelled root inference persisted: %s", string(mustMemeJSON(t, asset)))
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if _, e = db.Exec("UPDATE scenes SET value=json_set(value,'$.memoryReadEnabled',0) WHERE id='s'"); e != nil {
		t.Fatal(e)
	}
	w = request(ctx, "POST", "/api/memes/"+id+"/analyze", `{"sceneId":"s"}`)
	if w.Code != 503 || calls.Load() != 1 {
		t.Fatal("disabled model read reached provider", w.Code, calls.Load())
	}
}
func mustMemeJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := jsjson.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
