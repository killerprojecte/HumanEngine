package identityprivacy

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sort"
)

// ReadRegistry borrows an existing domain scope. It never creates the registry
// or opens a database/root transaction. Schema validation is shared with recall.
func ReadRegistry(scope sourceevents.RuntimeReadScope) (*jsjson.Object, error) {
	if err := scope.Ready(); err != nil {
		return nil, err
	}
	rows, err := scope.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_identity_registry'")
	if err != nil {
		return nil, err
	}
	exists := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if !exists {
		return object("format", float64(1), "revision", float64(0), "edges", []any{}, "aliases", []any{}, "operations", []any{}), nil
	}
	rows, err = scope.Query("SELECT value FROM memory_identity_registry WHERE id=1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return nil, err
		}
		return nil, errCorrupt
	}
	var raw any
	if err = rows.Scan(&raw); err != nil {
		return nil, err
	}
	text, ok := raw.(string)
	if !ok {
		return nil, errCorrupt
	}
	v, err := jsjson.Parse(text)
	if err != nil {
		return nil, errCorrupt
	}
	return parseState(v)
}

// Members implements the bounded source component walk, then restricts it to
// original-person records authorized in this scene/account. Links grant no read.
func Members(state *jsjson.Object, adapter, account, person string, allowed map[string]bool) ([]*jsjson.Object, error) {
	anchor, ok := endpoint(object("adapterId", adapter, "accountId", account, "personId", person))
	if !ok {
		return nil, errors.New("identity_endpoint_invalid")
	}
	out := []*jsjson.Object{}
	if !allowed[person] {
		return out, nil
	}
	found := map[string]*jsjson.Object{endpointKey(anchor): anchor}
	changed := true
	for changed {
		changed = false
		for _, raw := range get(state, "edges").([]any) {
			edge := raw.([]any)
			a, b := edge[0].(*jsjson.Object), edge[1].(*jsjson.Object)
			_, hasA := found[endpointKey(a)]
			_, hasB := found[endpointKey(b)]
			if hasA || hasB {
				for _, item := range []*jsjson.Object{a, b} {
					key := endpointKey(item)
					if _, ok := found[key]; !ok {
						found[key] = item
						changed = true
						if len(found) > 32 {
							return nil, errors.New("identity_component_capacity")
						}
					}
				}
			}
		}
	}
	for _, item := range found {
		if get(item, "adapterId") == adapter && get(item, "accountId") == account && allowed[get(item, "personId").(string)] {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := jsjson.UTF16(endpointKey(out[i]))
		b, _ := jsjson.UTF16(endpointKey(out[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return out, nil
}
