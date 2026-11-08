package assertions

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// AssertionRow retains the stored fingerprint and serialized preimage. In
// particular, historical fingerprints must not be reconstructed from content.
type AssertionRow struct {
	ID          string  `json:"id"`
	ScopeKey    string  `json:"scope_key"`
	PersonID    *string `json:"person_id"`
	Content     string  `json:"content"`
	Fingerprint string  `json:"fingerprint"`
	Value       string  `json:"value"`
}

func (row *AssertionRow) Record() (*Object, error) {
	v, e := jsjson.Parse(row.Value)
	if e != nil {
		return nil, e
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("assertion_record_object_required")
	}
	return o, nil
}

// ReconciliationRow performs the source policy's raw row read in the existing
// owner scope. Like the source SQL read, it does not add business readiness.
func (r *Reader) ReconciliationRow(key string, byID bool) (*AssertionRow, error) {
	if e := r.check(); e != nil {
		return nil, e
	}
	query := "SELECT id,scope_key,person_id,content,fingerprint,value FROM memories WHERE fingerprint=?"
	if byID {
		query = "SELECT id,scope_key,person_id,content,fingerprint,value FROM memories WHERE id=?"
	}
	row := new(AssertionRow)
	var person sql.NullString
	found, e := r.one(query, []any{key}, &row.ID, &row.ScopeKey, &person, &row.Content, &row.Fingerprint, &row.Value)
	if e != nil || !found {
		return nil, e
	}
	if person.Valid {
		row.PersonID = &person.String
	}
	return row, nil
}
func (r *Reader) FingerprintDeleted(fp string) (bool, error) {
	if e := r.check(); e != nil {
		return false, e
	}
	var one int
	return r.one("SELECT 1 FROM memory_deleted WHERE fingerprint=?", []any{fp}, &one)
}
