package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
)

// This context obeys synchronous Done/Err but lets the test hold AfterFunc
// delivery. Value deliberately does not expose the embedded cancelCtx: otherwise
// Go can propagate cancellation synchronously through its private optimization.
type heldAfterContext struct {
	context.Context
	mu        sync.Mutex
	callbacks map[*func()]bool
	ran       atomic.Int32
}

func (h *heldAfterContext) Value(any) any { return nil }
func (h *heldAfterContext) AfterFunc(f func()) func() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.callbacks == nil {
		h.callbacks = map[*func()]bool{}
	}
	key := &f
	h.callbacks[key] = true
	return func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		active := h.callbacks[key]
		delete(h.callbacks, key)
		return active
	}
}
func (h *heldAfterContext) release() {
	h.mu.Lock()
	calls := h.callbacks
	h.callbacks = map[*func()]bool{}
	h.mu.Unlock()
	for f, active := range calls {
		if active {
			h.ran.Add(1)
			(*f)()
		}
	}
}
func noCacheFiles(t *testing.T, root string) {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			count++
		}
		return nil
	})
	if err != nil || count != 0 {
		t.Fatal("late cache data survived generation disable", count, err)
	}
}

func TestBuiltinLateCacheRegistrationAndFinalizeArePhysicallyJoined(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		name := "plugin_cache_callback"
		if finalize {
			name = "automatic_read_finalization"
		}
		t.Run(name, func(t *testing.T) {
			m, i, life, turn := builtinToolManager(t)
			held := &heldAfterContext{Context: life}
			defer held.release()
			m.mu.Lock()
			i.ctx = held
			m.mu.Unlock()
			// A real private directory gives a physical witness, not just return denial.
			_ = m.cache.Dispose()
			directory := t.TempDir()
			m.cache = toolresults.New(toolresults.Config{TempDir: directory})
			captured, releaseOwner := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			resume := func() { releaseOnce.Do(func() { close(releaseOwner) }) }
			defer resume()
			var captureOnce sync.Once
			removeCapture := m.runtime.AddExtension(&botruntime.Extension{CaptureToolResultGuards: func(*botruntime.TurnContext) []*botruntime.ToolResultGuard {
				captureOnce.Do(func() { close(captured); <-releaseOwner })
				return nil
			}})
			defer removeCapture()
			var sourceReads atomic.Int32
			var cached toolresults.Result
			var cacheErr error
			run := func(ctx context.Context, _ string, _ json.RawMessage, cache func(context.Context, toolresults.Source, toolresults.Options) (toolresults.Result, error)) (json.RawMessage, error) {
				if finalize {
					return json.Marshal(map[string]any{"data": strings.Repeat("x", 60*1024)})
				}
				source := toolresults.Text("must never be published after the old generation was invalidated")
				cached, cacheErr = cache(context.Background(), toolresults.NextFunc(func(ctx context.Context) ([]byte, error) { sourceReads.Add(1); return source.Next(ctx) }), toolresults.Options{Encoding: toolresults.UTF8})
				return json.RawMessage(`{"ok":true}`), nil
			}
			// This is the actual installed manager extension and its physical cleanup,
			// not a bare callback disconnected from the manager's Disable queue.
			m.mu.Lock()
			physicalIndex := len(i.physicalCleanups)
			m.mu.Unlock()
			ext := builtinExtension(t, m, i, held, builtinReadDefinition, run)
			invalidated := make(chan struct{})
			physicalWaitProved := make(chan struct{})
			physicalWaitFailed := make(chan error, 1)
			m.mu.Lock()
			if len(i.physicalCleanups) != physicalIndex+1 {
				m.mu.Unlock()
				t.Fatal("installation did not register its real bridge physical cleanup")
			}
			physicalCleanup := i.physicalCleanups[physicalIndex]
			i.physicalCleanups[physicalIndex] = func(context.Context) error {
				// Call the exact registered bridge.CloseAndWait, not a test group.
				// Group.Wait returns nil when no physical owner remains, even for a
				// cancelled context. While owner capture is held, only Canceled proves
				// that this actual cleanup sees the outstanding continuation lease.
				probe, cancelProbe := context.WithCancel(context.Background())
				cancelProbe()
				if err := physicalCleanup(probe); !errors.Is(err, context.Canceled) {
					physicalWaitFailed <- err
				} else {
					close(physicalWaitProved)
				}
				// Probe cancellation must not replace the physical shutdown. Join the
				// same cleanup without a deadline, as the real Manager normally does.
				return physicalCleanup(context.Background())
			}
			i.cleanups = append(i.cleanups, func() { close(invalidated) })
			m.mu.Unlock()
			execution := make(chan error, 1)
			ext.Execute(context.Background(), "web-read.read", new(jsjson.Object), turn, func(_ any, err error) { execution <- err })
			select {
			case <-captured:
			case <-time.After(5 * time.Second):
				t.Fatal("owner capture did not reach after-current/before-Store barrier")
			}
			disabled := make(chan error, 1)
			go func() { disabled <- m.Disable(context.Background(), "web-read", "barrier") }()
			select {
			case <-invalidated:
			case <-time.After(5 * time.Second):
				t.Fatal("disable did not finish cache invalidation")
			}
			if held.Err() == nil || held.ran.Load() != 0 {
				t.Fatal("test did not hold asynchronous cancellation delivery", held.Err(), held.ran.Load())
			}
			select {
			case <-physicalWaitProved:
			case err := <-physicalWaitFailed:
				t.Fatal("registered bridge cleanup did not prove pending physical work; want context.Canceled", err)
			case <-time.After(5 * time.Second):
				t.Fatal("disable did not reach the real bridge physical-wait probe")
			}
			// Auxiliary sanity check only: the cancelled-context probe above is the
			// deterministic proof, independent of the Disable goroutine's scheduling.
			select {
			case err := <-disabled:
				t.Fatal("disable returned before admitted cache/finalization physically finished", err)
			default:
			}
			noCacheFiles(t, directory)
			// Source owner capture resumes only AFTER invalidate completed, while the
			// derived cancellation callback is still held. The Store admission is late.
			resume()
			select {
			case err := <-execution:
				if err == nil {
					t.Fatal("late completed tool result escaped")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("late execution did not settle")
			}
			select {
			case err := <-disabled:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disable did not join physical completion")
			}
			if !finalize && (cacheErr == nil || cached.ResultID != "" || sourceReads.Load() != 0) {
				t.Fatal("late Store entered or published despite revoked generation", cached.ResultID, cacheErr, sourceReads.Load())
			}
			noCacheFiles(t, directory)
		})
	}
}
