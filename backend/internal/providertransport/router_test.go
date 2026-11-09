package providertransport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOperationRouterNewConfigurationDoesNotRetireAcceptedCall(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	old := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; diagnosticResponse(w) })
	var newerCalls atomic.Int32
	newer := diagnosticModels(t, func(w http.ResponseWriter, r *http.Request) { newerCalls.Add(1); diagnosticResponse(w) })
	var current atomic.Pointer[NativeModels]
	current.Store(old)
	parent := &NativeModels{Router: func(context.Context) (*NativeModels, error) { return current.Load(), nil }}
	oldReturned := make(chan error, 1)
	go func() {
		result, err := parent.Complete(context.Background(), diagnosticRequest())
		if result != nil {
			result.Release()
		}
		oldReturned <- err
	}()
	<-entered
	current.Store(newer)
	result, err := parent.Complete(context.Background(), diagnosticRequest())
	if err != nil {
		t.Fatal(err)
	}
	result.Release()
	if newerCalls.Load() != 1 {
		t.Fatal("new operation used retired configuration")
	}
	select {
	case err := <-oldReturned:
		t.Fatalf("configuration change retired accepted request: %v", err)
	default:
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = parent.CloseAndWait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("old physical owner escaped parent drain", err)
	}
	unblock()
	if err = <-oldReturned; err != nil {
		t.Fatal(err)
	}
	if err = parent.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = parent.Complete(context.Background(), diagnosticRequest()); err == nil {
		t.Fatal("closed dispatcher accepted request")
	}
}

func TestCapturedRouterContinuationDoesNotCrossRouters(t *testing.T) {
	initial, host, _ := v1Models(t)
	defer host.Close(context.Background())
	// One source turn retains one Router; a different Router must not adopt its
	// opaque assistant even when both share the exact host and model identifier.
	parent := &NativeModels{Host: host}
	parent.Router = func(context.Context) (*NativeModels, error) {
		zero, timeout := float64(0), float64(1000)
		policy, err := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "fixture:custom", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
		if err != nil {
			return nil, err
		}
		return &NativeModels{Host: host, Policy: policy, Client: initial.Client, Providers: initial.Providers, ProviderSnapshots: initial.ProviderSnapshots, ModelSnapshots: initial.ModelSnapshots}, nil
	}
	captured, release, err := parent.AcquireRouter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	first, err := captured.Complete(context.Background(), diagnosticRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	next, err := captured.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	if next.Text != "continued" {
		t.Fatal("same-router continuation lost", next.Text)
	}
	other, err := parent.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	if other.Text != "fresh" {
		t.Fatal("different router adopted opaque continuation", other.Text)
	}
	raw, err := os.ReadFile("testdata/router-lifetime-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Cases []string `json:"cases"`
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	for i, actual := range []string{first.Text, next.Text, other.Text} {
		want := strings.SplitN(source.Cases[i], ":", 2)[1]
		if actual != want {
			t.Fatalf("source router case%d: got%s want%s", i, actual, want)
		}
	}

	release()
	if err = parent.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
