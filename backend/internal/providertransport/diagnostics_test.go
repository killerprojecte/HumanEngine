package providertransport

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type diagnosticFunc func(context.Context, modeldiagnostics.Entry, []string) error

func (f diagnosticFunc) Capture(c context.Context, e modeldiagnostics.Entry, s []string) error {
	return f(c, e, s)
}
func (f diagnosticFunc) CaptureLazy(ctx context.Context, project func() (modeldiagnostics.Entry, error), secrets []string) error {
	entry, err := project()
	if err != nil {
		return err
	}
	return f(ctx, entry, secrets)
}

func diagnosticModels(t *testing.T, handler http.HandlerFunc) *NativeModels {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, p := nativeTestClient(server)
	p.ID = "p"
	client.Credentials = testCredentials("actual-attempt-secret")
	retries, timeout := float64(1), float64(1000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: p.Protocol, RetryCount: &retries, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return &NativeModels{Policy: policy, Client: client, Providers: map[string]Provider{"p": p}}
}
func diagnosticRequest() CompletionRequest {
	return CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}}
}
func diagnosticResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok","tool_calls":[]}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
}
func TestDiagnosticHookCannotRetrySuccessfulModel(t *testing.T) {
	for _, panicHook := range []bool{false, true} {
		t.Run(fmt.Sprint(panicHook), func(t *testing.T) {
			var calls, captures atomic.Int32
			m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); diagnosticResponse(w) })
			m.Diagnostics = diagnosticFunc(func(_ context.Context, entry modeldiagnostics.Entry, secrets []string) error {
				captures.Add(1)
				if len(secrets) != 1 || secrets[0] != "actual-attempt-secret" {
					t.Error("lost actual attempt secret")
				}
				if entry.ModelID != "m" || len(entry.Messages) != 1 {
					t.Error("lost actual request")
				}
				if panicHook {
					panic("synthetic diagnostic failure")
				}
				return errors.New("synthetic diagnostic failure")
			})
			c, e := m.Complete(context.Background(), diagnosticRequest())
			if e != nil || c.Text != "ok" {
				t.Fatalf("diagnostic changed result: %v", e)
			}
			if calls.Load() != 1 || captures.Load() != 1 {
				t.Fatalf("requests %d captures %d", calls.Load(), captures.Load())
			}
			if e = m.CloseAndWait(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestFailedModelDoesNotCaptureDiagnostic(t *testing.T) {
	var captures atomic.Int32
	m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "synthetic", http.StatusBadRequest) })
	m.Diagnostics = diagnosticFunc(func(context.Context, modeldiagnostics.Entry, []string) error { captures.Add(1); return nil })
	if _, e := m.Complete(context.Background(), diagnosticRequest()); e == nil {
		t.Fatal("expected model failure")
	}
	if captures.Load() != 0 {
		t.Fatal("failure manufactured success diagnostic")
	}
	if e := m.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestNativeDrainRetainsDiagnosticPhysicalCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { diagnosticResponse(w) })
	m.Diagnostics = diagnosticFunc(func(context.Context, modeldiagnostics.Entry, []string) error { close(entered); <-release; return nil })
	returned := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, e := m.Complete(ctx, diagnosticRequest()); returned <- e }()
	select {
	case <-entered:
	case err := <-returned:
		t.Fatalf("request ended before held owner entered: %v", err)
	}
	cancel()
	wait, stop := context.WithCancel(context.Background())
	stop()
	if e := m.CloseAndWait(wait); !errors.Is(e, context.Canceled) {
		t.Fatalf("diagnostic callback escaped drain: %v", e)
	}
	close(release)
	<-returned
	if e := m.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestLateCanceledDiagnosticDoesNotInvokeOwner(t *testing.T) {
	var captures int
	m := &NativeModels{Diagnostics: diagnosticFunc(func(context.Context, modeldiagnostics.Entry, []string) error { captures++; return nil })}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.captureDiagnostic(ctx, "replyer", nil, &Completion{ModelID: "m"}, false, []string{"secret"})
	if captures != 0 {
		t.Fatal("late canceled completion invoked diagnostic owner")
	}
}

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDiagnosticActualLateAttemptSkipsCaptureAndDrains(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { t.Error("custom transport should own request") })
	var captured atomic.Int32
	m.Diagnostics = diagnosticFunc(func(context.Context, modeldiagnostics.Entry, []string) error { captured.Add(1); return nil })
	m.Client.HTTP = &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"late","tool_calls":[]}}]}`)), Request: r}, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { _, e := m.Complete(ctx, diagnosticRequest()); returned <- e }()
	select {
	case <-entered:
	case err := <-returned:
		t.Fatalf("request ended before held owner entered: %v", err)
	}
	cancel()
	wait, stop := context.WithCancel(context.Background())
	stop()
	if e := m.CloseAndWait(wait); !errors.Is(e, context.Canceled) {
		t.Fatalf("physical HTTP attempt escaped: %v", e)
	}
	close(release)
	<-returned
	if e := m.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if captured.Load() != 0 {
		t.Fatal("canceled late physical success was captured")
	}
}

func TestDiagnosticDisabledSkipsUnencodableNativeProjection(t *testing.T) {
	var enabled atomic.Bool
	var probes, secrets atomic.Int32
	store, e := modeldiagnostics.New(modeldiagnostics.Ports{Enabled: func() bool { probes.Add(1); return enabled.Load() }, Secrets: func(context.Context) ([]string, error) { secrets.Add(1); return nil, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer store.CloseAndWait(context.Background())
	m := &NativeModels{Diagnostics: store}
	// The native projection cannot encode this value. The genuine Store's
	// Enabled probe must nevertheless run first, exactly once, before any encode.
	m.captureDiagnostic(context.Background(), "replyer", []map[string]any{{"content": make(chan int)}}, &Completion{Text: "synthetic", ModelID: "m"}, false, nil)
	if probes.Load() != 1 || secrets.Load() != 0 {
		t.Fatalf("projection ran before disabled gate probes=%d secrets=%d", probes.Load(), secrets.Load())
	}
	enabled.Store(true)
	m.captureDiagnostic(context.Background(), "replyer", []map[string]any{{"role": "user", "content": "synthetic"}}, &Completion{Text: "synthetic", ModelID: "m"}, false, nil)
	if probes.Load() != 2 || secrets.Load() != 1 {
		t.Fatal("enabled native projection not executed")
	}
	if rows := store.Rows(); len(rows) != 1 {
		t.Fatal("enabled capture missing", rows)
	}
}
