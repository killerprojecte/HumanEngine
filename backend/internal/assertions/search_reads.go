package assertions

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// SearchRows executes the fixed source scope. Final applies time eligibility only
// after applicability traversal, preserving records that become eligible then.
func (r *Reader) SearchRows(scene string, simulation bool, person string, final bool, denied []string, space string) (out []SearchRow, err error) {
	if err = r.ready(); err != nil {
		return nil, err
	}
	out = []SearchRow{}
	scope := "(m.scope_key=? OR m.scope_key='shared:public')"
	args := []any{"scene:" + scene}
	if simulation {
		if person != "" {
			return
		}
		scope = "m.scope_key='shared:public'"
		args = nil
	} else if person != "" {
		scope = "m.scope_key=? AND m.person_id=?"
		args = append(args, person)
	}
	selectSQL := "m.value,m.content,NULL"
	join := ""
	if space != "" && final {
		selectSQL = "m.value,m.content,v.embedding"
		join = " LEFT JOIN memory_vectors v ON v.memory_id=m.id AND v.space_id=?"
		args = append([]any{space}, args...)
	}
	if final {
		ids := make([]any, len(denied))
		for i, id := range denied {
			ids[i] = id
		}
		raw, e := jsjson.Stringify(ids)
		if e != nil {
			return nil, e
		}
		args = append(args, raw)
		scope += ` AND m.id NOT IN (SELECT value FROM json_each(?)) AND json_extract(m.value,'$.internalUse') IS NULL AND COALESCE(json_extract(m.value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(m.value,'$.status') IN ('active','disputed') AND (json_extract(m.value,'$.validFrom') IS NULL OR julianday(json_extract(m.value,'$.validFrom')) <= julianday('now')) AND (json_extract(m.value,'$.validUntil') IS NULL OR julianday(json_extract(m.value,'$.validUntil')) > julianday('now'))`
	}
	rows, e := r.query("SELECT "+selectSQL+" FROM memories m"+join+" WHERE "+scope+" ORDER BY m.rowid DESC", args...)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var raw, content string
		var embedding []byte
		if e = rows.Scan(&raw, &content, &embedding); e != nil {
			return nil, e
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		record, ok := v.(*Object)
		if !ok {
			return nil, errors.New("assertion_record_object_required")
		}
		out = append(out, SearchRow{record, content, embedding})
	}
	err = rows.Err()
	return
}

type SearchRow struct {
	Record    *Object
	Content   string
	Embedding []byte
}

func (r *Reader) VectorSpace(id string) (*Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	var raw string
	found, e := r.one("SELECT value FROM vector_spaces WHERE id=?", []any{id}, &raw)
	if e != nil || !found {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("invalid_vector_space")
	}
	return o, nil
}
