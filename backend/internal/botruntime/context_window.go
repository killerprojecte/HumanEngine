package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/promptcache"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func (r *Runtime) selectWindow(key string, rows []*Object, options promptcache.Options, authority any, required []string) ([]*Object, error) {
	entries := []promptcache.Entry{}
	for _, m := range rows {
		entries = append(entries, promptcache.Entry{ID: idOf(m), Value: m})
	}
	r.cacheMu.Lock()
	selected, err := r.windows.Select(key, entries, options, authority, required)
	r.cacheMu.Unlock()
	if err != nil {
		return nil, err
	}
	out := []*Object{}
	for _, m := range selected {
		out = append(out, m.Value.(*Object))
	}
	return out, nil
}
func (r *Runtime) prepareTicket(key string, rows []*Object, p *replyplan.ContextProjection, authority any, enabled, evidence bool) (*promptcache.Ticket, error) {
	if !enabled {
		r.boundaries.Delete(key)
		return nil, nil
	}
	entries := []promptcache.Entry{}
	for _, m := range rows {
		var value any = m
		if evidence {
			value = obj("id", "message:"+idOf(m), "value", m)
		}
		projected, e := p.Project(value)
		if e != nil {
			return nil, e
		}
		entries = append(entries, promptcache.Entry{ID: idOf(m), Value: projected})
	}
	return r.boundaries.Prepare(key, entries, authority)
}
func (r *Runtime) layout(user *Object, name string, cfg Settings, ticket *promptcache.Ticket) {
	if cfg.PromptContextWindowEnabled {
		_ = user.Set("contextLayout", name)
	}
	if ticket != nil {
		indexes := []any{}
		for _, i := range ticket.ContextCacheBoundaries {
			indexes = append(indexes, float64(i))
		}
		_ = user.Set("contextCacheBoundaries", indexes)
	}
}
