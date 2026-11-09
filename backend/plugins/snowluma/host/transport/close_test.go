package snowlumatransport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestNonCooperatingCloseDisposalAndReplacement(t *testing.T) {
	raw, err := os.ReadFile("testdata/ws-close-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Frame struct {
			Code   int
			Reason string
		}
		ConfiguredCloseTimeout                int
		OpenBeforeDispose, ClosedAfterDispose bool
		Action                                struct {
			Error       string
			Transmitted bool
		}
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.ConfiguredCloseTimeout != 30000 || !source.OpenBeforeDispose || !source.ClosedAfterDispose {
		t.Fatal("source close contract changed")
	}
	path := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	if err = host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	peer := exec.Command("node", "--import", "tsx", "generate-close-source.mjs", "--peer")
	var diagnostics bytes.Buffer
	peer.Stderr = &diagnostics
	out, err := peer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := peer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		if err := peer.Wait(); err != nil {
			t.Errorf("peer: %v %s", err, diagnostics.String())
		}
	}()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatalf("peer address missing: %s", diagnostics.String())
	}
	var address struct{ URL string }
	if err = json.Unmarshal(scanner.Bytes(), &address); err != nil {
		t.Fatal(err)
	}
	events := make(chan []byte, 4)
	go func() {
		defer close(events)
		for scanner.Scan() {
			events <- append([]byte(nil), scanner.Bytes()...)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var mu sync.Mutex
	disposed := 0
	ws, err := NewWS(WSConfig{URL: address.URL, AccountID: "42", Timeout: time.Second, Reconnect: time.Millisecond * 100}, host, "snowluma", HTTPCallbacks{Status: func(s string) {
		if s == "disposed" {
			mu.Lock()
			disposed++
			mu.Unlock()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseAndWait(context.Background())
	if err = ws.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	action := make(chan error, 1)
	go func() { _, err := ws.Action(ctx, "invalid-utf8", new(jsjson.Object)); action <- err }()
	select {
	case data := <-events:
		var frame struct {
			Event  string
			Code   int
			Reason string
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event != "close-frame" || frame.Code != source.Frame.Code || frame.Reason != source.Frame.Reason {
			t.Fatalf("close frame %s", data)
		}
	case <-ctx.Done():
		t.Fatal("close frame not received")
	}
	select {
	case err := <-action:
		t.Fatalf("noncooperating close settled action before disposal: %v", err)
	default:
	}
	closed := make(chan error, 2)
	go func() { closed <- ws.CloseAndWait(context.Background()) }()
	go func() { closed <- ws.CloseAndWait(context.Background()) }()
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("disposal failed to interrupt physical close")
		}
	}
	select {
	case err := <-action:
		var e *Error
		if !errors.As(err, &e) || e.Code != source.Action.Error || e.Transmitted != source.Action.Transmitted {
			t.Fatalf("action %v source %s", err, source.Action.Error)
		}
	case <-ctx.Done():
		t.Fatal("action did not settle")
	}
	select {
	case data := <-events:
		var event struct{ Event string }
		json.Unmarshal(data, &event)
		if event.Event != "physical-close" {
			t.Fatalf("physical close: %s", data)
		}
	case <-ctx.Done():
		t.Fatal("peer still physically open")
	}
	mu.Lock()
	n := disposed
	mu.Unlock()
	if n != 1 {
		t.Fatalf("disposed notification count %d", n)
	}
	if err = host.Disable(context.Background(), "snowluma"); err != nil {
		t.Fatal(err)
	}
	if err = host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	if err = ws.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.Action(context.Background(), "get_login_info", new(jsjson.Object)); err == nil {
		t.Fatal("old transport accepted work")
	}
	if _, err = host.Call(context.Background(), "snowluma", []byte(`{"operation":"manifest"}`)); err != nil {
		t.Fatalf("old disposal damaged replacement: %v", err)
	}
}
