package botruntime

import (
	"context"
	"math"
)

func (r *Runtime) read(t *turn, ctx context.Context, name string, input any) (any, error) {
	c, s := t.context, t.state
	s.mu.Lock()
	if c.Mode == "real" {
		r.trimLocked(s, false)
		r.publishLocked(s)
	}
	rows := append([]*Object{}, s.messages...)
	s.mu.Unlock()
	var adapter *Adapter
	if a, ok := r.adapters.Load(c.Scene.AdapterID); ok {
		adapter = a.(*Adapter)
	}
	result := obj("items", []any{}, "coverage", "partial", "reason", "bounded_cache")
	items := []*Object{}
	if name == "messages.get" {
		v, err := schemaValue(get(contractTool(name), "inputSchema"), input)
		if err != nil {
			return nil, err
		}
		requested := stringsOf(get(v, "ids"))
		for _, id := range requested {
			c.AddDependency(id)
			if recalled, err := r.recalled(s, id); err != nil {
				return nil, err
			} else if recalled {
				continue
			}
			var cached *Object
			for _, m := range rows {
				if idOf(m) == id {
					cached = m
					break
				}
			}
			if cached != nil {
				m := cached.Clone()
				_ = m.Set("origin", "history")
				items = append(items, m)
			} else if c.Mode == "real" && adapter != nil && adapter.GetMessage != nil {
				out, e := trackedWithin(r, ctx, func(settle func(*Object, error)) { adapter.GetMessage(ctx, c.Scene, id, settle) })
				if e != nil {
					return nil, e
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
			out, err := trackedWithin(r, ctx, func(settle func(*Object, error)) { adapter.History(ctx, c.Scene, int(limit), settle) })
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
				selected = append(selected, r.normalize(m, c.Scene))
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
