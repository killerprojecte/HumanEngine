package assertions

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// ListRead is fixed readMemoryList scope data. Nil IDs means omitted; a nonnil
// empty slice deliberately selects nothing. This is not an exposure capability.
type ListRead struct {
	SceneID    string
	Simulation bool
	PersonID   string
	IDs        []string
}

// MemoryListCandidates applies source SQLite filters without granting authority.
// All rows are materialized and closed before support traversal starts.
func (r *Reader) MemoryListCandidates(input ListRead) (out []*Object, err error) {
	if err = r.ready(); err != nil {
		return nil, err
	}
	out = []*Object{}
	scope := "(scope_key=? OR scope_key='shared:public')"
	args := []any{"scene:" + input.SceneID}
	if input.Simulation {
		if input.PersonID != "" {
			return out, nil
		}
		scope = "scope_key='shared:public'"
		args = nil
	} else if input.PersonID != "" {
		scope = "scope_key=? AND person_id=?"
		args = append(args, input.PersonID)
	}
	from := "memories"
	prefix := ""
	if input.IDs != nil {
		unique := []any{}
		seen := map[string]bool{}
		for _, id := range input.IDs {
			if !seen[id] {
				seen[id] = true
				unique = append(unique, id)
			}
		}
		raw, e := jsjson.Stringify(unique)
		if e != nil {
			return nil, e
		}
		from = "json_each(?) AS wanted CROSS JOIN memories"
		prefix = "memories.id=wanted.value AND "
		args = append([]any{raw}, args...)
	}
	rows, err := r.query("SELECT memories.value FROM "+from+" WHERE "+prefix+scope+" AND json_extract(memories.value,'$.internalUse') IS NULL AND COALESCE(json_extract(memories.value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(memories.value,'$.status') IN ('active','disputed') AND (json_extract(memories.value,'$.validFrom') IS NULL OR julianday(json_extract(memories.value,'$.validFrom')) <= julianday('now')) AND (json_extract(memories.value,'$.validUntil') IS NULL OR julianday(json_extract(memories.value,'$.validUntil')) > julianday('now')) ORDER BY memories.rowid DESC", args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
		if err != nil {
			out = nil
		}
	}()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		o, ok := v.(*Object)
		if !ok {
			return nil, errors.New("assertion_record_object_required")
		}
		out = append(out, o)
	}
	err = rows.Err()
	return
}
