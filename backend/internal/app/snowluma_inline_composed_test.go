package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	snowhost "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/runtime"
)

// This composition exercises the exact bundled guest and HTTP path without
// recompiling transient manager policy guests for configuration or replacement.
func TestSnowLumaInlineImageComposed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var owners Owners
	options := applicationOptions(t)
	options.Entries = []string{}
	counter := &inlineVisionCounter{entered: make(chan struct{}), release: make(chan struct{})}
	options.Counter = counter
	options.relationExposurePort = func(a *Application, o Owners) (*memoryservice.ExposurePort, error) {
		owners = o
		return memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	}
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	repository, err := ingressrouting.NewSQLiteRepository(owners.Main, owners.Authority)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ingressrouting.New(repository)
	if err != nil {
		t.Fatal(err)
	}
	const guest = "snowluma-inline-composed"
	artifact, err := plugins.BuiltinArtifact("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("compile and instantiate pinned SnowLuma guest")
	if err := app.WASM.Enable(ctx, guest, artifact); err != nil {
		t.Fatal(err)
	}
	call, err := app.WASM.BindCall(guest)
	if err != nil {
		t.Fatal(err)
	}
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))

	connections := make(chan *websocket.Conn, 2)
	unexpected := make(chan string, 8)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			http.Error(w, "missing fixture token", http.StatusUnauthorized)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		connections <- socket
		for {
			_, wire, err := socket.Read(ctx)
			if err != nil {
				return
			}
			var request struct {
				Action string
				Echo   any
			}
			if json.Unmarshal(wire, &request) != nil {
				return
			}
			var data any = map[string]any{}
			switch request.Action {
			case "get_login_info":
				data = map[string]any{"user_id": 42}
			case "get_group_msg_history":
				messages := []any{}
				for _, id := range []int{2, 900} {
					messages = append(messages, map[string]any{
						"message_type": "group", "group_id": 100, "user_id": 7, "message_id": id, "time": time.Now().Unix(),
						"message": []any{map[string]any{"type": "image", "data": map[string]any{"base64": "YQ=="}}},
					})
				}
				data = map[string]any{"messages": messages}
			case "get_msg", "get_image":
				unexpected <- request.Action
			}
			out, _ := json.Marshal(map[string]any{"echo": request.Echo, "status": "ok", "retcode": 0, "data": data})
			if err := socket.Write(ctx, websocket.MessageText, out); err != nil {
				return
			}
		}
	}))
	defer func() { cancel(); peer.Close() }()
	config, err := json.Marshal(map[string]string{
		"url": "ws" + strings.TrimPrefix(peer.URL, "http"), "accountId": "42", "transport": "websocket",
	})
	if err != nil {
		t.Fatal(err)
	}
	ingress := snowhost.EngineIngress{Engine: app.Engine, Compare: owners.Compare, Identity: identity}
	factory := snowhost.Factory(snowhost.InstallationPorts{
		Registry: app.snowRegistry, Sender: snowhost.Ports{ReadOriginal: snowhost.OriginalReader(app.Engine)},
		BuildReceive: ingress.ReceiverFactory(func() {}, time.Now), Stopped: app.snowStopped,
		Error: func(code string, err error) { t.Logf("native fixture transport %s: %v", code, err) },
	}, func(string) (string, bool) { return "", false })
	activate := func(revision uint64) (pluginmanager.BuiltinLifetime, *websocket.Conn) {
		t.Helper()
		t.Logf("connect authenticated native generation %d", revision)
		life, stop := context.WithCancel(ctx)
		t.Cleanup(stop)
		owner, err := factory(ctx, pluginmanager.BuiltinRuntimeInput{
			ID: guest, Revision: revision, Config: config, Life: life, Host: app.WASM, CallGuest: call,
			Current: func() bool { return life.Err() == nil },
			Secret: func(_ context.Context, key string) (string, bool, error) {
				if key == "token" {
					return "synthetic", true, nil
				}
				return "", false, nil
			},
		})
		if owner != nil {
			t.Cleanup(func() { _ = owner.CloseAndWait(context.Background()) })
		}
		if err != nil {
			t.Fatalf("native activation %d: %v", revision, err)
		}
		select {
		case socket := <-connections:
			return owner, socket
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil, nil
		}
	}
	first, socket := activate(1)
	defer first.CloseAndWait(context.Background())
	t.Log("exercise native ingress and original-byte browser route")
	emit := func(event any) {
		t.Helper()
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := socket.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal(err)
		}
	}
	verifyRetired := assertSnowLumaInlineImageActualIngress(t, ctx, app, emit, unexpected, true)
	assertInlineVisionModel(t, ctx, app, emit, counter)
	if err := first.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := activate(2)
	defer second.CloseAndWait(context.Background())
	verifyRetired()
}
