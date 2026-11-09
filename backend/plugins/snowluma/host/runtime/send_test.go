package snowlumaruntime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

func TestActualGuestPreparedSendPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("guest build: %v %s", err, out)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual guest sha256 %x", sha256.Sum256(artifact))
	// Compile and instantiate once outside each peer's IO deadline. SnowLuma's
	// core has no mutable package state: every continuation is explicit JSON and
	// ABI allocations are freed by BindCall. Adapters, resolvers, peers, files,
	// cancellation lifetimes and generation gates are still new in every case.
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer setupCancel()
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err = host.Enable(setupCtx, "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"http", "ws"} {
		for _, scenario := range []string{"sent", "fallback", "resolver-throws", "missing-receipt", "reject", "source-revoked", "missing-validator", "missing-validator-bad-part", "generation-revoked", "cancel-before", "cancel-after-transmit"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				if !host.Loaded("snowluma") {
					t.Fatal("previous case retired the shared stateless guest")
				}
				testPreparedPeer(t, host, mode, scenario)
				if !host.Loaded("snowluma") {
					t.Fatal("case retired the shared stateless guest")
				}
			})
		}
	}
}
func testPreparedPeer(t *testing.T, host *plugins.WASMHost, mode, scenario string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	payload := []byte{0, 1, 2, 253, 254, 255}
	hash := sha256.Sum256(payload)
	file := filepath.Join(t.TempDir(), "image.bin")
	if err = os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int64
	entered := make(chan struct{}, 1)
	received := make(chan *jsjson.Object, 1)
	response := func() map[string]any {
		if scenario == "reject" {
			return map[string]any{"status": "failed", "retcode": 1404, "data": nil}
		}
		data := map[string]any{"message_id": float64(11)}
		if scenario == "missing-receipt" {
			data = map[string]any{}
		}
		return map[string]any{"status": "ok", "retcode": 0, "data": data}
	}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "http" {
			if r.URL.Path == "/get_login_info" {
				io.WriteString(w, `{"status":"ok","retcode":0,"data":{"user_id":42}}`)
				return
			}
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				return
			}
			v, e := jsjson.ParseBytes(raw)
			if e != nil {
				return
			}
			sends.Add(1)
			received <- v.(*jsjson.Object)
			entered <- struct{}{}
			if scenario == "cancel-after-transmit" {
				<-r.Context().Done()
				return
			}
			_ = json.NewEncoder(w).Encode(response())
			return
		}
		socket, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer socket.CloseNow()
		for {
			_, raw, e := socket.Read(ctx)
			if e != nil {
				return
			}
			v, e := jsjson.ParseBytes(raw)
			if e != nil {
				return
			}
			request := v.(*jsjson.Object)
			var reply map[string]any
			if str(request, "action") == "get_login_info" {
				reply = map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"user_id": 42}}
			} else {
				sends.Add(1)
				received <- get(request, "params").(*jsjson.Object)
				entered <- struct{}{}
				if scenario == "cancel-after-transmit" {
					continue
				}
				reply = response()
			}
			reply["echo"] = get(request, "echo")
			wire, _ := json.Marshal(reply)
			if e = socket.Write(ctx, websocket.MessageText, wire); e != nil {
				return
			}
		}
	}))
	defer peer.Close()
	var h *transport.HTTP
	var w *transport.WS
	if mode == "http" {
		h, err = transport.NewHTTP(transport.HTTPConfig{URL: peer.URL, AccountID: "42", Token: "synthetic", EventToken: "synthetic", Timeout: time.Second}, host, "snowluma", transport.HTTPCallbacks{})
		if err == nil {
			err = h.Connect(ctx)
		}
	} else {
		w, err = transport.NewWS(transport.WSConfig{URL: "ws" + strings.TrimPrefix(peer.URL, "http"), AccountID: "42", Token: "synthetic", Timeout: time.Second}, host, "snowluma", transport.HTTPCallbacks{})
		if err == nil {
			err = w.Connect(ctx)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	life, stop := context.WithCancel(context.Background())
	defer stop()
	var current atomic.Bool
	current.Store(true)
	trace := []string{}
	sender, err := NewSender(host, "snowluma", "snowluma", "42", Ports{Life: life, Current: current.Load,
		ResolveSource: func(context.Context, any, *jsjson.Object) (*Source, error) {
			trace = append(trace, "source")
			if scenario == "fallback" {
				return nil, nil
			}
			if scenario == "resolver-throws" {
				return nil, errors.New("synthetic_resolver")
			}
			source := &Source{Value: obj("type", "image", "path", file, "bytes", float64(len(payload)), "hash", hex.EncodeToString(hash[:])), Validate: func() error {
				trace = append(trace, "validate")
				if scenario == "source-revoked" {
					return errors.New("synthetic_revoked")
				}
				return nil
			}}
			if strings.HasPrefix(scenario, "missing-validator") {
				source.Validate = nil
			}
			return source, nil
		}, ResolveMedia: func(context.Context, any, *jsjson.Object) (*jsjson.Object, error) {
			trace = append(trace, "media")
			return obj("type", "image", "file", "https://example.invalid/synthetic.png"), nil
		}}, h, w)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.CloseAndWait(context.Background())
	ids, err := sender.call(obj("operation", "identifiers", "adapter", "snowluma", "account", "42", "kind", "group", "peer", "99", "platform", "11"))
	if err != nil {
		t.Fatal(err)
	}
	scene := obj("id", get(ids, "sceneId"), "kind", "group", "adapterId", "snowluma", "accountId", "42", "platformSceneId", "99")
	// Obtain the expected scoped receipt from this actual guest, not native code.
	expected, err := sender.call(obj("operation", "identifiers", "adapter", "snowluma", "account", "42", "scene", get(ids, "sceneId"), "platform", "11"))
	if err != nil {
		t.Fatal(err)
	}
	segment := obj("parts", []any{obj("type", "text", "text", "hello"), obj("type", "media", "assetId", "managed"), obj("type", "text", "text", "tail")})
	if scenario == "missing-validator-bad-part" {
		parts := get(segment, "parts").([]any)
		_ = segment.Set("parts", append(parts, obj("type", "unsupported")))
	}
	requestCtx, abort := context.WithCancel(ctx)
	defer abort()
	if scenario == "cancel-before" {
		abort()
	}
	if scenario == "cancel-after-transmit" {
		go func() {
			select {
			case <-entered:
				abort()
			case <-ctx.Done():
			}
		}()
	}
	outcome, err := sender.Send(requestCtx, scene, segment, func() error {
		trace = append(trace, "before")
		if scenario == "generation-revoked" {
			current.Store(false)
		}
		return nil
	})
	if scenario == "generation-revoked" {
		if err == nil {
			t.Fatalf("revoked generation returned receipt: %#v", outcome)
		}
		if sends.Load() != 0 {
			t.Fatal("revoked generation transmitted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// This fixture is produced by the unmodified TypeScript adapter against local
	// peers, including its actual encoder and transport error precedence.
	oracleRaw, e := os.ReadFile("testdata/send-peers-source.json")
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := jsjson.ParseBytes(oracleRaw)
	if e != nil {
		t.Fatal(e)
	}
	var oracle *jsjson.Object
	for _, row := range parsed.([]any) {
		r := row.(*jsjson.Object)
		if str(r, "mode") == mode && str(r, "scenario") == scenario {
			oracle = r
			break
		}
	}
	if oracle == nil {
		t.Fatal("missing actual source row")
	}
	actualReceipt, _ := jsjson.Stringify(get(outcome, "receipt"))
	sourceReceipt, _ := jsjson.Stringify(get(oracle, "receipt"))
	if actualReceipt != sourceReceipt {
		t.Fatalf("source receipt %s; guest %s", sourceReceipt, actualReceipt)
	}
	wantTrace := []string{}
	for _, v := range get(oracle, "trace").([]any) {
		wantTrace = append(wantTrace, v.(string))
	}
	if !reflect.DeepEqual(trace, wantTrace) {
		t.Fatalf("trace source %v actual %v", wantTrace, trace)
	}
	receipt := get(outcome, "receipt").(*jsjson.Object)
	status, detail := "sent", ""
	switch scenario {
	case "missing-validator-bad-part":
		status, detail = "failed", "unsupported_message_part"
	case "resolver-throws":
		status, detail = "failed", "send_failed"
	case "missing-receipt":
		status, detail = "unknown", "missing_send_receipt"
	case "reject":
		status, detail = "failed", "unsupported"
	case "source-revoked", "missing-validator":
		status, detail = "failed", "http_request_failed"
		if mode == "ws" {
			detail = "socket_send_failed"
		}
	case "cancel-before":
		status, detail = "cancelled", "cancelled_before_send"
	case "cancel-after-transmit":
		status, detail = "unknown", "request_cancelled"
	}
	if str(receipt, "status") != status || str(receipt, "detail") != detail {
		wire, _ := jsjson.Stringify(receipt)
		t.Fatalf("receipt %s; want %s/%s", wire, status, detail)
	}
	if scenario == "sent" && get(receipt, "messageId") != get(expected, "messageId") {
		t.Fatal("receipt identity changed")
	}
	if scenario == "cancel-before" {
		if len(trace) != 0 || sends.Load() != 0 {
			t.Fatal("cancel-before touched resolver/peer")
		}
		return
	}
	if scenario == "resolver-throws" || scenario == "missing-validator-bad-part" {
		if sends.Load() != 0 {
			t.Fatal("resolver failure transmitted")
		}
		return
	}
	expectedTrace := []string{"source", "before", "validate"}
	if scenario == "missing-validator" {
		expectedTrace = []string{"source", "before"}
	}
	if scenario == "fallback" {
		expectedTrace = []string{"source", "media", "before"}
	}
	if !reflect.DeepEqual(trace, expectedTrace) {
		t.Fatalf("source order %v", trace)
	}
	if scenario == "source-revoked" || scenario == "missing-validator" {
		if sends.Load() != 0 {
			t.Fatal("revoked source transmitted")
		}
		return
	}
	select {
	case params := <-received:
		requests := get(oracle, "requests").([]any)
		actualWire, _ := jsjson.Stringify(params)
		if len(requests) != 1 {
			t.Fatalf("source requests %d", len(requests))
		}
		expectedWire, _ := jsjson.Stringify(requests[0])
		if actualWire != expectedWire {
			t.Fatalf("source wire %s actual %s", expectedWire, actualWire)
		}
		parts := get(params, "message").([]any)
		media := get(parts[1].(*jsjson.Object), "data").(*jsjson.Object)
		expectedFile := "base64://" + base64.StdEncoding.EncodeToString(payload)
		if scenario == "fallback" {
			expectedFile = "https://example.invalid/synthetic.png"
		}
		if str(media, "file") != expectedFile {
			t.Fatal("media wire mismatch")
		}
		if len(parts) != 3 || get(params, "group_id") != "99" {
			t.Fatal("message envelope changed")
		}
	case <-ctx.Done():
		t.Fatal("peer never received action")
	}
	if err = sender.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
}
