package assertions

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// LearningTargetCandidates is the source's metadata fallback, not authority.
// LIMIT 9 is the existing eight-per-group policy plus its coverage sentinel.
func (r *Reader) LearningTargetCandidates(scene string, person *string, kinds []string) (out []*Object, err error) {
	if err = r.ready(); err != nil {
		return nil, err
	}
	values := make([]any, len(kinds))
	for i, k := range kinds {
		values[i] = k
	}
	raw, err := jsjson.Stringify(values)
	if err != nil {
		return nil, err
	}
	var p any
	if person != nil {
		p = *person
	}
	rows, err := r.query(`SELECT value FROM memories WHERE scope_key=? AND person_id IS ? AND json_extract(value,'$.kind') IN (SELECT value FROM json_each(?)) AND json_extract(value,'$.internalUse') IS NULL AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(value,'$.status') IN ('active','disputed') AND (json_extract(value,'$.validFrom') IS NULL OR julianday(json_extract(value,'$.validFrom')) <= julianday('now')) AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now')) ORDER BY rowid DESC LIMIT 9`, "scene:"+scene, p, raw)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []*Object{}
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
