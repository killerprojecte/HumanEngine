package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

type adapterFixture struct {
	mu                 sync.Mutex
	installed, stopped []AdapterMetadata
	received           []json.RawMessage
	recalled           map[string]bool
	host               *V1Host
	meta               AdapterMetadata
	scene              dispatch.SceneRef
	message            json.RawMessage
	action             map[string]string
}

func newAdapterFixture(t *testing.T, config map[string]any) *adapterFixture {
	t.Helper()
	f := &adapterFixture{recalled: map[string]bool{}, action: map[string]string{}}
	services := AdapterServices{
		Installed: func(_ context.Context, m AdapterMetadata) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.installed = append(f.installed, m)
			return nil
		},
		Stopped: func(m AdapterMetadata) { f.mu.Lock(); defer f.mu.Unlock(); f.stopped = append(f.stopped, m) },
		Receive: func(_ context.Context, _ AdapterMetadata, _ dispatch.SceneRef, m json.RawMessage) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.received = append(f.received, append(json.RawMessage(nil), m...))
			return nil
		},
		Edit: func(_ context.Context, _ AdapterMetadata, _ dispatch.SceneRef, m, c json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"status":"edited","bodyCoverage":"unavailable","ref":{"eventId":"synthetic-edit","revision":2,"deletionEpoch":0},"previousRef":{"eventId":"synthetic-event","revision":1,"deletionEpoch":0}}`), nil
		},
		Recall: func(_ context.Context, _ AdapterMetadata, s, m string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.recalled[s+"/"+m] = true
			return nil
		},
		IsRecalled: func(_ context.Context, _ AdapterMetadata, s, m string) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.recalled[s+"/"+m], nil
		},
		Media: func(_ context.Context, _ AdapterMetadata, a, s string) (*AdapterMedia, error) {
			return &AdapterMedia{File: "synthetic:" + a, Type: "image/png"}, nil
		},
	}
	var err error
	f.host, err = NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: services})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.host.Close(context.Background()) })
	path, err := filepath.Abs("testdata/adapter-fixture.mjs")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.host.Enable(context.Background(), "adapter-fixture", path, config)
	if err != nil {
		t.Fatal(err)
	}
	var loaded struct {
		Registrations []struct{ Kind, Name, Handle string }
	}
	if err = json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	for _, r := range loaded.Registrations {
		if r.Kind == "action" {
			f.action[r.Name] = r.Handle
		}
	}
	list := f.host.RegisteredAdapters()
	if len(list) != 1 {
		t.Fatal(list)
	}
	f.meta = list[0]
	raw = f.call(t, "context", "scene")
	var data struct {
		Scene   dispatch.SceneRef
		Message json.RawMessage
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	f.scene, f.message = data.Scene, data.Message
	return f
}
func (f *adapterFixture) call(t *testing.T, name string, arg any) json.RawMessage {
	t.Helper()
	r, e := f.host.Call(context.Background(), "adapter-fixture", f.action[name], []any{arg})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f *adapterFixture) send(ctx context.Context, mode string, before func() error) (dispatch.Receipt, error) {
	p, _ := json.Marshal(map[string]string{"type": "text", "text": mode})
	return f.host.AdapterSend(ctx, f.meta.ID, f.meta.Generation, f.scene, dispatch.Segment{Parts: []json.RawMessage{p}}, before)
}
func TestV1AdapterActualContextAndMethods(t *testing.T) {
	f := newAdapterFixture(t, map[string]any{})
	if got, err := f.host.AdapterStatus(context.Background(), f.meta.ID, f.meta.Generation); err != nil || got != "connected" {
		t.Fatal(got, err)
	}
	f.mu.Lock()
	initial := string(f.received[0])
	f.mu.Unlock()
	if strings.Contains(initial, "ignored") {
		t.Fatal(initial)
	}
	if got := string(f.call(t, "context", "invalid")); got != `["invalid_adapter_scene","adapter_scene_mismatch","adapter_bot_flag_required","adapter_ingress_requires_live_origin","invalid_adapter_message","adapter_message_scope_mismatch"]` {
		t.Fatal(got)
	}
	if got := string(f.call(t, "context", "media")); got != `{"file":"synthetic:synthetic-asset","type":"image/png"}` {
		t.Fatal(got)
	}
	if got := string(f.call(t, "context", "edit")); !strings.Contains(got, `"revision":2`) {
		t.Fatal(got)
	}
	f.call(t, "context", "receive")
	f.mu.Lock()
	bot := string(f.received[len(f.received)-1])
	f.mu.Unlock()
	if !strings.Contains(bot, `"senderId":"bot"`) {
		t.Fatal(bot)
	}
	gates := 0
	r, e := f.send(context.Background(), "ok", func() error { gates++; return nil })
	if e != nil || r.Status != dispatch.Sent || gates != 1 {
		t.Fatal(r, e, gates)
	}
	r, e = f.send(context.Background(), "throw", nil)
	if e != nil || r.Status != dispatch.Unknown || dispatch.OutcomeError(r) == nil {
		t.Fatal(r, e)
	}
	r, e = f.send(context.Background(), "invalid", nil)
	if e != nil || r.Status != dispatch.Unknown || r.Detail == nil || *r.Detail != "invalid_send_receipt" {
		t.Fatal(r, e)
	}
	r, e = f.send(context.Background(), "private-detail", nil)
	if e != nil || r.Detail != nil {
		t.Fatal(r, e)
	}
	r, e = f.send(context.Background(), "ok", func() error { return errors.New("synthetic_guard_revoked") })
	if e != nil || r.Status != dispatch.Unknown {
		t.Fatal(r, e)
	}
	history, e := f.host.AdapterHistory(context.Background(), f.meta.ID, f.meta.Generation, f.scene, map[string]any{"limit": 10})
	if e != nil || strings.Contains(string(history), "ignored") || !strings.Contains(string(history), `"origin":"history"`) {
		t.Fatal(string(history), e)
	}
	var message struct{ ID string }
	_ = json.Unmarshal(f.message, &message)
	one, e := f.host.AdapterGetMessage(context.Background(), f.meta.ID, f.meta.Generation, f.scene, message.ID)
	var parsed struct{ Items []json.RawMessage }
	_ = json.Unmarshal(one, &parsed)
	if e != nil || len(parsed.Items) != 1 {
		t.Fatal(string(one), e)
	}
	if string(f.call(t, "context", "recall")) != "true" {
		t.Fatal("recall not synchronous")
	}
	one, e = f.host.AdapterGetMessage(context.Background(), f.meta.ID, f.meta.Generation, f.scene, message.ID)
	_ = json.Unmarshal(one, &parsed)
	if e != nil || len(parsed.Items) != 0 {
		t.Fatal(string(one), e)
	}
	if _, e = f.host.AdapterHistory(context.Background(), f.meta.ID, f.meta.Generation, f.scene, map[string]any{"limit": 10, "cursor": "invalid"}); e == nil || !strings.Contains(e.Error(), "invalid_adapter_history") {
		t.Fatal(e)
	}
	if _, _, e = f.host.invokeAdapter(context.Background(), f.meta.ID, f.meta.Generation, "readImage", []any{f.scene, "synthetic-asset"}, nil); e == nil || e.Error() != "adapter_operation_unsupported" {
		t.Fatal("legacy readImage omission changed", e)
	}
}
func TestV1AdapterCancelDisableAndGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace")
	f := newAdapterFixture(t, map[string]any{"trace": path})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan dispatch.Receipt, 1)
	go func() { r, _ := f.send(ctx, "pending", nil); done <- r }()
	waitAdapter(t, func() bool {
		var v struct{ Sends int }
		_ = json.Unmarshal(f.call(t, "inspect", nil), &v)
		return v.Sends == 1
	})
	cancel()
	if r := <-done; r.Status != dispatch.Unknown {
		t.Fatal(r)
	}
	f.call(t, "release", nil)
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	r, e := f.send(cancelled, "ok", nil)
	if r.Status != dispatch.Cancelled || !errors.Is(e, context.Canceled) {
		t.Fatal(r, e)
	}
	go func() { r, _ := f.send(context.Background(), "pending", nil); done <- r }()
	waitAdapter(t, func() bool {
		var v struct{ Sends int }
		_ = json.Unmarshal(f.call(t, "inspect", nil), &v)
		return v.Sends == 2
	})
	if e = f.host.Disable(context.Background(), "adapter-fixture"); e != nil {
		t.Fatal(e)
	}
	if r = <-done; r.Status != dispatch.Unknown || r.Detail == nil || *r.Detail != "request_cancelled" {
		t.Fatal(r)
	}
	if f.host.PID() != 0 || len(f.host.RegisteredAdapters()) != 0 {
		t.Fatal("disabled adapter alive")
	}
	trace, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(trace), "adapter.dispose\nsetup.dispose\n") {
		t.Fatal(string(trace))
	}
	f.mu.Lock()
	if len(f.stopped) != 1 {
		t.Fatal(f.stopped)
	}
	f.mu.Unlock()
	source, _ := filepath.Abs("testdata/adapter-fixture.mjs")
	if _, e = f.host.Enable(context.Background(), "adapter-fixture", source, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	r, e = f.send(context.Background(), "ok", nil)
	if r.Status != dispatch.Cancelled || !errors.Is(e, ErrNotLoaded) {
		t.Fatal("stale generation accepted", r, e)
	}
}
func waitAdapter(t *testing.T, fn func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for !fn() {
		if time.Now().After(end) {
			t.Fatal("adapter fixture timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestV1AdapterRegistrationValidation(t *testing.T) {
	cases := []struct{ name, capabilities, body string }{{"capability", "[]", "ctx.registerAdapter('a',()=>({}))"}, {"name", "['adapter']", "ctx.registerAdapter('A',()=>({}))"}, {"duplicate", "['adapter']", "ctx.registerAdapter('a',()=>({}));ctx.registerAdapter('a',()=>({}))"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewV1Host("node", io.Discard)
			defer h.Close(context.Background())
			path := filepath.Join(t.TempDir(), "p.mjs")
			src := `export default {manifest:{id:'registration',configSchema:{type:'object'},capabilities:` + tc.capabilities + `},setup(ctx){` + tc.body + `}}`
			_ = os.WriteFile(path, []byte(src), 0600)
			_, e := h.Enable(context.Background(), "registration", path, map[string]any{})
			if e == nil || !strings.Contains(e.Error(), "invalid_adapter_registration") {
				t.Fatal(e)
			}
			if h.PID() != 0 {
				t.Fatal("failed setup retained host")
			}
		})
	}
}

func TestV1AdapterActualSourceDifferential(t *testing.T) {
	bytes, err := os.ReadFile("testdata/adapter-source-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle map[string]json.RawMessage
	if err = json.Unmarshal(bytes, &oracle); err != nil {
		t.Fatal(err)
	}
	f := newAdapterFixture(t, map[string]any{})
	equal := func(key string, actual any) {
		t.Helper()
		a, e := json.Marshal(actual)
		if e != nil {
			t.Fatal(e)
		}
		var av, bv any
		_ = json.Unmarshal(a, &av)
		_ = json.Unmarshal(oracle[key], &bv)
		if !reflect.DeepEqual(av, bv) {
			t.Fatalf("%s differs\nactual %s\noracle %s", key, a, oracle[key])
		}
	}
	equal("scene", f.scene)
	equal("message", f.message)
	f.mu.Lock()
	received := append([]json.RawMessage(nil), f.received...)
	f.mu.Unlock()
	equal("received", received)
	for _, op := range []string{"invalid", "media", "edit"} {
		equal(op, f.call(t, "context", op))
	}
	sends := map[string]dispatch.Receipt{}
	for _, mode := range []string{"ok", "throw", "invalid", "private-detail"} {
		r, e := f.send(context.Background(), mode, nil)
		if e != nil {
			t.Fatal(e)
		}
		sends[mode] = r
	}
	equal("sends", sends)
	history, e := f.host.AdapterHistory(context.Background(), f.meta.ID, f.meta.Generation, f.scene, map[string]any{"limit": 10})
	if e != nil {
		t.Fatal(e)
	}
	equal("history", history)
	var message struct{ ID string }
	_ = json.Unmarshal(f.message, &message)
	one, e := f.host.AdapterGetMessage(context.Background(), f.meta.ID, f.meta.Generation, f.scene, message.ID)
	if e != nil {
		t.Fatal(e)
	}
	equal("getMessage", one)
	equal("recalled", f.call(t, "context", "recall"))
	one, e = f.host.AdapterGetMessage(context.Background(), f.meta.ID, f.meta.Generation, f.scene, message.ID)
	if e != nil {
		t.Fatal(e)
	}
	equal("afterRecall", one)
}

func TestV1AdapterNativeCallbackContextAndConcurrentCaller(t *testing.T) {
	f := newAdapterFixture(t, map[string]any{})
	entered, release := make(chan struct{}), make(chan struct{})
	var reentry error
	f.host.adapterServices.Media = func(ctx context.Context, meta AdapterMetadata, asset, scene string) (*AdapterMedia, error) {
		_, reentry = f.host.AdapterHistory(ctx, meta.ID, meta.Generation, f.scene, map[string]any{"limit": 1})
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &AdapterMedia{File: strings.Repeat("x", 1024*1024), Type: "image/png"}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.host.Call(context.Background(), "adapter-fixture", f.action["context"], []any{"media"})
		done <- e
	}()
	<-entered
	concurrent := make(chan error, 1)
	go func() {
		_, e := f.host.AdapterHistory(context.Background(), f.meta.ID, f.meta.Generation, f.scene, map[string]any{"limit": 1})
		concurrent <- e
	}()
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := <-concurrent; e != nil {
		t.Fatal("unrelated concurrent caller rejected", e)
	}
	if reentry == nil || reentry.Error() != "adapter_native_callback_reentry" {
		t.Fatal(reentry)
	}
}

func TestV1AdapterDisableUnblocksNativeCallback(t *testing.T) {
	f := newAdapterFixture(t, map[string]any{})
	entered, exited := make(chan struct{}), make(chan struct{})
	f.host.adapterServices.Media = func(ctx context.Context, _ AdapterMetadata, asset, scene string) (*AdapterMedia, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		_, e := f.host.Call(context.Background(), "adapter-fixture", f.action["context"], []any{"media"})
		result <- e
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := f.host.Disable(ctx, "adapter-fixture"); e != nil {
		t.Fatal(e)
	}
	<-exited
	if e := <-result; e == nil {
		t.Fatal("retired callback reported success")
	}
	if f.host.PID() != 0 {
		t.Fatal("last adapter disable retained Node")
	}
}

func TestV1AdapterValidationRejectionIsNotUnknownReceipt(t *testing.T) {
	f := newAdapterFixture(t, map[string]any{})
	forged := f.scene
	forged.ID = "forged"
	r, e := f.host.AdapterSend(context.Background(), f.meta.ID, f.meta.Generation, forged, dispatch.Segment{}, nil)
	if e == nil || e.Error() != "adapter_scene_mismatch" || r.Status != "" {
		t.Fatal(r, e)
	}
	r, e = f.host.AdapterSend(context.Background(), f.meta.ID, f.meta.Generation, f.scene, dispatch.Segment{ReplyToID: func() *string { s := "foreign:message"; return &s }()}, nil)
	if e != nil || r.Status != dispatch.Failed || r.Detail == nil || *r.Detail != "quote_scope_mismatch" {
		t.Fatal(r, e)
	}
	var state struct{ Sends int }
	_ = json.Unmarshal(f.call(t, "inspect", nil), &state)
	if state.Sends != 0 {
		t.Fatal("validation transmitted", state)
	}
}

func TestV1AdapterStartupFailureAndDisposalOrder(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	path := filepath.Join(t.TempDir(), "fixture.mjs")
	trace := path + ".trace"
	log, _ := json.Marshal(trace)
	source := `import {appendFileSync} from 'node:fs';const log=(s)=>appendFileSync(` + string(log) + `,s+'\n');export default {manifest:{id:'order',configSchema:{type:'object'},capabilities:['adapter']},async setup(ctx){
 ctx.onDispose(async()=>{await Promise.resolve();log('setup.dispose')});
 for(const name of ['one','two'])ctx.registerAdapter(name,()=>{log(name+'.factory');return {start(){log(name+'.start');if(name==='two')throw Error('synthetic_start_failed')},dispose(){log(name+'.dispose')},send(){},getMessage(){},history(){}}});
 log('setup.done');await Promise.resolve();}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	_, e := h.Enable(context.Background(), "order", path, map[string]any{})
	if e == nil || !strings.Contains(e.Error(), "synthetic_start_failed") {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(trace)
	want := "setup.done\none.factory\ntwo.factory\none.start\ntwo.start\ntwo.dispose\none.dispose\nsetup.dispose\n"
	if string(b) != want {
		t.Fatal(string(b))
	}
	if h.PID() != 0 || len(h.RegisteredAdapters()) != 0 {
		t.Fatal("failed startup retained transport")
	}
}

func TestV1AdapterSyncNativeExceptionAndPendingHistoryCancellation(t *testing.T) {
	f := newAdapterFixture(t, map[string]any{})
	f.host.adapterServices.Media = func(context.Context, AdapterMetadata, string, string) (*AdapterMedia, error) {
		return nil, errors.New("synthetic_media_error")
	}
	_, e := f.host.Call(context.Background(), "adapter-fixture", f.action["context"], []any{"media"})
	if e == nil || !strings.Contains(e.Error(), "synthetic_media_error") {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := f.host.AdapterHistory(ctx, f.meta.ID, f.meta.Generation, f.scene, map[string]any{"limit": 1, "cursor": "pending"})
		done <- e
	}()
	cancel()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	f.call(t, "release", nil)
}
