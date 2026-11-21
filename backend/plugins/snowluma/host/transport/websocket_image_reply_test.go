//go:build bundled_wasm

package snowlumatransport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func TestBuiltinWSImageReplyKeepsDataOutsidePinnedGuest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	host, err := plugins.NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	bundle, err := plugins.BuiltinArtifact("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Enable(ctx, "snowluma", bundle); err != nil {
		t.Fatal(err)
	}

	pixels := make([]byte, 1<<20) // transport data, not a codec fixture
	for i := range pixels {
		pixels[i] = byte(i)
	}
	inline := "base64://" + base64.StdEncoding.EncodeToString(pixels)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			w.WriteHeader(http.StatusUnauthorized)
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
			var action struct {
				Action string
				Echo   string
				Params struct{ File string }
			}
			if err := json.Unmarshal(raw, &action); err != nil {
				t.Error(err)
				return
			}
			body := map[string]any{"echo": action.Echo, "status": "ok", "retcode": 0}
			switch action.Action {
			case "get_login_info":
				body["data"] = map[string]any{"user_id": 42}
			case "get_image":
				switch action.Params.File {
				case "large":
					body["data"] = map[string]string{"url": "", "file": inline}
				case "null":
					body["data"] = nil
				case "missing":
				case "wrong-retcode":
					body["retcode"] = "0"
					body["data"] = map[string]string{"file": "never return"}
				case "unsupported":
					body["retcode"] = 1404
					body["data"] = map[string]string{"file": "never return"}
				case "invalid":
					body["retcode"] = 1400
				case "bad-status":
					delete(body, "status")
				}
			case "get_msg":
				body["data"] = map[string]any{"plain": "ordinary reply"}
			default:
				t.Error("unexpected synthetic action", action.Action)
				return
			}
			wire, err := json.Marshal(body)
			if err != nil {
				t.Error(err)
				return
			}
			if err := socket.Write(ctx, websocket.MessageText, wire); err != nil {
				return
			}
		}
	}))
	defer peer.Close()
	ws, err := NewBuiltinWS(WSConfig{
		URL: "ws" + strings.TrimPrefix(peer.URL, "http"), AccountID: "42", Token: "synthetic",
	}, host, "snowluma", HTTPCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseAndWait(context.Background())

	type observed struct {
		data      bool
		inputSize int
		outSize   int
	}
	policy := ws.guest
	observedEcho := make(chan observed, 16)
	ws.guest = func(ctx context.Context, input []byte) ([]byte, error) {
		output, err := policy(ctx, input)
		value, parseErr := jsjson.ParseBytes(input)
		if parseErr != nil {
			return nil, parseErr
		}
		o, _ := value.(*jsjson.Object)
		if known, _ := o.Get("knownEcho"); known == true {
			event, _ := o.Get("event")
			_, data := event.(*jsjson.Object).Get("data")
			observedEcho <- observed{data: data, inputSize: len(input), outSize: len(output)}
		}
		return output, err
	}
	if err := ws.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	observe := func(t *testing.T) observed {
		t.Helper()
		select {
		case sight := <-observedEcho:
			return sight
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return observed{}
		}
	}
	if boot := observe(t); !boot.data {
		t.Fatal("login did not use the ordinary full-event policy")
	}
	for _, tc := range []struct{ file, wantError string }{
		{"large", ""}, {"null", ""}, {"missing", ""},
		{"wrong-retcode", "platform_action_failed"}, {"unsupported", "unsupported"},
		{"invalid", "invalid_action_parameters"}, {"bad-status", "platform_action_failed"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			value, err := ws.Action(ctx, "get_image", wsObject("file", tc.file))
			if got := fmt.Sprint(err); tc.wantError != "" && (got != tc.wantError || value != nil) || tc.wantError == "" && err != nil {
				t.Fatalf("response contract: error=%v value-present=%v", err, value != nil)
			}
			if sight := observe(t); sight.data || sight.inputSize > 200 || sight.outSize > 200 {
				t.Fatalf("pixel payload crossed policy: %+v", sight)
			}
			if tc.file == "large" {
				file, _ := value.(*jsjson.Object).Get("file")
				withPrefix, _ := file.(string)
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(withPrefix, "base64://"))
				if err != nil || sha256.Sum256(decoded) != sha256.Sum256(pixels) {
					t.Fatal("qualified data changed")
				}
			} else if value != nil {
				t.Fatal("non-data or rejected action returned bytes")
			}
		})
	}
	value, err := ws.Action(ctx, "get_msg", new(jsjson.Object))
	if err != nil {
		t.Fatal(err)
	}
	if ordinary := observe(t); !ordinary.data {
		t.Fatal("non-image action incorrectly used the envelope path")
	}
	plain, _ := value.(*jsjson.Object).Get("plain")
	if plain != "ordinary reply" {
		t.Fatal("ordinary response changed")
	}
}
