package externaladapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicimage"
)

func TestOneBotPublicImageRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, source, fileID, chosen string
		status, origin, actions      int
		revoke                       bool
		want                         string
	}{
		{"fresh", "http://8.8.8.8/original", "file-42", "", 200, 1, 0, false, "png"},
		{"expired", "http://8.8.8.8/original", "file-42", "http://8.8.4.4/fresh", 403, 2, 1, false, "png"},
		{"expired-inline", "http://8.8.8.8/original", "file-42", "base64://cG5n", 403, 1, 1, false, "png"},
		{"not-found", "http://8.8.8.8/original", "file-42", "", 404, 1, 0, false, ""},
		{"no-token", "http://8.8.8.8/original", "", "", 403, 1, 0, false, ""},
		{"private-refresh", "http://8.8.8.8/original", "file-42", "http://127.0.0.1/private", 403, 1, 1, false, ""},
		{"revoked-during-action", "http://8.8.8.8/original", "file-42", "http://8.8.4.4/fresh", 403, 1, 1, true, ""},
		{"opaque-cannot-follow-url", "file-42", "file-42", "http://8.8.4.4/fresh", 0, 0, 1, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actions, origin, revoked := 0, 0, false
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct{ File string }
				if r.Method != http.MethodPost || r.URL.Path != "/get_image" || r.Header.Get("Authorization") != "Bearer synthetic" ||
					json.NewDecoder(r.Body).Decode(&input) != nil || input.File != "file-42" {
					t.Error("wrong action, token, or credentials")
					w.WriteHeader(400)
					return
				}
				fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"url":%q}}`, tc.chosen)
			}))
			defer peer.Close()
			s := httpSession{
				life: t.Context(), client: peer.Client(),
				profile: httpProfile{action: peer.URL, onebotHTTP: &oneBotHTTP{token: "synthetic"}},
				call: func(_, _ context.Context, raw []byte) ([]byte, error) {
					actions++
					if !bytes.Contains(raw, []byte(`"operation":"adapterImage"`)) ||
						!bytes.Contains(raw, []byte(fmt.Sprintf(`"url":%q`, tc.chosen))) {
						t.Fatal("incorrect guest image envelope")
					}
					revoked = tc.revoke
					return json.Marshal(map[string]string{"source": tc.chosen})
				},
			}
			s.publicImage = &publicimage.Reader{Dial: func(_ context.Context, network, address string) (net.Conn, error) {
				origin++
				path, host, status := "/original", "8.8.8.8", tc.status
				if origin > 1 {
					path, host, status = "/fresh", "8.8.4.4", 200
				}
				if network != "tcp4" || address != host+":80" {
					return nil, fmt.Errorf("unqualified dial %s %s", network, address)
				}
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					req, err := http.ReadRequest(bufio.NewReader(server))
					if err != nil || req.Method != "GET" || req.Host != host || req.URL.Path != path || len(req.Header) != 0 {
						t.Error("wrong public image request or leaked credentials")
						return
					}
					body := ""
					if status == 200 {
						body = "png"
					}
					fmt.Fprintf(server, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\n\r\n%s", status, http.StatusText(status), len(body), body)
				}()
				return client, nil
			}}
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "synthetic"}}
			asset := "ob:media:" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sceneId":"synthetic","type":"image","file":%q,"fileId":%q}`, tc.source, tc.fileID)))
			guard := func(context.Context) error {
				if revoked {
					return botruntime.ErrDisplayImageUnavailable
				}
				return nil
			}
			if tc.source != "file-42" {
				if s.classifyOneBotImage(scene, asset) != botruntime.NativeImageBrowserDirect {
					t.Fatal("qualified URL did not receive a browser media link")
				}
				if tc.fileID != "" && s.prepareOneBotImage(scene, asset) != nil {
					t.Fatal("paired URL accidentally delegated to vision")
				}
			}
			wrongScene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "other"}}
			if _, err := s.readOneBotImage(t.Context(), wrongScene, asset, guard); err != botruntime.ErrDisplayImageUnavailable {
				t.Fatalf("cross-scene source was not rejected: %v", err)
			}
			got, err := s.readOneBotImage(t.Context(), scene, asset, guard)
			if string(got) != tc.want || (err == nil) != (tc.want != "") || origin != tc.origin || actions != tc.actions {
				t.Fatalf("bytes=%q err=%v public GETs=%d guest actions=%d", got, err, origin, actions)
			}
		})
	}
}
