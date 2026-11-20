package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestRemoteHistoryActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/remote-history-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scene, Live, Success, Timeout, InvalidTimestamp json.RawMessage
		Retained                                        []json.RawMessage
		Recall, Bot                                     string
		Cases                                           []struct {
			Mode, Method, Path string
			Status             int
			Body               any
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	object := func(raw json.RawMessage) *jsjson.Object {
		t.Helper()
		value, err := jsjson.ParseBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		return value.(*jsjson.Object)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	app, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	appRequest(t, app, "PUT", "/api/scenes/fixture", fixture.Scene)
	ingest := func(raw json.RawMessage) {
		t.Helper()
		if _, err := app.Engine.Runtime.Ingest(t.Context(), "fixture", object(raw), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range fixture.Retained {
		ingest(raw)
	}
	type historyCall struct {
		ctx    context.Context
		scene  botruntime.Scene
		limit  int
		settle func(*jsjson.Object, error)
	}
	calls := make(chan historyCall, 1)
	adapter := &botruntime.Adapter{
		BotUserID: fixture.Bot,
		History: func(ctx context.Context, scene botruntime.Scene, limit int, settle func(*jsjson.Object, error)) {
			calls <- historyCall{ctx, scene, limit, settle}
		},
	}
	app.Engine.Runtime.AddAdapter("fixture", adapter)
	request := func(ctx context.Context, method, path string) <-chan *httptest.ResponseRecorder {
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(method, path, nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer synthetic-token")
			recorder := httptest.NewRecorder()
			app.ServeHTTP(recorder, req)
			response <- recorder
		}()
		return response
	}
	awaitCall := func(t *testing.T, ctx context.Context) historyCall {
		t.Helper()
		select {
		case call := <-calls:
			if call.limit != 30 || call.scene.ID != "fixture" || call.scene.AdapterID != "fixture" ||
				call.scene.PlatformSceneID != "group" || call.scene.AccountID != "bot" || call.scene.Kind != "group" ||
				call.scene.DisplayName == nil || *call.scene.DisplayName != "Fixture" ||
				call.scene.AvatarURL == nil || *call.scene.AvatarURL != "https://synthetic.invalid/avatar" {
				call.settle(nil, context.Canceled)
				t.Fatalf("wrong platform read: %+v", call)
			}
			return call
		case <-ctx.Done():
			t.Fatal("request did not reach adapter:", ctx.Err())
		}
		return historyCall{}
	}
	awaitResponse := func(t *testing.T, ctx context.Context, response <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case recorder := <-response:
			return recorder
		case <-ctx.Done():
			t.Fatal("history request did not return:", ctx.Err())
		}
		return nil
	}
	// Cancel one pending request while a second one remains live. Retire the
	// canceled callback after the handler returns to exercise tracked ownership.
	t.Run("request cancellation", func(t *testing.T) {
		ctx, finish := context.WithTimeout(t.Context(), 10*time.Second)
		defer finish()
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		aborted := request(cancelled, "GET", "/api/scenes/fixture/history")
		first := awaitCall(t, ctx)
		defer first.settle(nil, context.Canceled)
		active := request(ctx, "GET", "/api/scenes/fixture/history")
		second := awaitCall(t, ctx)
		defer second.settle(nil, context.Canceled)
		cancel()
		if recorder := awaitResponse(t, ctx, aborted); recorder.Body.Len() != 0 {
			t.Fatal("canceled request published a body")
		}
		if first.ctx.Err() != context.Canceled || second.ctx.Err() != nil {
			t.Fatal("request cancellation was not scoped to the correct adapter read")
		}
		second.settle(object(fixture.Timeout), nil)
		if recorder := awaitResponse(t, ctx, active); recorder.Code != 200 {
			t.Fatal("independent request did not remain live:", recorder.Code)
		}
	})
	for _, tc := range fixture.Cases {
		t.Run(tc.Mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if tc.Mode == "no-adapter" {
				app.Engine.Runtime.RemoveAdapter("fixture")
			}
			pending := request(ctx, tc.Method, tc.Path)
			switch tc.Mode {
			case "success":
				call := awaitCall(t, ctx)
				defer call.settle(nil, context.Canceled)
				if err := app.Engine.Runtime.Recall(ctx, "fixture", fixture.Recall); err != nil {
					t.Fatal(err)
				}
				ingest(fixture.Live)
				call.settle(object(fixture.Success), nil)
			case "timeout", "invalid-timestamp":
				call := awaitCall(t, ctx)
				defer call.settle(nil, context.Canceled)
				page := fixture.Timeout
				if tc.Mode == "invalid-timestamp" {
					page = fixture.InvalidTimestamp
				}
				call.settle(object(page), nil)
			}
			response := awaitResponse(t, ctx, pending)
			if response.Code != tc.Status {
				t.Fatalf("got %d want %d: %s", response.Code, tc.Status, response.Body)
			}
			var got any = response.Body.String()
			if tc.Method != "HEAD" {
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
			}
			if tc.Mode == "invalid-timestamp" {
				delete(got.(map[string]any), "requestId")
			}
			if !reflect.DeepEqual(got, tc.Body) {
				t.Fatalf("body mismatch: got %s; want %+v", response.Body, tc.Body)
			}
		})
	}
	t.Run("scene remapped while adapter is pending", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		app.Engine.Runtime.AddAdapter("fixture", adapter)
		pending := request(ctx, "GET", "/api/scenes/fixture/history")
		call := awaitCall(t, ctx)
		defer call.settle(nil, context.Canceled)

		replacement := object(fixture.Scene)
		_ = replacement.Set("platformSceneId", "remapped-group")
		wire, err := jsjson.Marshal(replacement)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, app, "PUT", "/api/scenes/fixture", json.RawMessage(wire))
		call.settle(object(fixture.Success), nil)
		if response := awaitResponse(t, ctx, pending); response.Code != 503 {
			t.Fatalf("stale platform rows survived scene remap: %d %s", response.Code, response.Body)
		}
	})
}
