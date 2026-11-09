package snowlumatransport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestWSActualSourceGuestAndPhysicalUnload(t *testing.T) {
	for _, window := range []string{"default", "window9"} {
		t.Run(window, func(t *testing.T) { runWSActualSourceGuestAndPhysicalUnload(t, window) })
	}
}
func runWSActualSourceGuestAndPhysicalUnload(t *testing.T, window string) {
	var oracle struct {
		Actions []struct {
			Action      string
			Value       any
			Error       string
			Transmitted bool
		}
		Statuses    []string
		Connections []any
		Requests    []any
		WireCases   []struct {
			Action, Error string
			CloseCode     int
			CloseReason   string
			Transmitted   bool
			Errors        []string
		}
	}
	fixture := "testdata/ws-source.json"
	if window == "window9" {
		fixture = "testdata/ws-window9-source.json"
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", artifactPath, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("guest build: %v %s", err, out)
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual guest SHA256 %x", sha256.Sum256(artifact))
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err = host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	verifyReceiveOracle(t, host)
	peer := exec.Command("node", "--import", "tsx", "generate-ws-source.mjs", "--peer")
	if window == "window9" {
		peer.Args = append(peer.Args, "--window9")
	}
	var diagnostics bytes.Buffer
	peer.Stderr = &diagnostics
	stdout, err := peer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := peer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		if err := peer.Wait(); err != nil {
			t.Errorf("Node peer: %v %s", err, diagnostics.String())
		}
	})
	var address struct{ URL string }
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("missing peer address: %s", diagnostics.String())
	}
	if err = json.Unmarshal(scanner.Bytes(), &address); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	statuses := []string{}
	errorCodes := []string{}
	eventTexts := []string{}
	entered, release := make(chan struct{}), make(chan struct{})
	block := false
	var once sync.Once
	ws, err := NewWS(WSConfig{URL: address.URL, AccountID: "42", Token: "synthetic-token", Timeout: 1000 * time.Millisecond, Reconnect: 100 * time.Millisecond}, host, "snowluma", HTTPCallbacks{Error: func(code string, err error) { mu.Lock(); errorCodes = append(errorCodes, code); mu.Unlock() }, Status: func(s string) { mu.Lock(); statuses = append(statuses, s); mu.Unlock() }, Receive: func(ctx context.Context, raw string) error {
		mu.Lock()
		eventTexts = append(eventTexts, raw)
		hold := block
		mu.Unlock()
		if hold {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		if err := ws.CloseAndWait(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = ws.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range oracle.Actions {
		if row.Action == "disconnect" {
			if _, err = ws.Action(ctx, "binary-event", new(jsjson.Object)); err != nil {
				t.Fatal(err)
			}
		}
		call := ctx
		var cancelBefore context.CancelFunc
		name := row.Action
		if name == "cancel-before" {
			name = "ok"
			call, cancelBefore = context.WithCancel(ctx)
			cancelBefore()
		}
		params := new(jsjson.Object)
		if name == "large-request" {
			_ = params.Set("text", strings.Repeat("0123456789abcdef", 4096))
		}
		value, err := ws.Action(call, name, params)
		if row.Error != "" {
			var actual *Error
			if !errors.As(err, &actual) || actual.Code != row.Error || actual.Transmitted != row.Transmitted {
				t.Fatalf("%s: actual %v source %s/%t", row.Action, err, row.Error, row.Transmitted)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", row.Action, err)
		}
		if name == "compressed" {
			text, _ := value.(*jsjson.Object).Get("text")
			value = map[string]any{"length": float64(len(text.(string)))}
		} else {
			value = normalizationValue(value)
		}
		if !reflect.DeepEqual(value, row.Value) {
			t.Fatalf("%s value %v source %v", name, value, row.Value)
		}
	}
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		connected := 0
		for _, s := range statuses {
			if s == "connected" {
				connected++
			}
		}
		mu.Unlock()
		if connected == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("reconnect missing")
		case <-time.After(5 * time.Millisecond):
		}
	}
	inspection, err := ws.Action(ctx, "inspect", new(jsjson.Object))
	if err != nil {
		t.Fatal(err)
	}
	connections, _ := inspection.(*jsjson.Object).Get("connections")
	requests, _ := inspection.(*jsjson.Object).Get("requests")
	if !reflect.DeepEqual(normalizationValue(connections), oracle.Connections) || !reflect.DeepEqual(normalizationValue(requests), oracle.Requests) {
		wire, _ := jsjson.Stringify(inspection)
		t.Fatalf("actual peer wire %s does not match actual TS", wire)
	}
	mu.Lock()
	actualStatuses := append([]string{}, statuses...)
	events := append([]string{}, eventTexts...)
	mu.Unlock()
	if !reflect.DeepEqual(actualStatuses, oracle.Statuses) {
		t.Fatalf("statuses %v source %v", actualStatuses, oracle.Statuses)
	}
	if len(events) != 1 || !bytes.Contains([]byte(events[0]), []byte("�")) {
		t.Fatalf("binary replacement event %v", events)
	}
	for _, row := range oracle.WireCases {
		mu.Lock()
		before := 0
		for _, s := range statuses {
			if s == "connected" {
				before++
			}
		}
		errorStart := len(errorCodes)
		mu.Unlock()
		started := time.Now()
		_, err := ws.Action(ctx, row.Action, new(jsjson.Object))
		actionErr := err
		deadline := time.After(3 * time.Second)
		for {
			mu.Lock()
			count := 0
			for _, s := range statuses {
				if s == "connected" {
					count++
				}
			}
			mu.Unlock()
			if count > before {
				break
			}
			select {
			case <-deadline:
				t.Fatal("wire rejection did not reconnect")
			case <-time.After(5 * time.Millisecond):
			}
		}
		t.Logf("wire %s: actual action=%v; physical close and fresh bootstrap observed in %s", row.Action, actionErr, time.Since(started))
		var actual *Error
		if !errors.As(actionErr, &actual) || actual.Code != row.Error || actual.Transmitted != row.Transmitted {
			t.Fatalf("wire %s: %v", row.Action, actionErr)
		}
		inspected, inspectErr := ws.Action(ctx, "inspect", new(jsjson.Object))
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		closes, _ := inspected.(*jsjson.Object).Get("closes")
		closeJSON, _ := json.Marshal(normalizationValue(closes))
		var records []struct {
			Action string
			Code   int
			Reason string
		}
		if err := json.Unmarshal(closeJSON, &records); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, record := range records {
			if record.Action == row.Action {
				found = true
				if record.Code != row.CloseCode || record.Reason != row.CloseReason {
					t.Fatalf("close frame %s: %d %q source %d %q", row.Action, record.Code, record.Reason, row.CloseCode, row.CloseReason)
				}
			}
		}
		if !found {
			t.Fatalf("close frame missing for %s", row.Action)
		}
		mu.Lock()
		actualErrors := append([]string{}, errorCodes[errorStart:]...)
		mu.Unlock()
		if !reflect.DeepEqual(actualErrors, row.Errors) {
			t.Fatalf("wire %s errors %v source %v", row.Action, actualErrors, row.Errors)
		}
	}
	mu.Lock()
	block = true
	mu.Unlock()
	actionDone := make(chan error, 1)
	go func() { _, err := ws.Action(ctx, "binary-event", new(jsjson.Object)); actionDone <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("blocked callback not entered")
	}
	closed := make(chan error, 1)
	go func() { closed <- ws.CloseAndWait(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before actual callback: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("physical close did not finish")
	}
	select {
	case <-actionDone:
	case <-ctx.Done():
		t.Fatal("action did not settle")
	}
	if err := host.Disable(context.Background(), "snowluma"); err != nil {
		t.Fatal(err)
	}
}
