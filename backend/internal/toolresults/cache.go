package toolresults

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Config defaults to a 30-minute TTL, Date.now, and the OS temporary directory.
// Now returns epoch milliseconds and must be concurrency-safe. TempDir changes
// only where a fresh private directory is created, never loads existing results.
type Config struct {
	TTLMS   *float64
	Now     func() float64
	TempDir string
}

type entry struct {
	owner       Owner
	path        string
	total       int64
	contentType string
	encoding    Encoding
	expiresAt   float64
	order       *list.Element
}
type lease struct {
	owner       Owner
	invalidated atomic.Bool
	cancel      context.CancelFunc
}
type dependency struct{ scene, message string }
type Cache struct {
	mu                sync.Mutex
	entries           map[string]*entry
	entryOrder        list.List
	writes            map[*lease]struct{}
	pluginEntries     map[string]map[string]struct{}
	dependencyEntries map[dependency]map[string]struct{}
	pluginWrites      map[string]map[*lease]struct{}
	dependencyWrites  map[dependency]map[*lease]struct{}
	closed            bool
	pending           sync.WaitGroup
	ctx               context.Context
	cancel            context.CancelFunc
	ttl               float64
	now               func() float64
	tempDir           string
	directoryOnce     sync.Once
	directory         string
	directoryErr      error
	sweepDone         chan struct{}
	disposeOnce       sync.Once
	disposeErr        error
}

func New(config Config) *Cache {
	ttl := float64(30 * 60_000)
	if config.TTLMS != nil {
		ttl = *config.TTLMS
	}
	now := config.Now
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixMilli()) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Cache{entries: make(map[string]*entry), writes: make(map[*lease]struct{}), ctx: ctx, cancel: cancel,
		ttl: ttl, now: now, tempDir: config.TempDir, sweepDone: make(chan struct{}),
		pluginEntries: make(map[string]map[string]struct{}), dependencyEntries: make(map[dependency]map[string]struct{}),
		pluginWrites: make(map[string]map[*lease]struct{}), dependencyWrites: make(map[dependency]map[*lease]struct{})}
	// Node's timers coerce sub-millisecond, negative and NaN delays to 1ms.
	delay := math.Min(ttl, 60_000)
	if math.IsNaN(delay) || delay < 1 {
		delay = 1
	}
	go c.sweep(time.Duration(math.Trunc(delay)) * time.Millisecond)
	return c
}

func (c *Cache) sweep(delay time.Duration) {
	defer close(c.sweepDone)
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.Expire()
		}
	}
}

func (c *Cache) dir() (string, error) {
	c.directoryOnce.Do(func() { c.directory, c.directoryErr = os.MkdirTemp(c.tempDir, "humanengine-results-") })
	if c.directoryErr != nil {
		return "", ErrStorage
	}
	return c.directory, nil
}

// Expire performs the same sweep used by the timer. Revocation is immediate;
// unlink failures are ignored and directory cleanup is retried during Dispose.
func (c *Cache) Expire() {
	c.mu.Lock()
	type item struct {
		id    string
		entry *entry
	}
	entries := make([]item, 0, len(c.entries))
	for cursor := c.entryOrder.Front(); cursor != nil; cursor = cursor.Next() {
		id := cursor.Value.(string)
		entries = append(entries, item{id, c.entries[id]})
	}
	c.mu.Unlock()
	for _, item := range entries {
		if item.entry.expiresAt <= c.now() {
			c.remove(item.id)
		}
	}
}

func (c *Cache) remove(id string) {
	c.mu.Lock()
	e := c.removeEntryLocked(id)
	c.mu.Unlock()
	if e != nil {
		_ = os.Remove(e.path)
	}
}

func (c *Cache) Invalidate(sceneID, messageID string) {
	c.invalidate(&dependency{scene: sceneID, message: messageID}, "")
}
func (c *Cache) InvalidatePlugin(pluginID string) {
	c.invalidate(nil, pluginID)
}
func (c *Cache) invalidate(key *dependency, plugin string) int {
	var paths []string
	var cancels []context.CancelFunc
	c.mu.Lock()
	entries, writes := c.pluginEntries[plugin], c.pluginWrites[plugin]
	if key != nil {
		entries, writes = c.dependencyEntries[*key], c.dependencyWrites[*key]
	}
	// Counts candidates, not elapsed time. Unrelated entries/writes are never
	// scanned; indexes retain only active ownership, not revocation generations.
	visited := len(entries) + len(writes)
	for l := range writes {
		l.invalidated.Store(true)
		cancels = append(cancels, l.cancel)
	}
	for id := range entries {
		if e := c.removeEntryLocked(id); e != nil {
			paths = append(paths, e.path)
		}
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, path := range paths {
		_ = os.Remove(path)
	}
	return visited
}

func addIndex[K comparable, V comparable](index map[K]map[V]struct{}, key K, value V) {
	set := index[key]
	if set == nil {
		set = make(map[V]struct{})
		index[key] = set
	}
	set[value] = struct{}{}
}
func removeIndex[K comparable, V comparable](index map[K]map[V]struct{}, key K, value V) {
	set := index[key]
	delete(set, value)
	if len(set) == 0 {
		delete(index, key)
	}
}
func (c *Cache) addEntryLocked(id string, e *entry) {
	c.entries[id] = e
	e.order = c.entryOrder.PushBack(id)
	addIndex(c.pluginEntries, e.owner.PluginID, id)
	for _, message := range e.owner.Dependencies {
		addIndex(c.dependencyEntries, dependency{e.owner.SceneID, message}, id)
	}
}
func (c *Cache) removeEntryLocked(id string) *entry {
	e := c.entries[id]
	if e == nil {
		return nil
	}
	delete(c.entries, id)
	c.entryOrder.Remove(e.order)
	removeIndex(c.pluginEntries, e.owner.PluginID, id)
	for _, message := range e.owner.Dependencies {
		removeIndex(c.dependencyEntries, dependency{e.owner.SceneID, message}, id)
	}
	return e
}
func (c *Cache) addWriteLocked(l *lease) {
	c.writes[l] = struct{}{}
	addIndex(c.pluginWrites, l.owner.PluginID, l)
	for _, message := range l.owner.Dependencies {
		addIndex(c.dependencyWrites, dependency{l.owner.SceneID, message}, l)
	}
}
func (c *Cache) removeWriteLocked(l *lease) {
	delete(c.writes, l)
	removeIndex(c.pluginWrites, l.owner.PluginID, l)
	for _, message := range l.owner.Dependencies {
		removeIndex(c.dependencyWrites, dependency{l.owner.SceneID, message}, l)
	}
}

// Store does not return until all chunks are written and the guarded preview is
// read. Owner slices are snapshotted at entry; validation functions remain live.
// Sources are single-use. Neither callback code nor blocking I/O holds c.mu.
func (c *Cache) Store(ctx context.Context, source Source, owner Owner, options Options) (result Result, err error) {
	owner = owner.clone()
	lifetime, cancel := context.WithCancel(ctx)
	l := &lease{owner: owner, cancel: cancel}
	c.mu.Lock()
	closed := c.closed
	if !closed {
		c.addWriteLocked(l)
		c.pending.Add(1)
	}
	c.mu.Unlock()
	if closed {
		cancel()
		destroy(source)
		return Result{}, ErrCacheClosed
	}
	stop := context.AfterFunc(c.ctx, cancel)
	// AfterFunc schedules cancellation asynchronously. Check the cache context
	// explicitly at admission so an already disposed cache cannot start I/O.
	if c.ctx.Err() != nil {
		cancel()
	}
	defer func() {
		stop()
		cancel()
		if err != nil {
			destroy(source)
			if !isToolError(err) {
				if ctx.Err() != nil || c.ctx.Err() != nil {
					err = ErrCancelled
				} else {
					err = ErrStorage
				}
			}
		}
		c.mu.Lock()
		c.removeWriteLocked(l)
		c.mu.Unlock()
		c.pending.Done()
	}()
	return c.write(lifetime, source, owner, options, l)
}

func destroy(source Source) {
	defer func() { _ = recover() }()
	if source, ok := source.(Destroyer); ok {
		source.Destroy()
	}
}
func isToolError(err error) bool { return strings.HasPrefix(err.Error(), "tool_result_") }
func abortError(ctx context.Context) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

type nextResult struct {
	chunk []byte
	err   error
}
type iterator struct {
	ctx     context.Context
	request chan struct{}
	ready   chan nextResult
}

func newIterator(ctx context.Context, source Source) iterator {
	i := iterator{ctx: ctx, request: make(chan struct{}), ready: make(chan nextResult, 1)}
	// A single pump belongs to this Store, with no prefetch. A detached stalled
	// call owns only the source/context, not the file or cache. The buffered slot
	// lets an eventually resolved/rejected call exit after cancellation.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-i.request:
			}
			if err := abortError(ctx); err != nil {
				i.ready <- nextResult{err: err}
				return
			}
			chunk, err := source.Next(ctx)
			select {
			case <-ctx.Done():
				return
			case i.ready <- nextResult{chunk: chunk, err: err}:
			}
		}
	}()
	return i
}
func (i iterator) next() ([]byte, error) {
	if err := abortError(i.ctx); err != nil {
		return nil, err
	}
	select {
	case <-i.ctx.Done():
		return nil, ErrCancelled
	case i.request <- struct{}{}:
	}
	select {
	case <-i.ctx.Done():
		return nil, ErrCancelled
	case item := <-i.ready:
		return item.chunk, item.err
	}
}

func (c *Cache) write(ctx context.Context, source Source, owner Owner, options Options, l *lease) (result Result, err error) {
	if err := abortError(ctx); err != nil {
		return Result{}, err
	}
	directory, err := c.dir()
	if err != nil {
		return Result{}, err
	}
	id, err := uuid()
	if err != nil {
		return Result{}, ErrStorage
	}
	path := filepath.Join(directory, id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{}, ErrStorage
	}
	iterator := newIterator(ctx, source)
	completed := false
	defer func() {
		if err != nil {
			_ = file.Close()
			c.mu.Lock()
			c.removeEntryLocked(id)
			c.mu.Unlock()
			_ = os.Remove(path)
			switch {
			case l.invalidated.Load():
				err = ErrInvalidated
			case ctx.Err() != nil:
				err = ErrCancelled
			case !isToolError(err):
				err = ErrStorage
			}
		}
		if !completed {
			destroy(source)
			if source, ok := source.(Returner); ok {
				go func() { defer func() { _ = recover() }(); _ = source.Return() }()
			}
		}
	}()
	var total int64
	for {
		chunk, nextErr := iterator.next()
		if nextErr == io.EOF {
			completed = true
			break
		}
		if nextErr != nil {
			return Result{}, nextErr
		}
		if err := abortError(ctx); err != nil {
			return Result{}, err
		}
		for offset := 0; offset < len(chunk); {
			n, writeErr := file.Write(chunk[offset:])
			if writeErr != nil {
				return Result{}, writeErr
			}
			if n == 0 {
				return Result{}, ErrStorage
			}
			offset += n
		}
		if int64(len(chunk)) > MaxSafeInteger-total {
			return Result{}, ErrStorage
		}
		total += int64(len(chunk))
	}
	if err := abortError(ctx); err != nil {
		return Result{}, err
	}
	valid, err := owner.validate()
	if err != nil {
		return Result{}, err
	}
	if !valid {
		return Result{}, ErrInvalidated
	}
	if err := file.Close(); err != nil {
		return Result{}, err
	}
	if l.invalidated.Load() {
		return Result{}, ErrInvalidated
	}
	if err := abortError(ctx); err != nil {
		return Result{}, err
	}
	contentType := "application/octet-stream"
	if options.ContentType != nil {
		contentType = *options.ContentType
	}
	encoding := options.Encoding
	if encoding == "" {
		encoding = Base64
		if s, ok := source.(*single); ok && s.text {
			encoding = UTF8
		}
	}
	e := &entry{owner: owner.clone(), path: path, total: total, contentType: contentType, encoding: encoding, expiresAt: c.now() + c.ttl}
	c.mu.Lock()
	// Publish and invalidation serialize, so no result can appear after a
	// matching invalidate has returned, even before its cancel callback runs.
	if l.invalidated.Load() {
		c.mu.Unlock()
		return Result{}, ErrInvalidated
	}
	if c.closed || ctx.Err() != nil {
		c.mu.Unlock()
		return Result{}, ErrCancelled
	}
	c.addEntryLocked(id, e)
	c.mu.Unlock()
	return c.Read(ctx, ReadInput{ResultID: id, Length: options.PreviewBytes}, owner.Scope, nil)
}

func validInteger(value float64) bool {
	return value >= 0 && value <= MaxSafeInteger && math.Trunc(value) == value
}

// Read checks the caller's cancellation before ranges, scope before expiry,
// and source validation both before I/O and before adoption. A successful read
// linearizes at its final membership check; adoption may itself revoke a result.
// Cancellation is returned as the caller's context cause (as throwIfAborted in
// the source). Store, in contrast, normalizes aborted writes to ErrCancelled.
func (c *Cache) Read(ctx context.Context, input ReadInput, scope string, adopt func(Owner) error) (Result, error) {
	if err := abortError(ctx); err != nil {
		return Result{}, err
	}
	if !validInteger(input.Offset) || input.Length != nil && !validInteger(*input.Length) || input.Full && input.Length != nil {
		return Result{}, ErrInvalidRange
	}
	c.mu.Lock()
	e := c.entries[input.ResultID]
	c.mu.Unlock()
	if e == nil || e.owner.Scope != scope {
		return Result{}, ErrNotFound
	}
	if e.expiresAt <= c.now() {
		c.remove(input.ResultID)
		return Result{}, ErrExpired
	}
	valid, err := e.owner.validate()
	if err != nil {
		return Result{}, err
	}
	if !valid {
		c.remove(input.ResultID)
		return Result{}, ErrInvalidated
	}
	offset := int64(input.Offset)
	if offset > e.total {
		return Result{}, ErrInvalidRange
	}
	requested := int64(DefaultPreviewBytes)
	if input.Full {
		requested = e.total - offset
	} else if input.Length != nil {
		requested = int64(*input.Length)
	}
	count := min(requested, e.total-offset, ViewBytes)
	file, err := os.Open(e.path)
	if err != nil {
		return Result{}, ErrNotFound
	}
	bytes := make([]byte, int(count))
	_, err = file.ReadAt(bytes, offset)
	closeErr := file.Close()
	if closeErr != nil {
		return Result{}, closeErr
	}
	if err != nil {
		return Result{}, ErrStorage
	}
	encoding := input.Encoding
	if encoding == "" {
		encoding = e.encoding
	}
	encode := func(bytes []byte) string {
		if encoding == UTF8 {
			if utf8.Valid(bytes) {
				return string(bytes)
			}
			encoding = Base64
		}
		return base64.StdEncoding.EncodeToString(bytes)
	}
	content := encode(bytes)
	for {
		first, err := jsjson.Stringify(content)
		if err != nil {
			return Result{}, err
		}
		second, err := jsjson.Stringify(first)
		if err != nil {
			return Result{}, err
		}
		if len(second) <= ViewBytes || count == 0 {
			break
		}
		count = int64(math.Floor(float64(count) * 0.8))
		bytes = bytes[:int(count)]
		content = encode(bytes)
	}
	if err := abortError(ctx); err != nil {
		return Result{}, err
	}
	valid, err = e.owner.validate()
	if err != nil {
		return Result{}, err
	}
	c.mu.Lock()
	present := c.entries[input.ResultID] == e
	c.mu.Unlock()
	if !valid || !present {
		return Result{}, ErrInvalidated
	}
	if adopt != nil {
		if err := adopt(e.owner.clone()); err != nil {
			return Result{}, err
		}
	}
	expiresAt, err := isoDate(e.expiresAt)
	if err != nil {
		return Result{}, err
	}
	end := offset + count
	var nextOffset *int64
	if end < e.total {
		nextOffset = &end
	}
	return Result{ResultID: input.ResultID, TotalBytes: e.total, ContentType: e.contentType, Complete: true, ExpiresAt: expiresAt,
		Retention: Retention, RangeUnit: "bytes", RequestedRange: Range{Offset: offset, Length: requested},
		ReturnedRange: ReturnedRange{Offset: offset, Length: count, End: end}, Encoding: encoding, Content: content,
		Truncated: end < e.total || offset != 0, RequestComplete: count == min(requested, e.total-offset), NextOffset: nextOffset,
		ViewBudgetBytes: ViewBytes, RetrievalTool: "tool-results.read", Trust: "untrusted_tool_content"}, nil
}

// Dispose revokes all results, aborts and waits for cache writes (not stalled
// user Next/Return calls), stops expiry, and removes its private directory.
// It is idempotent and safe concurrently with Store, Read and invalidation.
func (c *Cache) Dispose() error {
	c.disposeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.cancel()
		c.pending.Wait()
		<-c.sweepDone
		c.mu.Lock()
		clear(c.entries)
		c.entryOrder.Init()
		clear(c.pluginEntries)
		clear(c.dependencyEntries)
		c.mu.Unlock()
		if c.directoryErr != nil {
			c.disposeErr = c.directoryErr
		} else if c.directory != "" {
			c.disposeErr = os.RemoveAll(c.directory)
		}
	})
	return c.disposeErr
}

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func isoDate(ms float64) (string, error) {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || math.Abs(ms) > 8.64e15 {
		return "", errors.New("Invalid time value")
	}
	t := time.UnixMilli(int64(math.Trunc(ms))).UTC()
	year := t.Year()
	if year >= 0 && year <= 9999 {
		return t.Format("2006-01-02T15:04:05.000Z"), nil
	}
	return fmt.Sprintf("%+07d%s", year, t.Format("-01-02T15:04:05.000Z")), nil
}
