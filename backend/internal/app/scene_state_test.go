package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSceneStateHTTPContract(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	app, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	scene := map[string]any{"id": "fixture", "adapterId": "fixture", "accountId": "bot", "platformSceneId": "group", "kind": "group", "name": "fixture", "participationEnabled": false, "learningEnabled": false, "proactiveEnabled": false}
	appRequest(t, app, "PUT", "/api/scenes/fixture", scene)
	for _, tc := range []struct {
		method, path, token string
		code                int
		body                string
	}{
		{"GET", "/api/scenes/fixture/state", "synthetic-token", 200, `{"activeTasks":0,"secondsSinceBotSpoke":null,"topicRouting":{"enabled":false,"deferred":0,"overflowed":0,"provisional":0,"maxTopics":4,"maxDeferred":60},"topics":[]}`},
		{"HEAD", "/api/scenes/fixture/state", "synthetic-token", 200, ""},
		{"GET", "/api/scenes/missing/state", "synthetic-token", 400, `{"error":"Scene unavailable"}`},
		{"HEAD", "/api/scenes/missing/state", "synthetic-token", 400, ""},
		{"GET", "/api/scenes/missing/state", "wrong", 401, `"error":"Authentication required"`},
		{"HEAD", "/api/scenes/missing/state", "wrong", 401, ""},
	} {
		t.Run(tc.method+tc.path+tc.token, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, req)
			if w.Code != tc.code || tc.body != "" && !strings.Contains(w.Body.String(), tc.body) || tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatalf("status/body: %d %s", w.Code, w.Body)
			}
			if tc.code != 401 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Header())
			}
		})
	}
}
