//go:build humanengine_icu && bundled_wasm

package logdelivery_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/app"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

func nativeLogSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatal("real file ownership boundary not reached")
	}
}
func TestApplicationActualFileWriteAndCloseRetainInstallation(t *testing.T) {
	source, images := os.Getenv("HE_SOURCE_ROOT"), os.Getenv("HE_IMAGE_LIBRARY")
	if source == "" || images == "" || os.Getenv("REQUIRE_ICU") == "" {
		t.Fatal("exact native application profile is required; no silent skip")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeGate, closeGate := make(chan struct{}), make(chan struct{})
	var writeOnce, closeOnce sync.Once
	releaseWrite := func() { writeOnce.Do(func() { close(writeGate) }) }
	releaseClose := func() { closeOnce.Do(func() { close(closeGate) }) }
	probe := logdelivery.NewHeldFileOwnerForAppTest(io.Discard, writeGate, closeGate)
	options := app.Options{Installation: installation.Options{Database: filepath.Join(root, "data/main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), VectorExtension: filepath.Join(source, "node_modules/sqlite-vec-linux-x64/vec0.so"), SourceTimezone: "UTC"}, ImageLibrary: images, Locale: "en-US", PluginRoot: t.TempDir(), Node: node, AdminToken: "synthetic-token", Entries: []string{}, Diagnostics: io.Discard, LogRegistry: probe.Registry}
	a, err := app.Open(context.Background(), options)
	if err != nil {
		releaseWrite()
		releaseClose()
		if a != nil {
			a.Shutdown(context.Background())
		}
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())
	defer releaseClose()
	defer releaseWrite()
	logs, ok := a.Logger().(*observability.StoreLogger)
	if !ok {
		t.Fatal("native app bypassed source default")
	}
	nativeLogSignal(t, probe.WriteEntered)
	assertHeld := func() {
		t.Helper()
		other, e := installation.Open(context.Background(), options.Installation)
		if other != nil {
			other.Close()
			t.Fatal("overlapping installation writer")
		}
		if !errors.Is(e, installation.ErrWriterBusy) {
			t.Fatal("writer lease released before file owner", e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err = a.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("held actual write escaped shutdown", err)
	}
	assertHeld()
	for _, err := range probe.DescriptorStatErrors() {
		if err != nil {
			t.Fatal("file descriptor closed while write held", err)
		}
	}
	releaseWrite()
	nativeLogSignal(t, probe.CloseEntered)
	// The same real regular-file descriptor completed its writes and fsync, but
	// has not executed close. No process exit is used as the join witness.
	if errors := probe.DescriptorStatErrors(); len(errors) != 1 || errors[0] != nil {
		t.Fatal("expected one still-open real fd", errors)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = a.ShutdownAndFlush(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("held actual close escaped shutdown", err)
	}
	assertHeld()
	done := probe.OwnerDone()
	if len(done) != 2 {
		t.Fatal("source must have independent stdout/file owners", len(done))
	}
	releaseClose()
	if err = a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, boundary := range done {
		select {
		case <-boundary:
		default:
			t.Fatal("app returned before native owner Done")
		}
	}
	for _, err := range probe.DescriptorStatErrors() {
		if !errors.Is(err, os.ErrClosed) {
			t.Fatal("fd is not physically closed", err)
		}
	}
	if err = logs.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "data/logs/runtime.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"code":"app.drained"`) != 1 {
		t.Fatal("retries duplicated final record", string(data))
	}
	reopened, err := installation.Open(context.Background(), options.Installation)
	if err != nil {
		t.Fatal("physical join did not release installation", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
