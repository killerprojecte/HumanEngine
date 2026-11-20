package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

type snowTokenFixture struct {
	encoded     string
	calls       chan string
	release     chan struct{}
	releaseOnce sync.Once
}

func newSnowTokenFixture(t *testing.T) *snowTokenFixture {
	t.Helper()
	wire, err := os.ReadFile("testdata/managed-display-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var image struct{ Base64 string }
	if err := json.Unmarshal(wire, &image); err != nil {
		t.Fatal(err)
	}
	return &snowTokenFixture{encoded: image.Base64, calls: make(chan string, 12), release: make(chan struct{})}
}
func (f *snowTokenFixture) releaseHeld() { f.releaseOnce.Do(func() { close(f.release) }) }
func (f *snowTokenFixture) action(ctx context.Context, token string) (any, bool) {
	switch token {
	case "opaque-inline":
		f.calls <- token
		return map[string]any{"url": "", "file": "base64://" + f.encoded}, true
	case "opaque-url-precedence":
		f.calls <- token
		return map[string]any{"url": "https://example.invalid/no-network", "file": "base64://" + f.encoded}, true
	case "opaque-local-result":
		f.calls <- token
		return map[string]any{"file": "/fixture/never-open.png"}, true
	case "opaque-hold":
		f.calls <- token
		select {
		case <-f.release:
		case <-ctx.Done():
		}
		return map[string]any{"url": "DATA:image/JPEG;base64," + f.encoded, "file": "/fixture/ignored.png"}, true
	}
	return nil, false
}

func (f *snowTokenFixture) assertImages(t *testing.T, ctx context.Context, app *Application, emit func(any)) func() {
	t.Helper()
	defer f.releaseHeld()
	const group = 104
	scene := snow.SceneID("snowluma", "42", "group", strconv.Itoa(group))
	id := func(n int) string { return snow.MessageID(scene, strconv.Itoa(n)) }
	path := "/api/scenes/" + url.PathEscape(scene)
	rows := []map[string]any{
		{"file": "opaque-inline"},
		{"file_id": "opaque-url-precedence"},
		{"file": "opaque-local-result"},
		{"file": "opaque-hold"},
		{"file": "/fixture/never-open.png"},
		{"url": "http://example.invalid/no-network", "file": "opaque-inline"},
	}
	for n, data := range rows {
		emit(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": group,
			"user_id": 7, "message_id": n + 1, "time": time.Now().Unix(), "message": []any{map[string]any{"type": "image", "data": data}}})
	}
	wait := func(condition func() bool) {
		t.Helper()
		limit := time.NewTimer(8 * time.Second)
		defer limit.Stop()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for !condition() {
			select {
			case <-tick.C:
			case <-limit.C:
				t.Fatal("SnowLuma token source did not reach the required retained state")
			}
		}
	}
	wait(func() bool {
		v, err := app.Engine.Runtime.Snapshot(scene)
		return err == nil && len(v.Messages) == len(rows)
	})
	original, err := base64.StdEncoding.DecodeString(f.encoded)
	if err != nil {
		t.Fatal(err)
	}
	get := func(n int, allowed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, "GET", path+"/messages/"+url.PathEscape(id(n))+"/media/0", nil)
		sourceFixtureAuth(req, allowed)
		out := httptest.NewRecorder()
		app.ServeHTTP(out, req)
		return out
	}
	check := func(n, status int, allowed bool) {
		t.Helper()
		response := get(n, allowed)
		if response.Code != status {
			t.Fatalf("token media row %d status=%d want=%d: %s", n, response.Code, status, response.Body)
		}
		if status == 200 && (!bytes.Equal(response.Body.Bytes(), original) || response.Header().Get("Content-Type") != "image/png") {
			t.Fatal("authenticated token fetch did not publish the exact original")
		}
	}
	expectAction := func(token string) {
		t.Helper()
		select {
		case actual := <-f.calls:
			if actual != token {
				t.Fatalf("wrong native action file token: %q want %q", actual, token)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing native get_image action")
		}
	}
	check(1, 401, false)
	check(1, 200, true)
	expectAction("opaque-inline")
	check(2, 503, true)
	expectAction("opaque-url-precedence")
	check(3, 503, true)
	expectAction("opaque-local-result")
	check(5, 404, true)
	check(6, 404, true)
	for _, row := range appRequest(t, app, "GET", path+"/messages", nil).([]any) {
		for _, part := range field(row, "parts").([]any) {
			if field(part, "assetId") != nil || field(part, "mediaUrl") != nil {
				t.Fatal("private token or unresolved public source appeared in preview")
			}
		}
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- get(4, true) }()
	expectAction("opaque-hold")
	emit(map[string]any{"post_type": "notice", "notice_type": "group_recall", "self_id": 42,
		"group_id": group, "message_id": 4, "user_id": 7, "operator_id": 7, "time": time.Now().Unix()})
	wait(func() bool {
		v, err := app.Engine.Runtime.Snapshot(scene)
		if err != nil {
			return false
		}
		for _, row := range v.Messages {
			if field(row, "id") == id(4) {
				return field(row, "status") == "recalled"
			}
		}
		return false
	})
	f.releaseHeld()
	select {
	case out := <-result:
		if out.Code != 404 || bytes.Equal(out.Body.Bytes(), original) {
			t.Fatalf("late recall leaked held inline response: %d %d", out.Code, out.Body.Len())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held request did not settle")
	}
	return func() {
		t.Helper()
		check(1, 404, true)
		select {
		case token := <-f.calls:
			t.Fatalf("unqualified source crossed authenticated get_image: %s", token)
		default:
		}
	}
}
