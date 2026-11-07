package toolresults

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	c := New(Config{TempDir: t.TempDir()})
	t.Cleanup(func() {
		if err := c.Dispose(); err != nil {
			t.Error(err)
		}
	})
	return c
}
func mustStore(t *testing.T, c *Cache, source Source, owner Owner, options Options) Result {
	t.Helper()
	result, err := c.Store(context.Background(), source, owner, options)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not finish")
	}
}
func assertEmpty(t *testing.T, c *Cache) {
	t.Helper()
	c.mu.Lock()
	n := len(c.entries) + len(c.writes) + len(c.pluginEntries) + len(c.dependencyEntries) + len(c.pluginWrites) + len(c.dependencyWrites)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("cache retained entries, writes or ownership indexes: %d", n)
	}
	if c.directory != "" {
		entries, err := os.ReadDir(c.directory)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatal("staged files retained")
		}
	}
}

type stalledSource struct {
	started, release, finished, returnStarted, returnRelease, returnFinished chan struct{}
	destroys, returns                                                        atomic.Int32
	panicHooks                                                               bool
}

func newStalled() *stalledSource {
	return &stalledSource{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), returnStarted: make(chan struct{}), returnRelease: make(chan struct{}), returnFinished: make(chan struct{})}
}
func (s *stalledSource) Next(context.Context) ([]byte, error) {
	close(s.started)
	<-s.release
	close(s.finished)
	return []byte("late result must never publish"), nil
}
func (s *stalledSource) Destroy() {
	s.destroys.Add(1)
	if s.panicHooks {
		panic("destroy failure")
	}
}
func (s *stalledSource) Return() error {
	s.returns.Add(1)
	close(s.returnStarted)
	defer close(s.returnFinished)
	if s.panicHooks {
		panic("return failure")
	}
	<-s.returnRelease
	return errors.New("late return rejection")
}

func TestStalledSourceMatchesOracleAndDoesNotDelayDisposal(t *testing.T) {
	for _, expected := range fixtures(t).Stalled {
		t.Run(expected.Mode, func(t *testing.T) {
			c := newTestCache(t)
			source := newStalled()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := c.Store(ctx, source, testOwner(), Options{}); result <- err }()
			waitSignal(t, source.started)
			if expected.Mode == "cancel" {
				cancel()
			}
			if expected.Mode == "dispose" {
				done := make(chan struct{})
				go func() { _ = c.Dispose(); close(done) }()
				waitSignal(t, done)
			}
			if expected.Mode == "invalidate" {
				c.Invalidate("scene-a", "message-a")
			}
			if expected.Mode == "plugin" {
				c.InvalidatePlugin("fixture")
			}
			select {
			case err := <-result:
				if err == nil || err.Error() != expected.Error {
					t.Fatalf("got %v, source %s", err, expected.Error)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Store waited on stalled Next")
			}
			waitSignal(t, source.returnStarted)
			if int(source.destroys.Load()) != expected.Destroys || int(source.returns.Load()) != expected.Returns {
				t.Fatalf("cleanup differs: destroys=%d returns=%d", source.destroys.Load(), source.returns.Load())
			}
			if err := c.Dispose(); err != nil {
				t.Fatal(err)
			}
			assertEmpty(t, c)
			close(source.release)
			close(source.returnRelease)
			waitSignal(t, source.finished)
			waitSignal(t, source.returnFinished)
			assertEmpty(t, c)
		})
	}
}

func TestPanickingCleanupHooksCannotPreventDisposal(t *testing.T) {
	c := newTestCache(t)
	source := newStalled()
	source.panicHooks = true
	result := make(chan error, 1)
	go func() { _, err := c.Store(context.Background(), source, testOwner(), Options{}); result <- err }()
	waitSignal(t, source.started)
	c.InvalidatePlugin("fixture")
	select {
	case err := <-result:
		if !errors.Is(err, ErrInvalidated) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup panic blocked store")
	}
	waitSignal(t, source.returnFinished)
	if err := c.Dispose(); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, c)
	close(source.release)
	waitSignal(t, source.finished)
	if source.destroys.Load() != 2 {
		t.Fatal("source destroy ordering changed")
	}
}

func TestEntireLargeStreamIsStoredAndCanBeReconstructed(t *testing.T) {
	c := newTestCache(t)
	payload := make([]byte, 2*1024*1024+19)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	consumed, offset := 0, 0
	buffer := make([]byte, 7001)
	source := NextFunc(func(context.Context) ([]byte, error) {
		if offset == len(payload) {
			return nil, io.EOF
		}
		n := copy(buffer, payload[offset:])
		offset += n
		consumed++
		return buffer[:n], nil
	})
	first := mustStore(t, c, source, testOwner(), Options{PreviewBytes: num(0)})
	wantCalls := (len(payload) + len(buffer) - 1) / len(buffer)
	if consumed != wantCalls || first.TotalBytes != int64(len(payload)) || first.Content != "" {
		t.Fatalf("incomplete stream: %+v, calls %d", first, consumed)
	}
	stat, err := os.Stat(c.entries[first.ResultID].path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != int64(len(payload)) || stat.Mode().Perm() != 0o600 {
		t.Fatalf("storage size or permission differs: %v", stat)
	}
	actual := make([]byte, 0, len(payload))
	for offset := int64(0); offset < int64(len(payload)); {
		part, err := c.Read(context.Background(), ReadInput{ResultID: first.ResultID, Offset: float64(offset), Full: true}, testOwner().Scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		if part.RequestedRange.Length != int64(len(payload))-offset || part.ReturnedRange.Length <= 0 {
			t.Fatalf("invalid page: %+v", part)
		}
		actual = append(actual, decode(t, part)...)
		offset = part.ReturnedRange.End
	}
	if !bytes.Equal(actual, payload) || consumed != wantCalls {
		t.Fatal("retrieval changed bytes or reran source")
	}
	request := ReadInput{ResultID: first.ResultID, Offset: 1048500, Length: num(251)}
	again, err := c.Read(context.Background(), request, testOwner().Scope, nil)
	if err != nil || !bytes.Equal(decode(t, again), payload[1048500:1048751]) {
		t.Fatalf("repeated range changed: %v", err)
	}
}

func TestRangeNumberContractAndCancellationOrdering(t *testing.T) {
	c := newTestCache(t)
	for _, value := range []float64{-1, 0.5, math.NaN(), math.Inf(1), math.Inf(-1), MaxSafeInteger + 1} {
		for _, input := range []ReadInput{{Offset: value}, {Length: num(value)}} {
			_, err := c.Read(context.Background(), input, "missing", nil)
			if !errors.Is(err, ErrInvalidRange) {
				t.Fatalf("%v: %v", value, err)
			}
		}
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("caller reason")
	cancel(cause)
	if _, err := c.Read(ctx, ReadInput{Offset: -1}, "missing", nil); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := c.Store(ctx, Text("x"), testOwner(), Options{PreviewBytes: num(-1)}); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancelCause(context.Background())
	cause = errors.New("tool_result_custom_reason")
	cancel(cause)
	if _, err := c.Store(ctx, Text("x"), testOwner(), Options{}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if err := c.Dispose(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store(ctx, Text("x"), testOwner(), Options{}); !errors.Is(err, ErrCacheClosed) {
		t.Fatal(err)
	}
}

func TestGuardsAndAdoptionCanReenterWithoutCacheLock(t *testing.T) {
	for _, phase := range []string{"pre-read", "post-read", "adopt"} {
		t.Run(phase, func(t *testing.T) {
			c := newTestCache(t)
			owner := testOwner()
			calls := 0
			trigger := 0
			owner.Validations = []Validation{func() (bool, error) {
				calls++
				if calls == trigger {
					c.InvalidatePlugin(owner.PluginID)
				}
				return true, nil
			}}
			first := mustStore(t, c, Text("protected"), owner, Options{})
			if calls != 3 {
				t.Fatalf("source guards called %d times", calls)
			}
			var adopt func(Owner) error
			want := ErrInvalidated
			if phase == "pre-read" {
				trigger = 4
				want = ErrNotFound
			}
			if phase == "post-read" {
				trigger = 5
			}
			if phase == "adopt" {
				adopt = func(o Owner) error { c.Invalidate(o.SceneID, o.Dependencies[0]); return nil }
				want = nil
			}
			done := make(chan error, 1)
			go func() {
				_, err := c.Read(context.Background(), ReadInput{ResultID: first.ResultID}, owner.Scope, adopt)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("callback reentry deadlocked")
			}
			assertEmpty(t, c)
		})
	}
}

func TestOwnerSnapshotIndexesAndLiveValidation(t *testing.T) {
	c := newTestCache(t)
	owner := testOwner()
	var valid atomic.Bool
	valid.Store(true)
	owner.Validations = []Validation{func() (bool, error) { return valid.Load(), nil }}
	first := mustStore(t, c, Text("owner"), owner, Options{})
	owner.Dependencies[0] = "mutated"
	owner.Validations[0] = func() (bool, error) { return false, nil }
	_, err := c.Read(context.Background(), ReadInput{ResultID: first.ResultID}, owner.Scope, func(adopted Owner) error {
		adopted.Dependencies[0] = "adoption mutation"
		adopted.Validations[0] = func() (bool, error) { return false, nil }
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Invalidate(owner.SceneID, "mutated")
	if _, err := c.Read(context.Background(), ReadInput{ResultID: first.ResultID}, owner.Scope, nil); err != nil {
		t.Fatal(err)
	}
	valid.Store(false)
	if _, err := c.Read(context.Background(), ReadInput{ResultID: first.ResultID}, owner.Scope, nil); !errors.Is(err, ErrInvalidated) {
		t.Fatal(err)
	}
	assertEmpty(t, c)
}

func TestIndexedInvalidationVisitsOnlyMatchingActiveOwnership(t *testing.T) {
	c := newTestCache(t)
	// No fake I/O: unrelated records only exercise ownership indexing. Real
	// file removal and revoked pending streams are covered separately above.
	c.mu.Lock()
	for i := 0; i < 10000; i++ {
		id := fmt.Sprint(i)
		owner := Owner{Scope: id, SceneID: id, PluginID: id, Dependencies: []string{"source", "source"}}
		c.addEntryLocked(id, &entry{owner: owner})
		c.addWriteLocked(&lease{owner: owner, cancel: func() {}})
	}
	c.mu.Unlock()
	if visited := c.invalidate(&dependency{"5000", "source"}, ""); visited != 2 {
		t.Fatalf("dependency revocation visited %d owners", visited)
	}
	if visited := c.invalidate(nil, "6000"); visited != 2 {
		t.Fatalf("plugin revocation visited %d owners", visited)
	}
	if visited := c.invalidate(&dependency{"absent", "source"}, ""); visited != 0 {
		t.Fatalf("absent dependency scanned %d owners", visited)
	}
	c.mu.Lock()
	for l := range c.writes {
		c.removeWriteLocked(l)
	}
	for id := range c.entries {
		c.removeEntryLocked(id)
	}
	c.mu.Unlock()
	assertEmpty(t, c)
}

func TestUnlinkFailureStillRevokesAndShutdownRetriesDirectoryCleanup(t *testing.T) {
	c := newTestCache(t)
	first := mustStore(t, c, Text("private"), testOwner(), Options{})
	path := c.entries[first.ResultID].path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "blocks-unlink"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.InvalidatePlugin("fixture")
	if _, err := c.Read(context.Background(), ReadInput{ResultID: first.ResultID}, testOwner().Scope, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("test did not preserve failed unlink", err)
	}
	if err := c.Dispose(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.directory); !os.IsNotExist(err) {
		t.Fatal("shutdown did not retry directory cleanup", err)
	}
	assertEmpty(t, c)
}

func TestFailedDirectoryCreationRemainsFailedLikeSourcePromise(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing")
	c := New(Config{TempDir: parent})
	if _, err := c.Store(context.Background(), Text("x"), testOwner(), Options{}); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store(context.Background(), Text("x"), testOwner(), Options{}); !errors.Is(err, ErrStorage) {
		t.Fatal("directory failure was not retained", err)
	}
	if err := c.Dispose(); err == nil {
		t.Fatal("source dispose propagates rejected directory promise")
	}
	assertEmpty(t, c)
}

func TestOwnerIdentifiersRemainOpaqueAndLossless(t *testing.T) {
	c := newTestCache(t)
	a, b := jsjson.FromUTF16([]uint16{0xd800}), jsjson.FromUTF16([]uint16{0xd801})
	owners := []Owner{
		{Scope: a, SceneID: a, PluginID: a, Dependencies: []string{a}},
		{Scope: b, SceneID: b, PluginID: b, Dependencies: []string{b}},
		{Scope: "�", SceneID: "�", PluginID: "�", Dependencies: []string{"�"}},
		{Scope: "", SceneID: "", PluginID: "", Dependencies: []string{""}},
	}
	results := make([]Result, len(owners))
	for i, owner := range owners {
		results[i] = mustStore(t, c, Text("opaque"), owner, Options{})
	}
	c.Invalidate(a, b)
	for i, owner := range owners {
		if _, err := c.Read(context.Background(), ReadInput{ResultID: results[i].ResultID}, owner.Scope, nil); err != nil {
			t.Fatal(err)
		}
	}
	c.Invalidate(a, a)
	if _, err := c.Read(context.Background(), ReadInput{ResultID: results[0].ResultID}, a, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for i := 1; i < len(owners); i++ {
		if _, err := c.Read(context.Background(), ReadInput{ResultID: results[i].ResultID}, owners[i].Scope, nil); err != nil {
			t.Fatal(err)
		}
		c.InvalidatePlugin(owners[i].PluginID)
	}
	assertEmpty(t, c)
}

func TestConcurrentStoreReadRevokeDispose(t *testing.T) {
	c := newTestCache(t)
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Go(func() {
			owner := testOwner()
			owner.PluginID = fmt.Sprint(i)
			for j := 0; j < 12; j++ {
				result, err := c.Store(context.Background(), Text("synthetic concurrent result"), owner, Options{})
				if err != nil {
					if !errors.Is(err, ErrCacheClosed) && !errors.Is(err, ErrCancelled) {
						t.Error(err)
					}
					return
				}
				_, err = c.Read(context.Background(), ReadInput{ResultID: result.ResultID}, owner.Scope, nil)
				if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalidated) {
					t.Error(err)
				}
				c.InvalidatePlugin(owner.PluginID)
			}
		})
	}
	group.Wait()
	var dispose sync.WaitGroup
	for range 4 {
		dispose.Go(func() {
			if err := c.Dispose(); err != nil {
				t.Error(err)
			}
		})
	}
	dispose.Wait()
	assertEmpty(t, c)
}
