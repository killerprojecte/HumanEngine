//go:build bundled_wasm

package snowlumaruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

func TestTokenImageActualBundledSender(t *testing.T) {
	setup, stopSetup := context.WithTimeout(context.Background(), 90*time.Second)
	defer stopSetup()
	host, err := plugins.NewWASMHost(setup)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	artifact, err := plugins.BuiltinArtifact("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bundled guest sha256 %x", sha256.Sum256(artifact))
	if err := host.Enable(setup, "snowluma", artifact); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var external atomic.Int32
	notMedia := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { external.Add(1) }))
	defer notMedia.Close()
	calls := make(chan string, 8)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-media-action" {
			http.Error(w, "fixture authorization missing", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, wire, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var request struct {
				Action string
				Params struct{ File string }
				Echo   any
			}
			if json.Unmarshal(wire, &request) != nil {
				return
			}
			var data any
			switch request.Action {
			case "get_login_info":
				data = map[string]any{"user_id": 42}
			case "get_image":
				calls <- request.Params.File
				switch request.Params.File {
				case "fixture-inline":
					data = map[string]any{"url": "", "file": "base64://AAEC"}
				case "fixture-url":
					data = map[string]any{"url": notMedia.URL, "file": "base64://AAEC"}
				case "fixture-path":
					data = map[string]any{"file": "/fixture/never-open.png"}
				case "fixture-null":
					data = nil
				}
			default:
				calls <- request.Action
			}
			response, _ := json.Marshal(map[string]any{"echo": request.Echo, "status": "ok", "retcode": 0, "data": data})
			if err := conn.Write(ctx, websocket.MessageText, response); err != nil {
				return
			}
		}
	}))
	defer func() { cancel(); peer.Close() }()
	ws, err := transport.NewWS(transport.WSConfig{URL: "ws" + strings.TrimPrefix(peer.URL, "http"), AccountID: "42", Token: "synthetic-media-action"}, host, "snowluma", transport.HTTPCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseAndWait(context.Background())
	if err = ws.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	sender, err := NewSender(host, "snowluma", "snowluma", "42", Ports{Life: ctx, Current: func() bool { return true }}, nil, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.CloseAndWait(context.Background())
	scene := botruntime.Scene{}
	scene.ID, scene.AdapterID, scene.AccountID, scene.Kind, scene.PlatformSceneID = snow.SceneID("snowluma", "42", "group", "99"), "snowluma", "42", "group", "99"
	asset := func(file string) string {
		wire, err := jsjson.Marshal(obj("sceneId", scene.ID, "type", "image", "file", file))
		if err != nil {
			t.Fatal(err)
		}
		return "ob:media:" + base64.RawURLEncoding.EncodeToString(wire)
	}
	deny := errors.New("fixture_source_revoked")
	if _, err := sender.readBrowserImage(ctx, scene, asset("fixture-inline"), func(context.Context) error { return deny }); !errors.Is(err, deny) {
		t.Fatalf("native action did not preserve source denial: %v", err)
	}

	check := func(file, wantError string) {
		t.Helper()
		guarded := false
		result, err := sender.readBrowserImage(ctx, scene, asset(file), func(context.Context) error { guarded = true; return nil })
		if !guarded {
			t.Fatal("native transport did not check source before authenticated action")
		}
		if wantError == "" {
			if err != nil || !bytes.Equal(result, []byte{0, 1, 2}) {
				t.Fatalf("native inline token result %v: %v", result, err)
			}
		} else if err == nil || err.Error() != wantError {
			t.Fatalf("native token error = %v, want %s", err, wantError)
		}
		select {
		case actual := <-calls:
			if actual != file {
				t.Fatalf("native get_image token = %q, want %q", actual, file)
			}
		default:
			t.Fatal("missing native get_image action")
		}
	}
	check("fixture-inline", "")
	publicError := "image_download_url_unavailable"
	if publicBrowserImageSupported {
		publicError = "media_address_not_public"
	}
	check("fixture-url", publicError)
	check("fixture-path", "image_download_url_unavailable")
	check("fixture-null", "invalid_payload")
	if external.Load() != 0 || len(calls) != 0 {
		t.Fatalf("unexpected transport activity: public=%d extraAction=%d", external.Load(), len(calls))
	}
}
