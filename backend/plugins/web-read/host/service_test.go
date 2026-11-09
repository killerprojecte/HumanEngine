//go:build web_read_candidate

package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	webread "github.com/killerprojecte/HumanEngine/backend/plugins/web-read/core"
	"github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
)

func ports(call func(context.Context, []byte) ([]byte, error), fetch host.Fetch, work *atomic.Int32) host.Ports {
	return host.Ports{Life: context.Background(), Config: json.RawMessage(`{"maxConcurrency":1}`), Current: func() bool { return true }, BeginWork: func() (func(), bool) { work.Add(1); return func() { work.Add(-1) }, true }, Lifetime: func(ctx, life context.Context) (context.Context, func()) {
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(life, cancel)
		return ctx, func() { stop(); cancel() }
	}, CallGuestWithAdmission: admitNative(call), Fetch: fetch}
}
func nativeGuest(_ context.Context, in []byte) ([]byte, error) {
	return json.Marshal(webread.Invoke(in))
}
func TestMissingTransportIsNotAdmitted(t *testing.T) {
	var work atomic.Int32
	if _, e := host.NewCandidate(ports(nativeGuest, nil, &work)); e == nil {
		t.Fatal("admitted without transport")
	}
}
func TestPhysicalWorkRetainedUntilStreamDrain(t *testing.T) {
	var work atomic.Int32
	finishing, release := make(chan struct{}), make(chan struct{})
	s, e := host.NewCandidate(ports(nativeGuest, func(context.Context, json.RawMessage, time.Duration) (host.Resource, error) {
		return host.Resource{Source: toolresults.Text("hello"), StatusCode: 200, ContentType: "text/plain", Headers: map[string]any{}, RawHeaders: []string{}, Finish: func() { close(finishing); <-release }}, nil
	}, &work))
	if e != nil {
		t.Fatal(e)
	}
	result := make(chan json.RawMessage, 1)
	go func() {
		out, _ := s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"https://fixture.invalid/"}`), func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error) {
			return toolresults.Result{}, errors.New("tool_result_storage_failed")
		})
		result <- out
	}()
	<-finishing
	if work.Load() != 1 {
		t.Fatal("released physical lease before drain")
	}
	out := <-result
	close(release)
	deadline := time.Now().Add(time.Second)
	for work.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	var r map[string]any
	json.Unmarshal(out, &r)
	if r["status"] != "cache_failed" || work.Load() != 0 {
		t.Fatal(string(out), work.Load())
	}
}
func TestConcurrentAdmissionAndCancelledCaller(t *testing.T) {
	var work atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	s, _ := host.NewCandidate(ports(nativeGuest, func(context.Context, json.RawMessage, time.Duration) (host.Resource, error) {
		close(entered)
		<-release
		return host.Resource{}, errors.New("network_request_failed")
	}, &work))
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"x"}`), nil)
	}()
	<-entered
	raw, e := s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"x"}`), nil)
	if e != nil || string(raw) != `{"ok":false,"status":"busy"}` {
		t.Fatal(string(raw), e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, e = s.Execute(ctx, "web-read.request", json.RawMessage(`{"url":"x"}`), nil)
	if !errors.Is(e, context.Canceled) && (e != nil || string(raw) != `{"ok":false,"status":"cancelled"}`) {
		t.Fatal(string(raw), e)
	}
	close(release)
	<-finished
}

// Build the actual distributed artifact separately and set WEB_READ_WASM. This
// test never substitutes a native callback for the guest under test.
func TestRealGuestServiceSourceFixtures(t *testing.T) {
	path := os.Getenv("WEB_READ_WASM")
	if path == "" {
		t.Skip("set WEB_READ_WASM to the built web-read reactor")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	wasm, e := plugins.NewWASMHost(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer wasm.Close(ctx)
	if e = wasm.Enable(ctx, "web-read", data); e != nil {
		t.Fatal(e)
	}
	call, e := wasm.BindCall("web-read")
	if e != nil {
		t.Fatal(e)
	}
	fixture, e := os.ReadFile("../core/testdata/node-service.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases struct {
		Cases []map[string]any `json:"cases"`
	}
	json.Unmarshal(fixture, &cases)
	for i, c := range cases.Cases {
		want := c["expected"]
		delete(c, "expected")
		input, _ := json.Marshal(c)
		raw, e := call(ctx, input)
		if e != nil {
			t.Fatal(e)
		}
		var got any
		json.Unmarshal(raw, &got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: %s != %#v", i, raw, want)
		}
	}
	var work atomic.Int32
	p := ports(call, func(context.Context, json.RawMessage, time.Duration) (host.Resource, error) {
		return host.Resource{Source: toolresults.Text("<p>Hello</p>"), StatusCode: 200, Headers: map[string]any{"content-type": "text/html"}, RawHeaders: []string{"Content-Type", "text/html"}, FinalURL: "https://fixture.invalid/", ContentType: "text/html", Finish: func() {}}, nil
	}, &work)
	p.CallGuestWithAdmission, e = wasm.BindCallWithAdmission("web-read")
	if e != nil {
		t.Fatal(e)
	}
	s, e := host.NewCandidate(p)
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.Execute(ctx, "web-read.read", json.RawMessage(`{"url":"https://fixture.invalid/"}`), func(_ context.Context, _ toolresults.Source, o toolresults.Options) (toolresults.Result, error) {
		if o.Encoding != toolresults.UTF8 || *o.PreviewBytes != 8192 {
			return toolresults.Result{}, errors.New("incorrect guest cache policy")
		}
		return toolresults.Result{ContentType: "text/html", Encoding: toolresults.UTF8, Content: "<p>Hello</p>"}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]any
	json.Unmarshal(out, &result)
	if result["text"] != "Hello" || result["status"] != "ok" {
		t.Fatal(string(out))
	}
	if e = wasm.Disable(ctx, "web-read"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Registrations(); e == nil {
		t.Fatal("stale guest still callable")
	}
}

func TestPhysicalDrainDoesNotHoldLogicalSlot(t *testing.T) {
	var work trackedWork
	var requests atomic.Int32
	finishing, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	fetch := func(context.Context, json.RawMessage, time.Duration) (host.Resource, error) {
		first := requests.Add(1) == 1
		return host.Resource{Source: toolresults.Text("x"), StatusCode: 200, ContentType: "text/plain", Headers: map[string]any{}, RawHeaders: []string{}, Finish: func() {
			if first {
				close(finishing)
				<-release
			}
		}}, nil
	}
	p := ports(nativeGuest, fetch, &work.n)
	p.BeginWork = work.begin
	s, e := host.NewCandidate(p)
	if e != nil {
		t.Fatal(e)
	}
	cache := func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error) {
		return toolresults.Result{}, errors.New("tool_result_storage_failed")
	}
	first, e := s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"http://fixture.invalid/"}`), cache)
	if e != nil {
		t.Fatal(e)
	}
	<-finishing
	var r map[string]any
	json.Unmarshal(first, &r)
	if r["status"] != "cache_failed" {
		t.Fatal(string(first))
	}
	second, e := s.Execute(context.Background(), "web-read.request", json.RawMessage(`{"url":"http://fixture.invalid/"}`), cache)
	if e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(second, &r)
	if r["status"] != "cache_failed" || requests.Load() != 2 || work.n.Load() < 1 {
		t.Fatal("physical drain changed source concurrency admission", string(second), requests.Load(), work.n.Load())
	}
}

// Native oracle policy is synchronous after admission, like source callbacks.
func admitNative(call func(context.Context, []byte) ([]byte, error)) func(context.Context, context.Context, []byte) ([]byte, error) {
	return func(wait, life context.Context, input []byte) ([]byte, error) {
		if wait.Err() != nil {
			return nil, wait.Err()
		}
		return call(life, input)
	}
}
