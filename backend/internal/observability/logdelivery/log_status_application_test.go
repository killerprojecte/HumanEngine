//go:build humanengine_icu && bundled_wasm

package logdelivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/app"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

// Status is read while the app remains available and its logger is explicitly
// closing. App.Shutdown seals HTTP first; this test does not pretend otherwise.
func TestApplicationLogStatusTCPTracksPhysicalFileTail(t *testing.T) {
	source, images := os.Getenv("HE_SOURCE_ROOT"), os.Getenv("HE_IMAGE_LIBRARY")
	if source == "" || images == "" || os.Getenv("REQUIRE_ICU") == "" {
		t.Fatal("native profile required; no skip")
	}
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	writeGate, closeGate := make(chan struct{}), make(chan struct{})
	var writeOnce, closeOnce sync.Once
	releaseWrite := func() { writeOnce.Do(func() { close(writeGate) }) }
	releaseClose := func() { closeOnce.Do(func() { close(closeGate) }) }
	probe := logdelivery.NewHeldFileOwnerForAppTest(io.Discard, writeGate, closeGate)
	root := t.TempDir()
	options := app.Options{Installation: installation.Options{Database: filepath.Join(root, "data/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), VectorExtension: filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so"), SourceTimezone: "UTC"}, ImageLibrary: images, Locale: "en-US", PluginRoot: t.TempDir(), Node: node, AdminToken: "synthetic-token", Entries: []string{}, Diagnostics: io.Discard, LogRegistry: probe.Registry}
	a, e := app.Open(context.Background(), options)
	if e != nil {
		releaseWrite()
		releaseClose()
		if a != nil {
			a.Shutdown(context.Background())
		}
		t.Fatal(e)
	}
	defer a.Shutdown(context.Background())
	defer releaseClose()
	defer releaseWrite()
	logs, ok := a.Logger().(*observability.StoreLogger)
	if !ok {
		t.Fatal("default Store absent")
	}
	nativeLogSignal(t, probe.WriteEntered)
	tcp := httptest.NewServer(a)
	defer tcp.Close()
	client := tcp.Client()
	client.Timeout = 5 * time.Second
	status := func() observability.StoreLoggingStatus {
		t.Helper()
		r, e := http.NewRequest("GET", tcp.URL+"/api/logs/status", nil)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer synthetic-token")
		res, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var value observability.StoreLoggingStatus
		if e = json.NewDecoder(res.Body).Decode(&value); e != nil || res.StatusCode != 200 {
			t.Fatal("TCP status", res.StatusCode, e)
		}
		return value
	}
	before := status()
	if before.File == nil || before.Stdout == nil || before.File.PendingRecords == 0 || before.File.WrittenRecords != 0 || before.Closed {
		t.Fatal("held write status", before)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	e = logs.Close(ctx)
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("caller escaped held physical write", e)
	}
	closing := status()
	if !closing.Closed || closing.File.State != "closed" || closing.File.PendingRecords != before.File.PendingRecords || closing.File.WrittenRecords != 0 {
		t.Fatal("cancellation fabricated physical exit", closing)
	}
	releaseWrite()
	nativeLogSignal(t, probe.CloseEntered)
	held := status()
	if !held.Closed || held.File.PendingRecords != 0 || held.File.WrittenRecords != held.File.AcceptedRecords || held.File.State != "closed" {
		t.Fatal("completed writes/held close status", held)
	}
	descriptors := probe.DescriptorStatErrors()
	if len(descriptors) != 1 || descriptors[0] != nil {
		t.Fatal("actual fd not held open", descriptors)
	}
	done := probe.OwnerDone()
	pending := 0
	for _, c := range done {
		select {
		case <-c:
		default:
			pending++
		}
	}
	if len(done) != 2 || pending != 1 {
		t.Fatal("independent stdout/file completion", len(done), pending)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	e = a.Shutdown(ctx)
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("app did not retain physical tail", e)
	}
	other, e := installation.Open(context.Background(), options.Installation)
	if other != nil {
		other.Close()
		t.Fatal("writer replaced before fd close")
	}
	if !errors.Is(e, installation.ErrWriterBusy) {
		t.Fatal(e)
	}
	releaseClose()
	e = a.Shutdown(context.Background())
	if e != nil && !errors.Is(e, observability.ErrDeliveryIncomplete) {
		t.Fatal(e)
	}
	for _, c := range done {
		select {
		case <-c:
		default:
			t.Fatal("physical owner not joined")
		}
	}
	for _, e := range probe.DescriptorStatErrors() {
		if !errors.Is(e, os.ErrClosed) {
			t.Fatal("fd still open", e)
		}
	}
	other, e = installation.Open(context.Background(), options.Installation)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.Close(); e != nil {
		t.Fatal(e)
	}
	if a.Bridge.Host.PID() != 0 {
		t.Fatal("status launched Node")
	}
}
