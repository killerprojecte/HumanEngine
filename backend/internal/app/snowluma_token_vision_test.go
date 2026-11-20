package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

type opaqueVisionTestBarrier struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *opaqueVisionTestBarrier) releaseHeld() { b.once.Do(func() { close(b.release) }) }

// The actual bundled Sender transport is covered independently. This arm
// drives the app and native model wire without compiling a WASM instance.
func TestOpaqueVisionNativeApplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	counter := &inlineVisionCounter{entered: make(chan struct{}), release: make(chan struct{})}
	options := applicationOptions(t)
	options.Entries, options.Counter = []string{}, counter
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	barrier := &opaqueVisionTestBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	defer barrier.releaseHeld()
	root, release, err := app.Engine.RootContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	wire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(wire))

	fixture := newSnowTokenFixture(t)
	original, err := base64.StdEncoding.DecodeString(fixture.encoded)
	if err != nil {
		t.Fatal(err)
	}
	file := func(locator string) string {
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(locator, "ob:media:"))
		if err != nil {
			return ""
		}
		body, err := jsjson.ParseBytes(decoded)
		if err != nil {
			return ""
		}
		value, _ := field(body, "file").(string)
		return value
	}
	var tokenCalls, inlineReads atomic.Int32
	adapter := &botruntime.Adapter{
		InlineImage: &botruntime.InlineImageAdapter{
			Current: func() bool { return true },
			Classify: func(_ botruntime.Scene, locator string) botruntime.NativeImageLink {
				switch value := file(locator); {
				case strings.HasPrefix(value, "base64://"):
					return botruntime.NativeImageInline
				case strings.HasPrefix(value, "https:"):
					return botruntime.NativeImageBrowserDirect
				default:
					return 0
				}
			},
			Read: func(context.Context, botruntime.Scene, string) ([]byte, error) {
				inlineReads.Add(1)
				return bytes.Clone(original), nil
			},
		},
		PrepareImageToken: func(scene botruntime.Scene, locator string) botruntime.GuardedImageRead {
			if file(locator) != "opaque-"+scene.PlatformSceneID {
				return nil
			}
			return func(call context.Context, guard func(context.Context) error) ([]byte, error) {
				if err := guard(call); err != nil {
					return nil, err
				}
				tokenCalls.Add(1)
				if scene.PlatformSceneID == "104" {
					close(barrier.entered)
					<-barrier.release
				}
				return bytes.Clone(original), nil
			}
		},
	}
	runtime := app.Engine.Runtime
	runtime.AddAdapter("snowluma", adapter)
	emit := func(event any) {
		t.Helper()
		wire, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsjson.ParseBytes(wire)
		if err != nil {
			t.Fatal(err)
		}
		raw := value.(*jsjson.Object)
		group := strconv.Itoa(int(field(raw, "group_id").(float64)))
		id := strconv.Itoa(int(field(raw, "message_id").(float64)))
		scene := botruntime.Scene{SceneRef: dispatch.SceneRef{
			ID: snow.SceneID("snowluma", "42", "group", group), AdapterID: "snowluma", AccountID: "42", Kind: "group", PlatformSceneID: group,
		}}
		if field(raw, "post_type") == "notice" {
			if err := runtime.Recall(root, scene.ID, snow.MessageID(scene.ID, id)); err != nil {
				t.Fatal(err)
			}
			return
		}
		row, _, err := snow.Normalize("snowluma", "42", raw, "live", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.IngestNativeMedia(root, adapter, scene, row, false); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if tokens, inline := tokenCalls.Load(), inlineReads.Load(); tokens != 3 || inline != 0 {
			t.Errorf("wrong priority or extra IO (token=%d inline=%d; want only three permitted tokens)", tokens, inline)
		}
	})
	assertInlineVisionModel(t, ctx, app, emit, counter, barrier)
}
