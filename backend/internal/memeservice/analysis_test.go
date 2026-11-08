package memeservice

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type syntheticCredentials struct{}

func (syntheticCredentials) Resolve(context.Context, providertransport.Provider) (string, error) {
	return "synthetic-fixture-only", nil
}
func nativeImageFixture(t *testing.T) (memeFixture, *Service, *imagecodec.Compressor, Context, *Object, []byte, *Object) {
	t.Helper()
	path := os.Getenv("HE_IMAGE_LIBRARY")
	if path == "" {
		t.Skip("HE_IMAGE_LIBRARY required for actual pixel/model lifecycle")
	}
	codec, e := imagecodec.New(path)
	if e != nil {
		t.Fatal(e)
	}
	f, s := newTestService(t)
	saveMemeScene(t, f, fields("id", "s", "adapterId", "a", "accountId", "b", "learningEnabled", true))
	source := fields("sceneId", "s", "messageId", "m", "senderId", "p")
	ev := copyObject(source)
	set(ev, "available", true)
	set(ev, "isBot", false)
	c := Context{SceneID: "s", SourceEvidence: []*Object{ev}}
	img := image.NewNRGBA(image.Rect(0, 0, 7, 5))
	img.SetNRGBA(1, 1, color.NRGBA{200, 30, 90, 255})
	var b bytes.Buffer
	if e = png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	a, e := s.Intake(context.Background(), c, b.Bytes(), source, true, codec)
	if e != nil {
		t.Fatal(e)
	}
	return f, s, codec, c, source, b.Bytes(), a
}
func modelFixture(t *testing.T, codec *imagecodec.Compressor, handler http.HandlerFunc) *providertransport.NativeModels {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	zero, timeout := float64(0), float64(30000)
	stream := false
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "vision"}, Streaming: &stream}}, Routes: []modelservice.Route{{Role: "vision", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return &providertransport.NativeModels{Policy: policy, Client: &providertransport.Client{HTTP: server.Client(), Credentials: syntheticCredentials{}, Budgets: providertransport.ExplicitOff{}, Images: providertransport.NativeImageSerializer{Compressor: codec}}, Providers: map[string]providertransport.Provider{"p": {ID: "p", Protocol: "openai-chat", BaseURL: server.URL}}}
}
func modelReply(w http.ResponseWriter, tags *Object) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": stringify(tags)}}}})
}
func waitUntil(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !f() {
		select {
		case <-deadline:
			t.Fatal("fixture did not reach expected barrier")
		case <-time.After(time.Millisecond):
		}
	}
}
func TestRealModelAnalysisManualRevisionAndCancellation(t *testing.T) {
	for _, action := range []string{"manual-tags", "admin-exclude", "recall"} {
		t.Run(action, func(t *testing.T) {
			_, s, codec, c, _, _, a := nativeImageFixture(t)
			id := str(get(a, "id"))
			arrived := make(chan map[string]any, 1)
			release := make(chan struct{})
			models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				arrived <- body
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				tags := emptyTags()
				set(tags, "intent", []any{"happy"})
				set(tags, "selection", fields("usable", true, "shareable", true, "confidence", 0.99, "containsPersonalData", false, "sourceSpecific", false, "containsRealPerson", false))
				modelReply(w, tags)
			})
			done := make(chan error, 1)
			go func() {
				_, e := s.Analyze(context.Background(), c, id, models, codec, AnalyzeOptions{Selection: &SelectionContext{Persona: "Synthetic", SourceContextKnown: true}})
				done <- e
			}()
			select {
			case body := <-arrived:
				if len(body["messages"].([]any)) != 2 {
					t.Fatal("prompt malformed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no native provider request")
			}
			switch action {
			case "manual-tags":
				tags := emptyTags()
				set(tags, "intent", []any{"manual"})
				if _, e := s.Update(context.Background(), id, fields("tags", tags)); e != nil {
					t.Fatal(e)
				}
			case "admin-exclude":
				if _, e := s.AdminSetIncluded(context.Background(), id, false); e != nil {
					t.Fatal(e)
				}
			case "recall":
				if _, e := s.Recall(context.Background(), "s", "m"); e != nil {
					t.Fatal(e)
				}
			}
			close(release)
			e := <-done
			if action == "manual-tags" && e != nil {
				t.Fatal(e)
			}
			if action != "manual-tags" && (e == nil || !strings.Contains(e.Error(), "analysis_cancelled")) {
				t.Fatalf("cancel result %v", e)
			}
			current, e := s.AdminGet(context.Background(), id)
			if e != nil {
				t.Fatal(e)
			}
			if action == "manual-tags" && stringify(get(obj(get(current, "tags")), "intent")) != `["manual"]` {
				t.Fatal("manual tags overwritten")
			}
			if action != "manual-tags" && (yes(get(current, "available")) || yes(get(current, "shared"))) {
				t.Fatal("cancelled inference promoted asset")
			}
		})
	}
}
func TestObservationConsumersShareNativeModelAndCancelIndependently(t *testing.T) {
	f, s, codec, c, source, b, _ := nativeImageFixture(t)
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		modelReply(w, emptyTags())
	})
	rootA, endA, err := f.authority.OperationContext(context.Background(), f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer endA()
	rootB, endB, err := f.authority.OperationContext(context.Background(), f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer endB()
	first, cancel := context.WithCancel(rootA)
	d1 := make(chan error, 1)
	d2 := make(chan error, 1)
	go func() { _, e := s.ObserveBytes(first, c, b, source, models, codec, ObserveOptions{}); d1 <- e }()
	<-arrived
	go func() {
		_, e := s.ObserveBytes(rootB, c, b, source, models, codec, ObserveOptions{})
		d2 <- e
	}()
	waitUntil(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, o := range s.observations {
			if o.consumers == 2 {
				return true
			}
		}
		return false
	})
	cancel()
	if e := <-d1; e == nil || e.Error() != "analysis_cancelled" {
		t.Fatal(e)
	}
	endA() // The remaining consumer must not inherit A's expired root.
	close(release)
	if e := <-d2; e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate observation provider calls", calls.Load())
	}
}
func TestThumbnailQueueDeduplicatesAndDeleteInvalidates(t *testing.T) {
	f, s, codec, _, _, _, a := nativeImageFixture(t)
	id := str(get(a, "id"))
	release1, release2, release3 := s.thumbnailSlot(), s.thumbnailSlot(), s.thumbnailSlot()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, e := s.Thumbnail(context.Background(), id, codec); done <- e }()
	}
	waitUntil(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.thumbnailTasks) == 1 && len(s.thumbnailWaiters) == 1
	})
	operation, release, e := f.authority.OperationContext(context.Background(), f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if ok, e := s.AdminDelete(operation, id); e != nil || !ok {
		t.Fatal(ok, e)
	}
	release1()
	release2()
	release3()
	for i := 0; i < 2; i++ {
		if e := <-done; e == nil || e.Error() != "meme_not_found" {
			t.Fatal(e)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.thumbnails) != 0 || len(s.thumbnailTasks) != 0 || s.thumbnailActive != 0 || len(s.writes) != 0 {
		t.Fatal("async state leaked after delete")
	}
}

func TestAnalysisCancellationWhileFinalWriterQueuedCannotPublish(t *testing.T) {
	f, s, codec, c, _, _, asset := nativeImageFixture(t)
	id := str(get(asset, "id"))
	arrived := make(chan struct{})
	providerResume := make(chan struct{})
	var providerOnce sync.Once
	releaseProvider := func() { providerOnce.Do(func() { close(providerResume) }) }
	defer releaseProvider()
	models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-providerResume
		tags := emptyTags()
		set(tags, "intent", []any{"must not publish"})
		set(tags, "selection", fields("usable", true, "shareable", true, "confidence", 0.99, "containsPersonalData", false, "sourceSpecific", false, "containsRealPerson", false))
		modelReply(w, tags)
	})
	signal, cancel := context.WithCancel(context.Background())
	defer cancel()
	analysisDone := make(chan error, 1)
	go func() {
		_, e := s.Analyze(signal, c, id, models, codec, AnalyzeOptions{Selection: &SelectionContext{Persona: "Synthetic", SourceContextKnown: true}})
		analysisDone <- e
	}()
	<-arrived
	writerHeld := make(chan struct{})
	writerResume := make(chan struct{})
	var writerOnce sync.Once
	releaseWriter := func() { writerOnce.Do(func() { close(writerResume) }) }
	defer releaseWriter()
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- f.authority.WithWriteSession(context.Background(), f.main, func(ws *recovery.WriteSession) error { close(writerHeld); <-writerResume; return nil })
	}()
	<-writerHeld
	before := f.main.Stats().WaitCount
	releaseProvider()
	// The real NativeModels response has settled and the analysis has reached
	// its final main.Conn acquisition. No provider stub or scheduling sleep.
	waitUntil(t, func() bool { return f.main.Stats().WaitCount > before })
	cancel()
	releaseWriter()
	if e := <-writerDone; e != nil {
		t.Fatal(e)
	}
	if e := <-analysisDone; e == nil || e.Error() != "analysis_cancelled" {
		t.Fatalf("queued cancellation: %v", e)
	}
	current, e := s.AdminGet(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	if yes(get(current, "analyzed")) || yes(get(current, "available")) || yes(get(current, "shared")) || yes(get(current, "privacyReviewed")) || stringify(get(current, "tags")) != stringify(emptyTags()) {
		t.Fatalf("cancelled inference was committed: %s", stringify(current))
	}
	if get(current, "analysisState") != "cancelled" {
		t.Fatal("terminal cancellation missing", stringify(current))
	}
}

func TestObservationTaskReleasesAfterSuccessFailureAndAllConsumersCancel(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel-all"} {
		t.Run(mode, func(t *testing.T) {
			f, s, codec, c, source, b, original := nativeImageFixture(t)
			arrived, respond := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(respond) }) }
			defer unblock()
			models := modelFixture(t, codec, func(w http.ResponseWriter, r *http.Request) {
				arrived <- struct{}{}
				select {
				case <-respond:
				case <-r.Context().Done():
					return
				}
				if mode == "failure" {
					w.WriteHeader(400)
					return
				}
				modelReply(w, emptyTags())
			})
			root, endRoot, err := f.authority.OperationContext(context.Background(), f.main)
			if err != nil {
				t.Fatal(err)
			}
			defer endRoot()
			ctx, cancel := context.WithCancel(root)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := s.ObserveBytes(ctx, c, b, source, models, codec, ObserveOptions{}); result <- err }()
			<-arrived
			s.mu.Lock()
			var task *observationTask
			for _, candidate := range s.observations {
				task = candidate
			}
			s.mu.Unlock()
			if task == nil {
				t.Fatal("missing live task")
			}
			if mode == "cancel-all" {
				cancel()
			} else {
				unblock()
			}
			err = <-result
			endRoot()
			if (mode == "success") != (err == nil) {
				t.Fatalf("%s: %v", mode, err)
			}
			<-task.done
			if err := f.authority.RequireAsyncRoot(task.root, f.main); err == nil || err.Error() != "recovery_operation_expired" {
				t.Fatalf("task root leaked: %v", err)
			}
			s.mu.Lock()
			observations, analyses := len(s.observations), len(s.analyses)
			s.mu.Unlock()
			if observations != 0 || analyses != 0 {
				t.Fatal("task registry leaked", observations, analyses)
			}
			if mode == "cancel-all" {
				a, err := s.AdminGet(context.Background(), str(get(original, "id")))
				if err != nil {
					t.Fatal(err)
				}
				if a == nil {
					t.Fatal("cancelled task lost original asset")
				}
				if get(a, "analysisState") != "cancelled" {
					t.Fatal("terminal cancellation missing", stringify(a))
				}
				for _, key := range []string{"tags", "analyzed", "privacyReviewed", "available", "shared"} {
					if stringify(get(a, key)) != stringify(get(original, key)) {
						t.Fatalf("cancel changed %s: %s", key, stringify(a))
					}
				}
				if yes(get(a, "available")) || yes(get(a, "shared")) {
					t.Fatal("cancelled task published inference")
				}
			}
		})
	}
}
