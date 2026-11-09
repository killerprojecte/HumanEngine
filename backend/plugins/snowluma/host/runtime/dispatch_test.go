package snowlumaruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

// This synthetic Authority exists only in this test. It deliberately revokes
// final admission on a trace barrier after initial send validation has completed.
type dispatchAuthority struct {
	revoked    atomic.Bool
	finalReads atomic.Int64
}

func (a *dispatchAuthority) Ready() error {
	if a.revoked.Load() {
		a.finalReads.Add(1)
		return errors.New("synthetic_authority_revoked")
	}
	return nil
}
func (a *dispatchAuthority) SceneParticipationEnabled(s botruntime.Scene) (bool, error) {
	return s.ParticipationEnabled, a.Ready()
}
func (a *dispatchAuthority) ValidateSources(botruntime.Scene, []*botruntime.Object, []string) (bool, error) {
	err := a.Ready()
	return err == nil, err
}
func (a *dispatchAuthority) IsRecalled(botruntime.Scene, string) (bool, error) {
	return false, a.Ready()
}
func (a *dispatchAuthority) PromptAuthority(s botruntime.Scene, tools []*botruntime.Object) (any, error) {
	values := []any{}
	for _, tool := range tools {
		values = append(values, tool)
	}
	return obj("scope", "synthetic:"+s.ID, "tools", values), a.Ready()
}
func (a *dispatchAuthority) ProjectDisplay(_ botruntime.Scene, rows []*botruntime.Object, _ []string) ([]*botruntime.Object, error) {
	return rows, a.Ready()
}
func (a *dispatchAuthority) RouteIngress(context.Context, botruntime.Scene, *botruntime.Object) error {
	return a.Ready()
}
func (a *dispatchAuthority) Observe(_ context.Context, _ botruntime.Scene, message, edit *botruntime.Object) (string, *botruntime.Object, error) {
	return "new", message, a.Ready()
}
func (a *dispatchAuthority) Recall(ctx context.Context, s botruntime.Scene, id string, prepare func(botruntime.ReadAuthority) (func() error, error), apply func() error) error {
	finish, err := prepare(a)
	if err != nil {
		return err
	}
	if err = apply(); err != nil {
		return err
	}
	return finish()
}

func runtimeCompletion(name string, args any) botruntime.Completion {
	calls := []replyplan.ToolCall{}
	values := []any{}
	if name != "" {
		calls = append(calls, replyplan.ToolCall{ID: name, Name: name, Arguments: args})
		values = append(values, obj("id", name, "name", name, "arguments", args))
	}
	return botruntime.Completion{Completion: replyplan.Completion{ToolCalls: calls, Assistant: obj("role", "assistant", "content", "", "toolCalls", values)}}
}
func planData(req botruntime.ModelRequest) *jsjson.Object {
	data := obj()
	for _, message := range req.Messages {
		role := str(message, "role")
		if role == "assistant" || role == "tool" {
			break
		}
		if role == "user" {
			parsed, err := jsjson.Parse(str(message, "content"))
			if err != nil {
				continue
			}
			if o, ok := parsed.(*jsjson.Object); ok {
				for _, m := range o.Members() {
					_ = data.Set(m.Key, m.Value)
				}
			}
		}
	}
	return data
}
func newDispatchRuntime(t *testing.T, scenario string, a *dispatchAuthority) *botruntime.Runtime {
	t.Helper()
	dates, err := jsdate.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
	policy, err := replyplan.New(compare, dates)
	if err != nil {
		t.Fatal(err)
	}
	settings := botruntime.DefaultSettings()
	settings.Persona = "Synthetic sender integration"
	settings.DebounceMS = 0
	settings.GroupCooldownMS = 0
	settings.MaxWaitMS = 100
	settings.TopicAdmissionEnabled = true
	var reply atomic.Int64
	var projectedTarget atomic.Value
	projectedTarget.Store("")
	models := botruntime.ModelFunc(func(ctx context.Context, req botruntime.ModelRequest, done func(botruntime.Completion, error)) {
		switch req.Stage {
		case "decision":
			// Models must return the actual prompt projection reference, never
			// the opaque canonical source ID held by the test harness.
			messages, _ := get(planData(req), "messages").([]any)
			if len(messages) != 1 {
				done(botruntime.Completion{}, errors.New("fixture_projected_message_missing"))
				return
			}
			message, ok := messages[0].(*jsjson.Object)
			if !ok || str(message, "id") == "" {
				done(botruntime.Completion{}, errors.New("fixture_projected_message_missing"))
				return
			}
			reference := str(message, "id")
			projectedTarget.Store(reference)
			done(runtimeCompletion("participation.decide", obj("action", "reply", "reasonCode", "fixture", "targetMessageIds", []any{reference})), nil)
		case "plan":
			contract, _ := get(planData(req), "planContract").(*jsjson.Object)
			if contract == nil {
				done(botruntime.Completion{}, errors.New("missing_plan_contract"))
				return
			}
			done(runtimeCompletion("reply.plan", obj("action", "reply", "deliveryTool", "messages.send", "targetMessageIds", get(contract, "targetMessageIds"), "contextMessageIds", get(contract, "contextMessageIds"), "strategy", "Answer the selected synthetic context.", "constraints", []any{}, "facts", []any{}, "assets", []any{}, "emoji", []any{}, "memorySelections", []any{}, "expressionGoals", []any{"Compose a concise reply."}, "actions", []any{})), nil)
		default:
			if reply.Add(1) == 1 {
				done(runtimeCompletion("messages.send", obj("operationId", "synthetic-send", "segments", []any{obj("replyToId", projectedTarget.Load().(string), "parts", []any{obj("type", "text", "text", "synthetic dispatch")})})), nil)
			} else {
				done(runtimeCompletion("", nil), nil)
			}
		}
	})
	runtime, err := botruntime.New(settings, botruntime.Services{Models: models, Policy: policy, Dates: dates, Compare: compare, Authority: a, Trace: func(e botruntime.Event) {
		if scenario == "final-authority" && e.Stage == "adapter_send" && e.Status == "started" {
			a.revoked.Store(true)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtime.CloseAndWait(ctx); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

func TestRegisteredRuntimeDispatchPeers(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "snowluma.wasm")
	build := exec.Command("go", "build", "-buildvcs=false", "-buildmode=c-shared", "-o", artifactPath, "../../wasm")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual guest SHA256 %x", sha256.Sum256(artifact))
	for _, mode := range []string{"http", "ws"} {
		t.Run(mode, func(t *testing.T) {
			// A revoked installation may close its executing guest. Production
			// drains and unloads that generation before enabling its replacement;
			// the next transport must not borrow the revoked generation's module.
			host, err := plugins.NewWASMHost(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := host.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			setup, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			if err = host.Enable(setup, "snowluma", artifact); err != nil {
				t.Fatalf("guest setup: %v (setup context: %v)", err, setup.Err())
			}
			for _, scenario := range []string{"sent", "replacement", "final-authority", "revoke-after-wire"} {
				t.Run(scenario, func(t *testing.T) {
					if !host.Loaded("snowluma") {
						t.Fatalf("guest retired before %s (setup context: %v)", scenario, setup.Err())
					}
					registeredDispatchPeer(t, host, mode, scenario)
					if scenario == "revoke-after-wire" {
						// Make permitted revocation deterministic even when cancellation
						// happens between guest calls rather than during response decoding.
						if err := host.Disable(context.Background(), "snowluma"); err != nil {
							t.Fatal(err)
						}
						if host.Loaded("snowluma") {
							t.Fatal("revoked generation remains loaded")
						}
					} else if !host.Loaded("snowluma") {
						t.Fatalf("non-revocation case %s retired guest (setup context: %v)", scenario, setup.Err())
					}
				})
			}
		})
	}
}
func registeredDispatchPeer(t *testing.T, host *plugins.WASMHost, mode, scenario string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var requests atomic.Int64
	wire := make(chan *jsjson.Object, 2)
	// Native generation cancellation is intentionally not an application turn abort:
	// the outer dispatch must retain uncertainty when the guest can no longer reply.
	life, stop := context.WithCancel(context.Background())
	defer stop()
	var current atomic.Bool
	current.Store(true)
	accept := func(params *jsjson.Object) {
		requests.Add(1)
		wire <- params
		if scenario == "revoke-after-wire" {
			current.Store(false)
			stop()
		}
	}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "http" {
			if r.URL.Path == "/get_login_info" {
				io.WriteString(w, `{"status":"ok","retcode":0,"data":{"user_id":42}}`)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return
			}
			value, err := jsjson.ParseBytes(body)
			if err != nil {
				return
			}
			accept(value.(*jsjson.Object))
			io.WriteString(w, `{"status":"ok","retcode":0,"data":{"message_id":11}}`)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for {
			_, raw, err := socket.Read(ctx)
			if err != nil {
				return
			}
			value, err := jsjson.ParseBytes(raw)
			if err != nil {
				return
			}
			request := value.(*jsjson.Object)
			data := obj("user_id", float64(42))
			if str(request, "action") != "get_login_info" {
				accept(get(request, "params").(*jsjson.Object))
				data = obj("message_id", float64(11))
			}
			response, _ := jsjson.Marshal(obj("status", "ok", "retcode", float64(0), "data", data, "echo", get(request, "echo")))
			if err = socket.Write(ctx, websocket.MessageText, response); err != nil {
				return
			}
		}
	}))
	defer peer.Close()
	makeSender := func(ownerLife context.Context, gate func() bool) *Sender {
		var h *transport.HTTP
		var w *transport.WS
		var err error
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
			t.Fatalf("%s connect: %v (peer context: %v, guest loaded: %t)", mode, err, ctx.Err(), host.Loaded("snowluma"))
		}
		sender, err := NewSender(host, "snowluma", "snowluma", "42", Ports{Life: ownerLife, Current: gate}, h, w)
		if err != nil {
			t.Fatal(err)
		}
		return sender
	}
	sender := makeSender(life, current.Load)
	defer sender.CloseAndWait(context.Background())
	identity, err := sender.call(obj("operation", "identifiers", "adapter", "snowluma", "account", "42", "kind", "group", "peer", "99"))
	if err != nil {
		t.Fatal(err)
	}
	sceneID := str(identity, "sceneId")
	identity, err = sender.call(obj("operation", "identifiers", "adapter", "snowluma", "account", "42", "scene", sceneID, "platform", "5"))
	if err != nil {
		t.Fatal(err)
	}
	sourceID := str(identity, "messageId")
	authority := new(dispatchAuthority)
	runtime := newDispatchRuntime(t, scenario, authority)
	// Exactly one Registry owns this application's SnowLuma adapter, including all
	// replacements. No external AddAdapter/RemoveAdapter competes with this owner.
	registry := NewRegistry(runtime)
	registration, err := registry.RegisterAdapter(sender)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.CloseAndWait(context.Background())
	if scenario == "replacement" {
		replacementLife, stopReplacement := context.WithCancel(context.Background())
		defer stopReplacement()
		replacement := makeSender(replacementLife, func() bool { return replacementLife.Err() == nil })
		defer replacement.CloseAndWait(context.Background())
		next, err := registry.RegisterAdapter(replacement)
		if err != nil {
			t.Fatal(err)
		}
		defer next.CloseAndWait(context.Background())
		// Invoke stale cleanup only after a new owner is installed. The following
		// real runtime turn must still find and use that new registered transport.
		if err = registration.CloseAndWait(ctx); err != nil {
			t.Fatal(err)
		}
		registration.Remove()
	}
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: sceneID, Kind: "group", AdapterID: "snowluma", AccountID: "42", PlatformSceneID: "99"}, ParticipationEnabled: true, LearningEnabled: true, ProactiveEnabled: true}
	if err = runtime.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	topic, err := runtime.Ingest(ctx, sceneID, obj("id", sourceID, "sceneId", sceneID, "senderId", "synthetic-user", "origin", "live", "status", "available", "sentAt", now, "receivedAt", now, "parts", []any{obj("type", "text", "text", "Please respond to this synthetic fixture.")}), false)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Run(sceneID, topic, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-handle.Done:
	}
	result := handle.Wait()
	t.Logf("runtime result status=%s counts=%v receipts=%+v error=%v", result.Status, result.Counts, result.Receipts, result.Err)
	switch scenario {
	case "final-authority":
		if requests.Load() != 0 || authority.finalReads.Load() == 0 || result.Counts["sentSegments"] != 0 {
			t.Fatalf("final authority not enforced: requests=%d checks=%d result=%+v", requests.Load(), authority.finalReads.Load(), result)
		}
		if len(result.Receipts) != 1 || result.Receipts[0].Status != dispatch.Failed {
			t.Fatalf("final-gate receipt missing: %+v", result.Receipts)
		}
	case "revoke-after-wire":
		if requests.Load() != 1 || result.Counts["unknownSegments"] != 1 || result.Counts["sentSegments"] != 0 {
			t.Fatalf("lost conservative uncertainty: requests=%d result=%+v", requests.Load(), result)
		}
		if len(result.Receipts) != 1 || result.Receipts[0].Status != dispatch.Unknown || result.Receipts[0].MessageID != nil {
			t.Fatalf("invented guest receipt after revoke: %+v", result.Receipts)
		}
	default:
		if requests.Load() != 1 || result.Counts["sentSegments"] != 1 {
			t.Fatalf("registered runtime did not send: requests=%d result=%+v", requests.Load(), result)
		}
		select {
		case params := <-wire:
			parts := get(params, "message").([]any)
			raw, _ := jsjson.Marshal(parts)
			if len(parts) != 2 || str(parts[0].(*jsjson.Object), "type") != "reply" || str(get(parts[0].(*jsjson.Object), "data").(*jsjson.Object), "id") != "5" {
				t.Fatalf("canonical reply ID was not restored: %s", raw)
			}
			if !strings.Contains(string(raw), "synthetic dispatch") {
				t.Fatalf("wrong wire %s", raw)
			}
		default:
			t.Fatal("no peer wire")
		}
	}
	if err = registration.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
}
