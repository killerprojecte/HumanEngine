package plugins

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBoundWASMCallCannotEnterOrCancelReplacement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "typing-delay.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "../../plugins/typing-delay")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("real guest build: %v %s", err, output)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	if err = host.Enable(ctx, "typing-delay", artifact); err != nil {
		t.Fatal(err)
	}
	oldCall, err := host.BindCall("typing-delay")
	if err != nil {
		t.Fatal(err)
	}
	entered, release, settled := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	input := []byte(`{"operation":"delay","segment":{"parts":[{"type":"text","text":"hello"}]}}`)
	go func() { close(entered); <-release; _, err := oldCall(ctx, input); settled <- err }()
	<-entered
	if err = host.Disable(ctx, "typing-delay"); err != nil {
		close(release)
		t.Fatal(err)
	}
	if err = host.Enable(ctx, "typing-delay", artifact); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-settled; !errors.Is(err, ErrNotLoaded) {
		t.Fatal("old callback entered replacement", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = oldCall(cancelled, input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = host.Call(cancelled, "typing-delay", input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !host.Loaded("typing-delay") {
		t.Fatal("old cancellation closed new guest")
	}
	newCall, err := host.BindCall("typing-delay")
	if err != nil {
		t.Fatal(err)
	}
	out, err := newCall(ctx, input)
	if err != nil || string(out) != `{"delayMs":1300}` {
		t.Fatal("replacement unusable", string(out), err)
	}
	if err = host.Disable(ctx, "typing-delay"); err != nil || host.Loaded("typing-delay") {
		t.Fatal(err)
	}
	if _, err = newCall(ctx, input); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("disabled callback admitted", err)
	}
}
