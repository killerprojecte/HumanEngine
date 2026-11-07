package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealBuiltinWASMLoadInvokeUnload(t *testing.T) {
	ctx := context.Background()
	artifact := filepath.Join(t.TempDir(), "typing-delay.wasm")
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", artifact, "../../plugins/typing-delay")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("WASM build: %v %s", err, out)
	}
	bytes, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	if host.Loaded("typing-delay") {
		t.Fatal("loaded before enable")
	}
	if err = host.Enable(ctx, "typing-delay", bytes); err != nil {
		t.Fatal(err)
	}
	out, err := host.Call(ctx, "typing-delay", []byte(`{"operation":"delay","segment":{"parts":[{"type":"text","text":"hello"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		DelayMS int `json:"delayMs"`
	}
	if err = json.Unmarshal(out, &result); err != nil || result.DelayMS != 1300 {
		t.Fatal(string(out), err)
	}
	if err = host.Disable(ctx, "typing-delay"); err != nil {
		t.Fatal(err)
	}
	if host.Loaded("typing-delay") {
		t.Fatal("disabled instance still loaded")
	}
	if _, err = host.Call(ctx, "typing-delay", []byte(`{}`)); err != ErrNotLoaded {
		t.Fatal("disabled plugin callable", err)
	}
	if err = host.Enable(ctx, "typing-delay", bytes); err != nil {
		t.Fatal("reload failed", err)
	}
}

// lifecycleWASM is a tiny real module, so cancellation tests need no compiler
// subprocess. probe.entered lets each test synchronize with guest execution.
func lifecycleWASM(free, initialize []byte) []byte {
	uleb := func(n uint64) []byte {
		var result []byte
		for {
			b := byte(n & 127)
			n >>= 7
			if n != 0 {
				b |= 128
			}
			result = append(result, b)
			if n == 0 {
				return result
			}
		}
	}
	artifact := []byte{0, 97, 115, 109, 1, 0, 0, 0}
	section := func(id byte, body []byte) {
		artifact = append(artifact, id)
		artifact = append(artifact, uleb(uint64(len(body)))...)
		artifact = append(artifact, body...)
	}
	section(1, []byte{4, 0x60, 1, 0x7f, 1, 0x7f, 0x60, 1, 0x7f, 0, 0x60, 2, 0x7f, 0x7f, 1, 0x7e, 0x60, 0, 0})
	section(2, append([]byte{1, 5, 'p', 'r', 'o', 'b', 'e', 7}, append([]byte("entered"), 0, 3)...))
	section(3, []byte{4, 0, 1, 2, 3})
	section(5, []byte{1, 0, 1})
	exports := []byte{5}
	for i, name := range []string{"alloc", "free", "plugin_call", "_initialize", "memory"} {
		exports = append(exports, byte(len(name)))
		exports = append(exports, name...)
		kind, index := byte(0), byte(i+1)
		if name == "memory" {
			kind, index = 2, 0
		}
		exports = append(exports, kind, index)
	}
	section(7, exports)
	if free == nil {
		free = []byte{0x0b}
	}
	if initialize == nil {
		initialize = []byte{0x0b}
	}
	// alloc returns address 8; plugin_call returns the same two-byte input.
	call := append([]byte{0x42}, uleb(uint64(8)<<32|2)...)
	call = append(call, 0x0b)
	code := []byte{4}
	for _, body := range [][]byte{{0x41, 8, 0x0b}, free, call, initialize} {
		code = append(code, uleb(uint64(len(body)+1))...)
		code = append(code, 0) // No local declarations.
		code = append(code, body...)
	}
	section(10, code)
	return artifact
}

func waitWASMResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("WASM lifecycle operation did not finish")
		return nil
	}
}

func TestWASMCallerCancellationInterruptsFreeAndAllowsReload(t *testing.T) {
	host, err := NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	entered := make(chan struct{}, 1)
	if _, err = host.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func() {
		entered <- struct{}{}
	}).Export("entered").Instantiate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Signal entry into free, then loop indefinitely until the caller cancels.
	artifact := lifecycleWASM([]byte{0x10, 0, 0x03, 0x40, 0x0c, 0, 0x0b, 0x0b}, nil)
	if err = host.Enable(context.Background(), "free-loop", artifact); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := host.Call(ctx, "free-loop", []byte(`{}`)); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest free did not begin")
	}
	cancel()
	if err = waitWASMResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup: %v", err)
	}
	if host.Loaded("free-loop") {
		t.Fatal("cancelled guest remains loaded")
	}
	if err = host.Enable(context.Background(), "free-loop", lifecycleWASM(nil, nil)); err != nil {
		t.Fatal("reload after cancellation:", err)
	}
	if out, err := host.Call(context.Background(), "free-loop", []byte(`{}`)); err != nil || string(out) != `{}` {
		t.Fatal(string(out), err)
	}
}

func TestWASMReenableWaitsForConcurrentDisable(t *testing.T) {
	host, err := NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var starts atomic.Int32
	if _, err = host.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func() {
		if starts.Add(1) == 1 {
			close(entered)
			<-release
		}
	}).Export("entered").Instantiate(context.Background()); err != nil {
		t.Fatal(err)
	}
	artifact := lifecycleWASM(nil, []byte{0x10, 0, 0x0b})
	initial := make(chan error, 1)
	go func() { initial <- host.Enable(context.Background(), "replace", artifact) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest initialization did not begin")
	}
	host.mu.Lock()
	instance := host.instances["replace"]
	host.mu.Unlock()
	disabled := make(chan error, 1)
	go func() { disabled <- host.Disable(context.Background(), "replace") }()
	<-instance.stopped.Done()
	secondDisable := make(chan error, 1)
	go func() { secondDisable <- host.disableInstance(context.Background(), "replace", instance) }()
	reenabled := make(chan error, 1)
	go func() { reenabled <- host.Enable(context.Background(), "replace", artifact) }()
	// The old module still owns the runtime name until the blocked host call exits.
	host.mu.Lock()
	retained := host.instances["replace"] == instance
	host.mu.Unlock()
	if !retained {
		t.Error("teardown removed the instance before unloading")
	}
	select {
	case err := <-disabled:
		t.Fatalf("disable returned before guest exit: %v", err)
	case err := <-secondDisable:
		t.Fatalf("second disable returned before guest exit: %v", err)
	case err := <-reenabled:
		t.Fatalf("re-enable returned before guest exit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err = waitWASMResult(t, initial); err == nil {
		t.Fatal("cancelled initialization reported enabled")
	}
	if err = waitWASMResult(t, disabled); err != nil {
		t.Fatal(err)
	}
	if err = waitWASMResult(t, secondDisable); err != nil {
		t.Fatal(err)
	}
	if err = waitWASMResult(t, reenabled); err != nil {
		t.Fatal("re-enable raced the old runtime name:", err)
	}
	if !host.Loaded("replace") {
		t.Fatal("replacement is not loaded")
	}
}

func TestWASMConcurrentCloseWaitsForTeardown(t *testing.T) {
	host, err := NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	if _, err = host.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func() {
		close(entered)
		<-release
	}).Export("entered").Instantiate(context.Background()); err != nil {
		t.Fatal(err)
	}
	initial := make(chan error, 1)
	go func() {
		initial <- host.Enable(context.Background(), "closing", lifecycleWASM(nil, []byte{0x10, 0, 0x0b}))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest initialization did not begin")
	}
	host.mu.Lock()
	instance := host.instances["closing"]
	host.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- host.Close(context.Background()) }()
	<-instance.stopped.Done()
	closedAgain := make(chan error, 1)
	go func() { closedAgain <- host.Close(context.Background()) }()
	select {
	case err := <-closedAgain:
		t.Fatalf("second close returned before teardown: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err = waitWASMResult(t, initial); err == nil {
		t.Fatal("closed initialization reported enabled")
	}
	if err = waitWASMResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if err = waitWASMResult(t, closedAgain); err != nil {
		t.Fatal(err)
	}
	if err = host.Enable(context.Background(), "closing", lifecycleWASM(nil, nil)); err == nil {
		t.Fatal("closed host accepted enable")
	}
}

func TestAllBuiltinArtifactsExecutePolicy(t *testing.T) {
	cases := []struct{ id, path, input, contains string }{
		{"web-read", "web-read", `{"operation":"extract","html":"<p>Hello</p><script>hidden</script>"}`, `"text":"Hello"`},
		{"web-search", "web-search", `{"operation":"parse_api","provider":"tavily","body":"{\"results\":[]}"}`, `[]`},
		{"snowluma", "snowluma-wasm", `{"operation":"parse_cq","text":"[CQ:at,qq=123]"}`, `"type":"at"`},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			artifact := filepath.Join(t.TempDir(), c.id+".wasm")
			build := exec.Command("go", "build", "-buildmode=c-shared", "-ldflags=-s -w", "-o", artifact, "../../plugins/"+c.path)
			build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("compile %v: %s", err, out)
			}
			bytes, err := os.ReadFile(artifact)
			if err != nil {
				t.Fatal(err)
			}
			host, err := NewWASMHost(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close(context.Background())
			if err = host.Enable(context.Background(), c.id, bytes); err != nil {
				t.Fatal(err)
			}
			out, err := host.Call(context.Background(), c.id, []byte(c.input))
			if err != nil || !strings.Contains(string(out), c.contains) {
				t.Fatalf("policy execution %s: %v", out, err)
			}
			manifest, err := host.Call(context.Background(), c.id, []byte(`{"operation":"manifest"}`))
			if err != nil || !strings.Contains(string(manifest), `"apiVersion":2`) {
				t.Fatal("V2 manifest", string(manifest), err)
			}
			if err = host.Disable(context.Background(), c.id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWASMDisableInterruptsGuestInitialization(t *testing.T) {
	// A real WASM _initialize export loops forever. No timers or fake runtime hooks.
	artifact := []byte{0, 97, 115, 109, 1, 0, 0, 0, 1, 4, 1, 96, 0, 0, 3, 2, 1, 0, 7, 15, 1, 11}
	artifact = append(artifact, []byte("_initialize")...)
	artifact = append(artifact, 0, 0, 10, 9, 1, 7, 0, 3, 64, 12, 0, 11, 11)
	host, err := NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- host.Enable(context.Background(), "loop", artifact) }()
	for {
		host.mu.Lock()
		pending := host.instances["loop"] != nil
		host.mu.Unlock()
		if pending {
			break
		}
		runtime.Gosched()
	}
	if err = host.Disable(context.Background(), "loop"); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err == nil {
		t.Fatal("disabled initialization reported enabled")
	}
	if host.Loaded("loop") {
		t.Fatal("disabled instance retained")
	}
}
