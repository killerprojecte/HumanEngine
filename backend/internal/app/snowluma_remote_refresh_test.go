package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestRemoteBrowserImageRefreshesOneExactMessage(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	app, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	const sceneID, adapterID = "refresh", "refresh-adapter"
	scenePath := "/api/scenes/" + sceneID
	scene := map[string]any{
		"id": sceneID, "name": "Refresh fixture", "kind": "group", "adapterId": adapterID, "accountId": "42", "platformSceneId": "100",
		"participationEnabled": false, "learningEnabled": false, "proactiveEnabled": false,
	}
	appRequest(t, app, "PUT", scenePath, scene)
	png, err := base64.StdEncoding.DecodeString(newSnowTokenFixture(t).encoded)
	if err != nil {
		t.Fatal(err)
	}
	const stale, fresh = "ob:media:stale", "ob:media:fresh"
	result := func(id, asset string) *jsjson.Object {
		value, err := jsjson.Parse(fmt.Sprintf(`{"items":[{"id":%q,"sceneId":%q,"status":"available","origin":"history","parts":[{"type":"text","text":""},{"type":"media","mediaType":"image","assetId":%q}]}]}`, id, sceneID, asset))
		if err != nil {
			t.Fatal(err)
		}
		return value.(*jsjson.Object)
	}
	for _, tc := range []struct {
		id          string
		want        int
		replacement string
		interrupt   string
	}{
		{"replacement", http.StatusOK, fresh, ""},
		{"unchanged", http.StatusServiceUnavailable, stale, ""},
		{"recalled", http.StatusNotFound, fresh, "recall"},
		{"rebound", http.StatusNotFound, fresh, "rebind"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			type lookup struct {
				settle func(*jsjson.Object, error)
			}
			calls := make(chan lookup, 3)
			browserReads := make(chan string, 2)
			inlineReads := make(chan string, 2)
			adapter := &botruntime.Adapter{
				InlineImage: &botruntime.InlineImageAdapter{
					Current: func() bool { return true },
					Classify: func(_ botruntime.Scene, asset string) botruntime.NativeImageLink {
						if asset == fresh {
							return botruntime.NativeImageInline
						}
						return botruntime.NativeImageBrowserDirect
					},
					Read: func(_ context.Context, _ botruntime.Scene, asset string) ([]byte, error) {
						inlineReads <- asset
						return png, nil
					},
				},
				BrowserMessage: func(ctx context.Context, captured botruntime.Scene, id string, before func(context.Context) error, settle func(*jsjson.Object, error)) {
					if err := before(ctx); err != nil {
						settle(nil, err)
						return
					}
					if captured.ID != sceneID || captured.AdapterID != adapterID || captured.PlatformSceneID != "100" || id != tc.id {
						settle(nil, fmt.Errorf("incorrect replacement lookup: %+v / %s", captured, id))
						return
					}
					calls <- lookup{settle}
				},
				BrowserImage: func(ctx context.Context, _ botruntime.Scene, asset string, before func(context.Context) error) ([]byte, error) {
					if err := before(ctx); err != nil {
						return nil, err
					}
					browserReads <- asset
					return nil, fmt.Errorf("public_http_403: %w", botruntime.ErrBrowserImageLocatorExpired)
				},
			}
			app.Engine.Runtime.AddAdapter(adapterID, adapter)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, scenePath+"/messages/"+tc.id+"/media/1", nil).WithContext(ctx)
				sourceFixtureAuth(req, true)
				app.ServeHTTP(w, req)
				response <- w
			}()
			waitLookup := func(phase string) lookup {
				t.Helper()
				select {
				case call := <-calls:
					return call
				case out := <-response:
					t.Fatalf("%s was skipped: HTTP %d %s", phase, out.Code, out.Body.String())
				case <-ctx.Done():
					t.Fatalf("missing %s: %v", phase, ctx.Err())
				}
				return lookup{}
			}
			waitLookup("initial exact lookup").settle(result(tc.id, stale), nil)
			replacement := waitLookup("replacement exact lookup")
			switch tc.interrupt {
			case "recall":
				if err := app.Engine.Runtime.Recall(ctx, sceneID, tc.id); err != nil {
					t.Fatal(err)
				}
			case "rebind":
				scene["platformSceneId"] = "101"
				appRequest(t, app, "PUT", scenePath, scene)
			}
			replacement.settle(result(tc.id, tc.replacement), nil)
			select {
			case out := <-response:
				if out.Code != tc.want || (tc.want == http.StatusOK && !bytes.Equal(out.Body.Bytes(), png)) {
					t.Fatalf("expected HTTP %d, got %d (%d bytes)", tc.want, out.Code, out.Body.Len())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if len(calls) != 0 || len(browserReads) != 1 || <-browserReads != stale {
				t.Fatal("unexpected platform lookup or stale locator read")
			}
			if tc.want == http.StatusOK {
				if len(inlineReads) != 1 || <-inlineReads != fresh {
					t.Fatal("missing single qualified replacement read")
				}
			} else if len(inlineReads) != 0 {
				t.Fatal("unqualified replacement reached the reader")
			}
		})
	}
}
