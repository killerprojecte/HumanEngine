// Package promptcache retains membership and successful prefix hashes only.
// Callers supply freshly authorized bodies on every selection. It is not authority.
package promptcache

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Entry struct {
	ID    string
	Value any
}
type digestEntry struct {
	id, hash string
	bytes    int
}
type prefix struct {
	signature string
	entries   []digestEntry
}

// The source runtime constructs these four fields in this exact order.
type Options struct {
	Enabled                               bool
	MaxMessages, RetainMessages, MaxBytes int
}

func encode(v any) (string, error) { return jsjson.Stringify(v) }
func digest(v any) (string, int, error) {
	s, err := encode(v)
	if err != nil {
		return "", 0, err
	}
	b, err := jsjson.UTF8Text(s)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b))), len(b), nil
}
func entries(current []Entry) ([]digestEntry, error) {
	out := make([]digestEntry, len(current))
	seen := map[string]bool{}
	for i, e := range current {
		h, n, err := digest(e.Value)
		if err != nil {
			return nil, err
		}
		out[i] = digestEntry{e.ID, h, n}
		seen[e.ID] = true
	}
	if len(seen) != len(current) {
		return nil, fmt.Errorf("prompt_context_duplicate_message")
	}
	return out, nil
}
func matches(a, b []digestEntry) bool {
	if len(a) > len(b) {
		return false
	}
	for i, e := range a {
		if e.id != b[i].id || e.hash != b[i].hash {
			return false
		}
	}
	return true
}

// Membership is the source's 256-key LRU. This is an existing cache policy, not
// a newly introduced admission cap. It stores no message payloads.
type membership[T any] struct {
	values map[string]*list.Element
	order  list.List
}
type row[T any] struct {
	key   string
	value T
}

func (m *membership[T]) get(key string) (T, bool) {
	if e := m.values[key]; e != nil {
		return e.Value.(row[T]).value, true
	}
	var z T
	return z, false
}
func (m *membership[T]) delete(key string) {
	if e := m.values[key]; e != nil {
		delete(m.values, key)
		m.order.Remove(e)
	}
}
func (m *membership[T]) set(key string, v T) {
	if m.values == nil {
		m.values = map[string]*list.Element{}
	}
	m.delete(key)
	m.values[key] = m.order.PushBack(row[T]{key, v})
	if len(m.values) > 256 {
		m.delete(m.order.Front().Value.(row[T]).key)
	}
}
func (m *membership[T]) clear() { m.values = nil; m.order.Init() }

// Windows is owned by the scene/runtime coordinator. Calls are serialized by
// that owner; it starts no goroutines, performs no IO and retains no bodies.
type Windows struct{ windows membership[prefix] }

func (w *Windows) Clear()            { w.windows.clear() }
func (w *Windows) Delete(key string) { w.windows.delete(key) }
func suffix(n, count int) int {
	if count >= n {
		return 0
	}
	return n - count
}
func (w *Windows) Select(key string, current []Entry, o Options, authority any, requiredIDs []string) ([]Entry, error) {
	if !o.Enabled {
		w.Delete(key)
		return append([]Entry{}, current[suffix(len(current), 24):]...), nil
	}
	fail := func(code string) ([]Entry, error) { w.Delete(key); return nil, fmt.Errorf("%s", code) }
	if len(current) == 0 {
		w.Delete(key)
		if len(requiredIDs) > 0 {
			return fail("prompt_context_target_budget_exhausted")
		}
		return []Entry{}, nil
	}
	// Validated runtime settings require positive integral counts/bytes. No silent
	// clamping or new limits are added here; malformed internal options are errors.
	if o.MaxMessages < 1 || o.RetainMessages < 1 || o.MaxBytes < 1 {
		return nil, fmt.Errorf("prompt_context_invalid_options")
	}
	// Avoid integer overflow without changing the suffix for an oversized count.
	if o.MaxMessages < len(current) {
		current = current[len(current)-o.MaxMessages-1:]
	}
	required := map[string]bool{}
	present := map[string]bool{}
	for _, id := range requiredIDs {
		required[id] = true
	}
	for _, e := range current {
		present[e.ID] = true
	}
	for id := range required {
		if !present[id] {
			return fail("prompt_context_target_budget_exhausted")
		}
	}
	options, _ := jsjson.NewObject(jsjson.Member{Key: "enabled", Value: o.Enabled}, jsjson.Member{Key: "maxMessages", Value: float64(o.MaxMessages)}, jsjson.Member{Key: "retainMessages", Value: float64(o.RetainMessages)}, jsjson.Member{Key: "maxBytes", Value: float64(o.MaxBytes)})
	signature, _, err := digest([]any{options, authority})
	if err != nil {
		return nil, err
	}
	all, err := entries(current)
	if err != nil {
		return nil, err
	}
	start := -1
	if previous, ok := w.windows.get(key); ok && previous.signature == signature && len(previous.entries) > 0 {
		for i, e := range all {
			if e.id == previous.entries[0].id {
				start = i
				break
			}
		}
		if start >= 0 && !matches(previous.entries, all[start:]) {
			start = -1
		}
	}
	if start < 0 {
		start = suffix(len(current), o.RetainMessages)
	}
	// Each message is serialized once. Array JSON size is exactly brackets,
	// element bytes and commas; trimming subtracts sizes instead of reserializing.
	size := func(from int) int {
		n := 2
		for i := from; i < len(all); i++ {
			n += all[i].bytes
			if i > from {
				n++
			}
		}
		return n
	}
	used := size(start)
	if len(current)-start > o.MaxMessages || used > o.MaxBytes {
		count := min(o.RetainMessages, o.MaxMessages)
		start = suffix(len(current), count)
		used = size(start)
	}
	for i, e := range current {
		if required[e.ID] {
			if i < start {
				start = i
				used = size(start)
			}
			break
		}
	}
	if len(current)-start > o.MaxMessages {
		return fail("prompt_context_target_budget_exhausted")
	}
	for len(current)-start > 1 && used > o.MaxBytes && !required[current[start].ID] {
		used -= all[start].bytes + 1
		start++
	}
	if used > o.MaxBytes {
		return fail("prompt_context_message_budget_exhausted")
	}
	w.windows.set(key, prefix{signature, append([]digestEntry(nil), all[start:]...)})
	return append([]Entry{}, current[start:]...), nil
}

type boundaryState struct{ successful *prefix }

// Boundaries permits asynchronous ticket commits. Preparation and commit have
// one publication order; old, deleted, cleared or LRU-evicted tickets do nothing.
type Boundaries struct {
	mu     sync.Mutex
	states membership[*boundaryState]
}
type Ticket struct {
	ContextCacheBoundaries []int
	commit                 func()
}

func (t *Ticket) Commit() {
	if t != nil && t.commit != nil {
		t.commit()
	}
}
func (b *Boundaries) Clear()            { b.mu.Lock(); defer b.mu.Unlock(); b.states.clear() }
func (b *Boundaries) Delete(key string) { b.mu.Lock(); defer b.mu.Unlock(); b.states.delete(key) }
func (b *Boundaries) Prepare(key string, current []Entry, authority any) (*Ticket, error) {
	signature, _, err := digest(authority)
	if err != nil {
		return nil, err
	}
	all, err := entries(current)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := &boundaryState{}
	indexes := []int{}
	if previous, ok := b.states.get(key); ok && previous.successful != nil {
		p := previous.successful
		if p.signature == signature && len(p.entries) > 0 && matches(p.entries, all) {
			state.successful = p
			indexes = append(indexes, len(p.entries)-1)
		}
	}
	if len(all) > 0 && (len(indexes) == 0 || indexes[len(indexes)-1] != len(all)-1) {
		indexes = append(indexes, len(all)-1)
	}
	b.states.set(key, state)
	return &Ticket{indexes, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if current, ok := b.states.get(key); ok && current == state {
			state.successful = &prefix{signature, all}
		}
	}}, nil
}
