package botruntime

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"weak"
)

const (
	visionOriginalCount = 24
	visionOriginalBytes = 32 * 1024 * 1024
	visionOriginalTTL   = 5 * 60 * 1000 // runtime clock milliseconds
)

// The immutable retained record owns its scene, exact revision, and part
// locators. A weak reference does not retain the room's message body.
type visionOriginalKey struct {
	record  weak.Pointer[messageRecord]
	adapter weak.Pointer[Adapter]
	part    uint8
}
type visionOriginalEntry struct {
	key     visionOriginalKey
	bytes   []byte
	expires float64
}
type visionOriginalCache struct {
	mu      sync.Mutex
	entries []visionOriginalEntry // least-recently-used first
	total   int
}

func (c *visionOriginalCache) find(key visionOriginalKey, now float64) int {
	entry := -1
	c.entries = slices.DeleteFunc(c.entries, func(e visionOriginalEntry) bool {
		if e.expires <= now || e.key.record.Value() == nil || e.key.adapter.Value() == nil {
			c.total -= len(e.bytes)
			return true
		}
		return false
	})
	for i := range c.entries {
		if c.entries[i].key == key {
			entry = i
			break
		}
	}
	return entry
}

func (c *visionOriginalCache) get(key visionOriginalKey, now float64) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.find(key, now)
	if i < 0 {
		return nil
	}
	e := c.entries[i]
	copy(c.entries[i:], c.entries[i+1:])
	c.entries[len(c.entries)-1] = e
	return e.bytes
}

func (c *visionOriginalCache) put(key visionOriginalKey, now float64, original []byte, source *DisplayImageSource) bool {
	if len(original) == 0 || len(original) > visionOriginalBytes {
		return true
	}
	owned := bytes.Clone(original)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !source.Current() {
		return false
	}
	if i := c.find(key, now); i >= 0 {
		c.total -= len(c.entries[i].bytes)
		c.entries = slices.Delete(c.entries, i, i+1)
	}
	c.entries = append(c.entries, visionOriginalEntry{key, owned, now + visionOriginalTTL})
	c.total += len(owned)
	drop := 0
	for len(c.entries)-drop > visionOriginalCount || c.total > visionOriginalBytes {
		c.total -= len(c.entries[drop].bytes)
		drop++
	}
	c.entries = slices.Delete(c.entries, 0, drop)
	return true
}

func (c *visionOriginalCache) clear() {
	c.mu.Lock()
	c.entries = nil
	c.total = 0
	c.mu.Unlock()
}

// Only a qualified native vision source carries guardedRead. Browser previews
// and historical lookups neither seed nor consult this cache.
func (m *DisplayImageSource) visionOriginalBytes(ctx context.Context, before func(context.Context) error) ([]byte, error) {
	key := visionOriginalKey{weak.Make(m.record), weak.Make(m.adapter), m.visionPart}
	cache := &m.runtime.visionOriginals
	check := func() error {
		if err := before(ctx); err != nil {
			return err
		}
		if !m.Current() {
			return ErrDisplayImageUnavailable
		}
		return nil
	}
	if original := cache.get(key, m.runtime.services.Clock.Now()); original != nil {
		if err := check(); err != nil {
			return nil, err
		}
		return bytes.Clone(original), nil
	}
	original, err := m.guardedRead(ctx, before)
	if err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	if !cache.put(key, m.runtime.services.Clock.Now(), original, m) {
		return nil, ErrDisplayImageUnavailable
	}
	return original, nil
}
