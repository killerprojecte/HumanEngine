package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"slices"
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
	direct, batch    bool
	cacheProbe       bool
	plannerProbe     bool
	fresh            atomic.Bool
	reads            func() int32
	setAvailable     func(bool)
}

func (b *opaqueVisionTestBarrier) releaseHeld() { b.once.Do(func() { close(b.release) }) }

// The actual bundled Sender transport is covered independently. This arm
// drives the app and native model wire without compiling a WASM instance.
func TestOpaqueVisionNativeApplication(t *testing.T) {
	testGuardedNativeVisionApplication(t, false, false, false, false)
}

func TestDirectURLVisionNativeApplication(t *testing.T) {
	testGuardedNativeVisionApplication(t, true, false, false, false)
}

func TestCurrentNativeVisionBatchApplication(t *testing.T) {
	testGuardedNativeVisionApplication(t, true, true, false, false)
}

func TestNativeVisionOriginalCacheApplication(t *testing.T) {
	t.Run("direct", func(t *testing.T) { testGuardedNativeVisionApplication(t, true, false, true, false) })
	t.Run("opaque", func(t *testing.T) { testGuardedNativeVisionApplication(t, false, false, true, false) })
}

func TestNativeVisionPlannerReceiptReplay(t *testing.T) {
	testGuardedNativeVisionApplication(t, true, false, false, true)
}

func testGuardedNativeVisionApplication(t *testing.T, direct, batch, cacheProbe, plannerProbe bool) {
	t.Helper()
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
	barrier := &opaqueVisionTestBarrier{entered: make(chan struct{}), release: make(chan struct{}), direct: direct, batch: batch, cacheProbe: cacheProbe, plannerProbe: plannerProbe}
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
	var tokenCalls, directCalls, inlineReads atomic.Int32
	barrier.reads = func() int32 { return tokenCalls.Load() + directCalls.Load() }
	var batchMu sync.Mutex
	var batchReads []int
	batchPrefix := "https://vision.invalid/batch/"
	batchData := map[int][]byte{}
	if batch || cacheProbe {
		for i := 1; i <= 5; i++ {
			pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
			pixels.SetNRGBA(0, 0, color.NRGBA{R: uint8(i * 30), A: 255})
			var b bytes.Buffer
			if err := png.Encode(&b, pixels); err != nil {
				t.Fatal(err)
			}
			batchData[i] = b.Bytes()
		}
	}
	adapter := &botruntime.Adapter{
		InlineImage: &botruntime.InlineImageAdapter{
			Current: func() bool { return true },
			Classify: func(_ botruntime.Scene, locator string) botruntime.NativeImageLink {
				switch value := file(locator); {
				case strings.HasPrefix(value, "base64://"):
					return botruntime.NativeImageInline
				case strings.HasPrefix(value, "opaque-"):
					return botruntime.NativeImageBrowserOpaque
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
		PrepareVisionImage: func(scene botruntime.Scene, locator string) botruntime.GuardedImageRead {
			value := file(locator)
			if tail, ok := strings.CutPrefix(value, batchPrefix); batch && ok && scene.PlatformSceneID == "102" {
				id, _ := strconv.Atoi(tail)
				return func(call context.Context, guard func(context.Context) error) ([]byte, error) {
					if err := guard(call); err != nil {
						return nil, err
					}
					batchMu.Lock()
					batchReads = append(batchReads, id)
					batchMu.Unlock()
					if id == 3 {
						return nil, errors.New("synthetic media failure")
					}
					return bytes.Clone(batchData[id]), nil
				}
			}
			if value != "opaque-"+scene.PlatformSceneID && (!direct || value != "https://vision.invalid/original.png") {
				return nil
			}
			return func(call context.Context, guard func(context.Context) error) ([]byte, error) {
				hold := func() {
					close(barrier.entered)
					<-barrier.release
				}
				if !cacheProbe && direct && scene.PlatformSceneID == "104" {
					hold()
				}
				if err := guard(call); err != nil {
					return nil, err
				}
				if direct {
					directCalls.Add(1)
				} else {
					tokenCalls.Add(1)
				}
				if scene.PlatformSceneID == "104" && (cacheProbe || !direct) {
					hold() // Remote IO has completed; pause before the bytes reach the cache.
				}
				if cacheProbe && scene.PlatformSceneID == "102" && barrier.fresh.Load() {
					return bytes.Clone(batchData[1]), nil
				}
				return bytes.Clone(original), nil
			}
		},
	}
	runtime := app.Engine.Runtime
	runtime.AddAdapter("snowluma", adapter)
	if direct {
		barrier.setAvailable = func(available bool) {
			if available {
				runtime.AddAdapter("snowluma", adapter)
			} else {
				runtime.RemoveAdapter("snowluma")
			}
		}
	}
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
		wantToken, wantDirect := int32(3), int32(0)
		if direct {
			wantToken, wantDirect = 0, 2
		}
		wantInline := int32(1) // older inline is now read; identical bytes reuse the turn observation
		if batch {
			wantDirect, wantInline = 1, 0 // the positive batch uses its own adapter fixture
			batchMu.Lock()
			defer batchMu.Unlock()
			if !slices.Equal(batchReads, []int{2, 3, 4, 5}) {
				t.Errorf("batch source order/budget: %v", batchReads)
			}
		}
		if cacheProbe {
			wantInline = 0
			if direct {
				wantDirect = 4
			} else {
				wantToken = 4
			}
		}
		if plannerProbe {
			wantToken, wantDirect, wantInline = 0, 1, 0
		}
		if tokens, urls, inline := tokenCalls.Load(), directCalls.Load(), inlineReads.Load(); tokens != wantToken || urls != wantDirect || inline != wantInline {
			t.Errorf("wrong priority or extra IO: token=%d URL=%d inline=%d; want %d/%d/%d", tokens, urls, inline, wantToken, wantDirect, wantInline)
		}
	})
	assertInlineVisionModel(t, ctx, app, emit, counter, barrier)
}
