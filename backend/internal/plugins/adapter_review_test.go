package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

type rpcFixture struct {
	host    *V1Host
	meta    AdapterMetadata
	scene   dispatch.SceneRef
	actions map[string]string
}

func newRPCFixture(t *testing.T, services AdapterServices, config map[string]any) *rpcFixture {
	t.Helper()
	if config == nil {
		config = map[string]any{}
	}
	if services.IsRecalled == nil {
		services.IsRecalled = func(context.Context, AdapterMetadata, string, string) (bool, error) { return false, nil }
	}
	if services.Media == nil {
		services.Media = func(context.Context, AdapterMetadata, string, string) (*AdapterMedia, error) { return nil, nil }
	}
	if services.Receive == nil {
		services.Receive = func(context.Context, AdapterMetadata, dispatch.SceneRef, json.RawMessage) error { return nil }
	}
	h, e := NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: services})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.Close(ctx)
	})
	path, e := filepath.Abs("testdata/adapter-cancellation-fixture.mjs")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := h.Enable(context.Background(), "adapter-calls", path, config)
	if e != nil {
		t.Fatal(e)
	}
	f := &rpcFixture{host: h, meta: h.RegisteredAdapters()[0], actions: map[string]string{}}
	var loaded struct {
		Registrations []struct{ Kind, Name, Handle string }
	}
	if e = json.Unmarshal(raw, &loaded); e != nil {
		t.Fatal(e)
	}
	for _, r := range loaded.Registrations {
		if r.Kind == "action" {
			f.actions[r.Name] = r.Handle
		}
	}
	raw, e = h.Call(context.Background(), "adapter-calls", f.actions["scene"], nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &f.scene); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *rpcFixture) send(ctx context.Context, mode string) (dispatch.Receipt, error) {
	p, _ := json.Marshal(map[string]string{"type": "text", "text": mode})
	return f.host.AdapterSend(ctx, f.meta.ID, f.meta.Generation, f.scene, dispatch.Segment{Parts: []json.RawMessage{p}}, nil)
}
func awaitRPC[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("RPC regression fixture timed out")
		var zero T
		return zero
	}
}
func statusResponsive(t *testing.T, f *rpcFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, e := f.host.AdapterStatus(ctx, f.meta.ID, f.meta.Generation)
	if e != nil || s != "connected" {
		t.Fatal("cancelled invocation retained isolate", s, e)
	}
}

func TestV1AdapterInvocationCancelsNativeRPCAcrossAwait(t *testing.T) {
	for _, operation := range []string{"send-media", "send-edit", "history-normalization", "getMessage-normalization", "admin-media"} {
		t.Run(operation, func(t *testing.T) {
			entered, exited := make(chan struct{}), make(chan struct{})
			wait := func(ctx context.Context) { close(entered); <-ctx.Done(); close(exited) }
			services := AdapterServices{}
			switch operation {
			case "send-media", "admin-media":
				services.Media = func(ctx context.Context, _ AdapterMetadata, _, _ string) (*AdapterMedia, error) {
					wait(ctx)
					return nil, ctx.Err()
				}
			case "send-edit":
				services.Edit = func(ctx context.Context, _ AdapterMetadata, _ dispatch.SceneRef, _, _ json.RawMessage) (json.RawMessage, error) {
					wait(ctx)
					return nil, ctx.Err()
				}
			default:
				services.IsRecalled = func(ctx context.Context, _ AdapterMetadata, _, _ string) (bool, error) {
					wait(ctx)
					return false, ctx.Err()
				}
			}
			f := newRPCFixture(t, services, nil)
			ctx, cancel := context.WithCancel(context.Background())
			type outcome struct {
				receipt dispatch.Receipt
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				var r dispatch.Receipt
				var e error
				switch operation {
				case "send-media":
					r, e = f.send(ctx, "media")
				case "send-edit":
					r, e = f.send(ctx, "edit")
				case "history-normalization":
					_, e = f.host.AdapterHistory(ctx, f.meta.ID, f.meta.Generation, f.scene, map[string]any{"limit": 1})
				case "getMessage-normalization":
					_, e = f.host.AdapterGetMessage(ctx, f.meta.ID, f.meta.Generation, f.scene, f.scene.ID+":synthetic")
				default:
					_, e = f.host.Call(ctx, "adapter-calls", f.actions["admin-media"], nil)
				}
				done <- outcome{r, e}
			}()
			awaitRPC(t, entered)
			cancel()
			result := awaitRPC(t, done)
			awaitRPC(t, exited)
			if strings.HasPrefix(operation, "send-") {
				if result.err != nil || result.receipt.Status != dispatch.Unknown || result.receipt.Detail == nil || *result.receipt.Detail != "request_cancelled" {
					t.Fatal(result)
				}
			} else if !errors.Is(result.err, context.Canceled) {
				t.Fatal(result.err)
			}
			statusResponsive(t, f)
		})
	}
}

func TestV1AdapterCallCancellationIsolatedFromConcurrentAndBackground(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	receives := 0
	f := newRPCFixture(t, AdapterServices{
		Media: func(ctx context.Context, _ AdapterMetadata, asset, _ string) (*AdapterMedia, error) {
			if asset == "blocked" {
				close(entered)
				<-ctx.Done()
				close(exited)
				return nil, ctx.Err()
			}
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			return &AdapterMedia{File: "synthetic", Type: "image/png"}, nil
		},
		Receive: func(ctx context.Context, _ AdapterMetadata, _ dispatch.SceneRef, _ json.RawMessage) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			mu.Lock()
			receives++
			mu.Unlock()
			return nil
		},
	}, map[string]any{"background": true})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan dispatch.Receipt, 1)
	second := make(chan dispatch.Receipt, 1)
	go func() { r, _ := f.send(ctx, "media"); first <- r }()
	awaitRPC(t, entered)
	go func() {
		r, e := f.send(context.Background(), "healthy")
		if e != nil {
			r.Status = dispatch.Failed
		}
		second <- r
	}()
	cancel()
	if r := awaitRPC(t, first); r.Status != dispatch.Unknown {
		t.Fatal(r)
	}
	awaitRPC(t, exited)
	if r := awaitRPC(t, second); r.Status != dispatch.Sent {
		t.Fatal("unrelated call cancelled", r)
	}
	statusResponsive(t, f)
	mu.Lock()
	before := receives
	mu.Unlock()
	waitAdapter(t, func() bool { mu.Lock(); defer mu.Unlock(); return receives > before })
	raw, e := f.host.Call(context.Background(), "adapter-calls", f.actions["counts"], nil)
	if e != nil || string(raw) != "2" {
		t.Fatal("send replay", string(raw), e)
	}
}

func TestV1AdapterLateNativeReplyCannotSettleNextInvocation(t *testing.T) {
	entered, release, returned := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	f := newRPCFixture(t, AdapterServices{Media: func(ctx context.Context, _ AdapterMetadata, asset, _ string) (*AdapterMedia, error) {
		if asset == "blocked" {
			entered <- ctx
			<-release
			defer close(returned)
			return &AdapterMedia{File: "late", Type: "image/png"}, nil
		}
		return &AdapterMedia{File: "current", Type: "image/png"}, nil
	}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan dispatch.Receipt, 1)
	go func() { r, _ := f.send(ctx, "media"); done <- r }()
	native := awaitRPC(t, entered)
	cancel()
	if r := awaitRPC(t, done); r.Status != dispatch.Unknown {
		t.Fatal(r)
	}
	if native.Err() != context.Canceled {
		t.Fatal("native callback context not cancelled", native.Err())
	}
	statusResponsive(t, f)
	r, e := f.send(context.Background(), "healthy")
	if e != nil || r.Status != dispatch.Sent {
		t.Fatal(r, e)
	}
	close(release)
	awaitRPC(t, returned)
	statusResponsive(t, f)
	raw, e := f.host.Call(context.Background(), "adapter-calls", f.actions["counts"], nil)
	if e != nil || string(raw) != "2" {
		t.Fatal(string(raw), e)
	}
}

func installReviewPlugin(t *testing.T, id string, two, throwDispose bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "fixture.mjs")
	trace := filepath.Join(root, "trace")
	quoted, _ := json.Marshal(trace)
	names := "['one']"
	if two {
		names = "['one','two']"
	}
	dispose := ""
	if throwDispose {
		dispose = "if(name==='two')throw Error('synthetic_dispose_failed');"
	}
	source := `import {appendFileSync} from 'node:fs';const trace=s=>appendFileSync(` + string(quoted) + `,s+'\n');export default {manifest:{id:'` + id + `',configSchema:{type:'object'},capabilities:['adapter']},setup(ctx){ctx.onDispose(async()=>{await Promise.resolve();trace('setup.dispose')});for(const name of ` + names + `)ctx.registerAdapter(name,()=>{trace(name+'.factory');return {send(){},history(){},getMessage(){},dispose(){trace(name+'.dispose');` + dispose + `}}})}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	return path, trace
}
func assertRetiredInstall(t *testing.T, h *V1Host, lease *nodeLease) {
	t.Helper()
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if len(lease.handles) != 0 {
		t.Fatal("failed installation retained handles", lease.handles)
	}
	for _, a := range lease.adapters {
		if !a.stopped || len(a.handles) != 0 {
			t.Fatal("failed installation retained metadata", a)
		}
	}
}
func TestV1AdapterFailedNativeInstallOwnsDisposeAndOriginalError(t *testing.T) {
	path, trace := installReviewPlugin(t, "install-failed", true, true)
	var h *V1Host
	var lease *nodeLease
	var stopped []string
	var mu sync.Mutex
	services := AdapterServices{
		Installed: func(_ context.Context, m AdapterMetadata) error {
			h.lifecycle.Lock()
			lease = h.enabled["install-failed"]
			h.lifecycle.Unlock()
			if strings.HasSuffix(m.ID, ":two") {
				return errors.New("synthetic_install_rejected")
			}
			return nil
		},
		Stopped: func(m AdapterMetadata) { mu.Lock(); defer mu.Unlock(); stopped = append(stopped, m.ID) },
	}
	var e error
	h, e = NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: services})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(context.Background())
	_, e = h.Enable(context.Background(), "install-failed", path, map[string]any{})
	if e == nil || !strings.Contains(e.Error(), "synthetic_install_rejected") || strings.Contains(e.Error(), "synthetic_dispose_failed") {
		t.Fatal(e)
	}
	b, e := os.ReadFile(trace)
	if e != nil || string(b) != "one.factory\ntwo.factory\ntwo.dispose\none.dispose\nsetup.dispose\n" {
		t.Fatal(string(b), e)
	}
	mu.Lock()
	got := strings.Join(stopped, ",")
	mu.Unlock()
	if got != "install-failed:two,install-failed:one" {
		t.Fatal(got)
	}
	assertRetiredInstall(t, h, lease)
	if h.PID() != 0 || len(h.RegisteredAdapters()) != 0 {
		t.Fatal("failed install left host/adapter")
	}
}
func TestV1AdapterDisableDuringNativeInstallOwnsCleanup(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "cooperative", true: "late-reply"}[late], func(t *testing.T) {
			path, trace := installReviewPlugin(t, "install-cancelled", false, false)
			entered, release, stopped := make(chan context.Context, 1), make(chan struct{}), make(chan struct{}, 2)
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var h *V1Host
			var lease *nodeLease
			services := AdapterServices{Installed: func(ctx context.Context, _ AdapterMetadata) error {
				h.lifecycle.Lock()
				lease = h.enabled["install-cancelled"]
				h.lifecycle.Unlock()
				entered <- ctx
				if late {
					<-release
					return nil
				}
				<-ctx.Done()
				return ctx.Err()
			}, Stopped: func(AdapterMetadata) { stopped <- struct{}{} }}
			var e error
			h, e = NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: services})
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				h.Close(context.Background())
			}()
			enabled := make(chan error, 1)
			go func() {
				_, e := h.Enable(context.Background(), "install-cancelled", path, map[string]any{})
				enabled <- e
			}()
			native := awaitRPC(t, entered)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			disabled := make(chan error, 1)
			go func() { disabled <- h.Disable(ctx, "install-cancelled") }()
			select {
			case <-native.Done():
			case <-ctx.Done():
				t.Fatal("native install not cancelled")
			}
			if late {
				select {
				case e := <-disabled:
					t.Fatal("Disable returned before physical Installed callback", e)
				default:
				}
				if len(stopped) != 0 {
					t.Fatal("stopped before pending native install returned")
				}
				close(release)
			}
			if e = awaitRPC(t, disabled); e != nil {
				t.Fatal(e)
			}
			if native.Err() != context.Canceled {
				t.Fatal(native.Err())
			}
			if e = awaitRPC(t, enabled); e == nil {
				t.Fatal("cancelled install enabled")
			}
			b, e := os.ReadFile(trace)
			if e != nil || string(b) != "one.factory\none.dispose\nsetup.dispose\n" {
				t.Fatal(string(b), e)
			}
			assertRetiredInstall(t, h, lease)
			awaitRPC(t, stopped)
			if len(stopped) != 0 {
				t.Fatal("duplicate native stop")
			}
			if h.PID() != 0 || len(h.RegisteredAdapters()) != 0 {
				t.Fatal("cancelled install left host/adapter")
			}
		})
	}
}

func TestV1AdapterIngressKeepsLeaseScopeInsideInvocation(t *testing.T) {
	received := make(chan context.Context, 1)
	f := newRPCFixture(t, AdapterServices{Receive: func(ctx context.Context, _ AdapterMetadata, _ dispatch.SceneRef, _ json.RawMessage) error {
		received <- ctx
		return nil
	}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, e := f.send(ctx, "receive")
	if e != nil || r.Status != dispatch.Sent {
		t.Fatal(r, e)
	}
	native := awaitRPC(t, received)
	cancel()
	if e = native.Err(); e != nil {
		t.Fatal("transport ingress was owned by completed outbound call", e)
	}
	statusResponsive(t, f)
}

func TestV1AdapterSourceLifecycleDifferential(t *testing.T) {
	bytes, e := os.ReadFile("testdata/adapter-source-lifecycle.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		CancelledSend, NextSend  dispatch.Receipt
		Sends, BackgroundIngress int
		HistoryCancellation      string
		DisposeTrace             []string
	}
	if e = json.Unmarshal(bytes, &source); e != nil {
		t.Fatal(e)
	}
	f := newAdapterFixture(t, map[string]any{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan dispatch.Receipt, 1)
	go func() { r, _ := f.send(ctx, "pending", nil); done <- r }()
	waitAdapter(t, func() bool {
		var state struct{ Sends int }
		_ = json.Unmarshal(f.call(t, "inspect", nil), &state)
		return state.Sends == 1
	})
	cancel()
	r := awaitRPC(t, done)
	if r.Status != source.CancelledSend.Status || r.Detail == nil || source.CancelledSend.Detail == nil || *r.Detail != *source.CancelledSend.Detail {
		t.Fatal(r, source.CancelledSend)
	}
	f.call(t, "release", nil)
	r, e = f.send(context.Background(), "healthy", nil)
	if e != nil || r.Status != source.NextSend.Status {
		t.Fatal(r, e)
	}
	var state struct{ Sends int }
	_ = json.Unmarshal(f.call(t, "inspect", nil), &state)
	if state.Sends != source.Sends {
		t.Fatal(state.Sends, source.Sends)
	}
	f.mu.Lock()
	before := len(f.received)
	f.mu.Unlock()
	f.call(t, "context", "receive")
	f.mu.Lock()
	got := len(f.received) - before
	f.mu.Unlock()
	if got != source.BackgroundIngress {
		t.Fatal(got)
	}
	if source.HistoryCancellation != "plugin_disabled" || len(source.DisposeTrace) != 1 || source.DisposeTrace[0] != "adapter.dispose" {
		t.Fatal("source lifecycle oracle changed", source)
	}
}

func TestV1AdapterUnexpectedStartupExitStillFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exit.mjs")
	source := `export default {manifest:{id:'adapter-exit',configSchema:{type:'object'},capabilities:['adapter']},setup(ctx){ctx.registerAdapter('a',()=>({start(){process.exit(17)},send(){},history(){},getMessage(){},dispose(){}}))}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	stopped := make(chan struct{}, 1)
	h, e := NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: AdapterServices{Stopped: func(AdapterMetadata) { stopped <- struct{}{} }}})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, e = h.Enable(ctx, "adapter-exit", path, map[string]any{})
	if e == nil || !strings.Contains(e.Error(), "plugin_worker_exited") {
		t.Fatal("unexpected exit was hidden", e)
	}
	awaitRPC(t, stopped)
	if h.PID() != 0 || len(h.RegisteredAdapters()) != 0 {
		t.Fatal("exited adapter remained published")
	}
}
