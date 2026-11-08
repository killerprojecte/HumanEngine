package memeservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sort"
)

func canonical(v any, compare func(string, string) (int, error)) (any, error) {
	switch x := v.(type) {
	case *Object:
		keys := x.Keys()
		if len(keys) > 1 && compare == nil {
			return nil, errors.New("recovery_domain_collation_required")
		}
		var failure error
		sort.SliceStable(keys, func(i, j int) bool {
			n, e := compare(keys[i], keys[j])
			if e != nil {
				failure = e
			}
			return n < 0
		})
		if failure != nil {
			return nil, failure
		}
		out := new(Object)
		for _, k := range keys {
			v, e := canonical(get(x, k), compare)
			if e != nil {
				return nil, e
			}
			set(out, k, v)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			v, e := canonical(x[i], compare)
			if e != nil {
				return nil, e
			}
			out[i] = v
		}
		return out, nil
	}
	return v, nil
}

func (s *Service) qualification(a *Object) (string, error) {
	q := object()
	for _, k := range []string{"id", "format", "bytes", "width", "height", "frames", "analyzed", "tags"} {
		if v, ok := a.Get(k); ok {
			set(q, k, v)
		}
	}
	set(q, "contentHash", content(a))
	sources := append([]any{}, arr(get(a, "sources"))...)
	var failure error
	sort.SliceStable(sources, func(i, j int) bool {
		x, e := jsjson.Stringify(sources[i])
		if e != nil {
			failure = e
		}
		y, e := jsjson.Stringify(sources[j])
		if e != nil {
			failure = e
		}
		n, e := s.policy.Compare(x, y)
		if e != nil {
			failure = e
		}
		return n < 0
	})
	if failure != nil {
		return "", failure
	}
	set(q, "sources", sources)
	v, e := canonical(q, s.policy.Compare)
	if e != nil {
		return "", e
	}
	raw, e := jsjson.Stringify(v)
	return digest([]byte(raw)), e
}
