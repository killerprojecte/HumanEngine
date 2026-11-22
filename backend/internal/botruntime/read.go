package botruntime

import (
	"context"
	"math"
)

func (r *Runtime) read(t *turn, ctx context.Context, name string, input any) (any, error) {
	return r.readWithRequestedTracking(t, ctx, name, input, true)
}

func (r *Runtime) readWithRequestedTracking(t *turn, ctx context.Context, name string, input any, trackRequestedIDs bool) (any, error) {
	c, s := t.context, t.state
	proofs := receiptImageSources(ctx)
	var requested []string
	var inputErr error
	if name == "messages.get" {
		var v any
		v, inputErr = schemaValue(get(contractTool(name), "inputSchema"), input)
		if inputErr == nil {
			requested = stringsOf(get(v, "ids"))
		}
	}
	s.mu.Lock()
	if c.Mode == "real" {
		r.trimLocked(s, false)
		r.publishLocked(s)
	}
	var rows []*Object
	var history *messageBlock
	if name == "messages.get" {
		history = s.history
	} else {
		rows = append([]*Object{}, s.messages...)
	}
	s.mu.Unlock()
	var adapter *Adapter
	if a, ok := r.adapters.Load(c.Scene.AdapterID); ok {
		adapter = a.(*Adapter)
	}
	result := obj("items", []any{}, "coverage", "partial", "reason", "bounded_cache")
	items := []*Object{}
	if name == "messages.get" {
		if inputErr != nil {
			return nil, inputErr
		}
		cached := cachedMessages(history, requested)
		for i, id := range requested {
			if trackRequestedIDs {
				c.AddDependency(id)
			}
			if recalled, err := r.recalled(s, id); err != nil {
				return nil, err
			} else if recalled {
				continue
			}
			if cached := cached[i]; cached != nil {
				m := cached.Clone()
				_ = m.Set("origin", "history")
				items = append(items, m)
				if proofs != nil {
					if proofs.byRow == nil {
						proofs.byRow = make(map[*Object]*Object)
					}
					proofs.byRow[m] = cached
				}
			} else if c.Mode == "real" && adapter != nil && adapter.GetMessage != nil {
				out, e := trackedWithin(r, ctx, func(settle func(*Object, error)) { adapter.GetMessage(ctx, c.Scene, id, settle) })
				if e != nil {
					return nil, e
				}
				if !trackRequestedIDs {
					current, _ := r.adapters.Load(c.Scene.AdapterID)
					binding, exists := r.RegisteredScene(c.Scene.ID)
					if current != adapter || !exists || !sameMediaScene(binding, c.Scene) {
						continue
					}
				}
				for _, value := range array(get(out, "items")) {
					m, ok := value.(*Object)
					if ok && idOf(m) == id {
						items = append(items, m)
					}
				}
			}
		}
		if len(items) == len(requested) {
			_ = result.Set("coverage", "complete")
			result.Delete("reason")
		} else {
			_ = result.Set("reason", "missing_or_recalled")
		}
	} else {
		limit := 20.0
		if n, ok := get(input, "limit").(float64); ok {
			limit = n
		}
		if math.IsNaN(limit) || math.Trunc(limit) != limit || limit < 1 || limit > 30 {
			return nil, code("runtime_invalid_tool_input")
		}
		if c.Mode == "real" && adapter != nil && adapter.History != nil {
			out, err := r.adapterHistory(ctx, adapter, c.Scene, int(limit))
			if err != nil {
				return nil, err
			}
			result = out.Clone()
			for _, value := range array(get(out, "items")) {
				if m, ok := value.(*Object); ok {
					items = append(items, m)
				}
			}
		} else {
			for _, m := range suffix(rows, int(limit)) {
				copy := m.Clone()
				_ = copy.Set("origin", "history")
				items = append(items, copy)

			}
		}
		if name == "messages.byUser" {
			user, ok := get(input, "userId").(string)
			if !ok {
				return nil, code("runtime_invalid_tool_input")
			}
			items = filterMessages(items, func(m *Object) bool { return get(m, "senderId") == user })
			_ = result.Set("reason", "bounded_user_scan")
		} else {
			messageID, ok := get(input, "messageId").(string)
			if !ok {
				return nil, code("runtime_invalid_tool_input")
			}
			for i, m := range items {
				if idOf(m) == messageID {
					start := max(0, i-int(limit)/2)
					end := min(len(items), i+int(math.Ceil(limit/2)))
					items = items[start:end]
					break
				}
			}
			_ = result.Set("reason", "bounded_history_not_native_around")
		}
		_ = result.Set("coverage", "partial")
		for _, m := range items {
			c.AddDependency(idOf(m))
		}
	}
	selected := []any{}
	for _, m := range items {
		if get(m, "sceneId") == c.Scene.ID {
			recalled, err := r.recalled(s, idOf(m))
			if err != nil {
				return nil, err
			}
			if !recalled {
				row := r.normalize(m, c.Scene)
				if proofs != nil {
					if body := proofs.byRow[m]; body != nil {
						delete(proofs.byRow, m)
						proofs.byRow[row] = body
					}
				}
				selected = append(selected, row)
			}
		}
	}
	if valid, err := r.eligible(c); err != nil {
		return nil, err
	} else if !valid {
		return obj("items", []any{}, "coverage", "unavailable", "reason", "context_cancelled"), nil
	}
	_ = result.Set("items", selected)
	return result, nil
}

func (r *Runtime) adapterHistory(ctx context.Context, adapter *Adapter, scene Scene, limit int) (*Object, error) {
	return trackedWithin(r, ctx, func(settle func(*Object, error)) {
		adapter.History(ctx, scene, limit, settle)
	})
}

// findCached returns the first cached row for id in the immutable snapshot.
func findCached(root *messageBlock, id string) *Object {
	if root == nil {
		return nil
	}
	if row := findCached(root.previous, id); row != nil {
		return row
	}
	for _, row := range root.rows {
		if idOf(row) == id {
			return row
		}
	}
	return nil
}

func cachedMessages(root *messageBlock, ids []string) []*Object {
	out := make([]*Object, len(ids))
	switch len(ids) {
	case 0:
		return out
	case 1:
		out[0] = findCached(root, ids[0])
	default:
		matches := make(map[string]*Object, len(ids))
		for _, id := range ids {
			matches[id] = nil
		}
		fillCachedMatches(root, matches, len(matches))
		for i, id := range ids {
			out[i] = matches[id]
		}
	}
	return out
}

// Visit oldest blocks first to preserve the original first match for duplicate
// IDs. Writers publish new blocks so the captured snapshot needs no scene lock.
func fillCachedMatches(root *messageBlock, matches map[string]*Object, remaining int) int {
	if root == nil {
		return remaining
	}
	remaining = fillCachedMatches(root.previous, matches, remaining)
	if remaining == 0 {
		return 0
	}
	for _, row := range root.rows {
		id := idOf(row)
		if value, requested := matches[id]; requested && value == nil {
			matches[id] = row
			remaining--
			if remaining == 0 {
				break
			}
		}
	}
	return remaining
}
