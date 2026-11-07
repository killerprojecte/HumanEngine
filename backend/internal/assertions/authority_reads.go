package assertions

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// AssertReady checks the existing Reader capability; it never enters recovery
// authority again. Authority consumers must not retain this Reader after Read.
func (r *Reader) AssertReady() error { return r.ready() }

// CheckScope checks lifetime/ownership without invoking business readiness.
func (r *Reader) CheckScope() error { return r.check() }

// AuthoritySourceEvents shares the exact connection/transaction and lifetime of
// this Reader. An independently opened source-event read would reenter the
// authority lock and could observe a different epoch.
func (r *Reader) AuthoritySourceEvents() (*sourceevents.View, error) {
	return sourceevents.BorrowReadScope(r.ready, func(q string, args ...any) (sourceevents.Rows, error) {
		return r.query(q, args...)
	})
}

func (r *Reader) Recalled(sceneID, messageID string) (bool, error) {
	if e := r.ready(); e != nil {
		return false, e
	}
	var one int
	return r.one("SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", []any{sceneID, messageID}, &one)
}

func (r *Reader) Scene(id string) (*Object, error) {
	if e := r.ready(); e != nil {
		return nil, e
	}
	var raw string
	found, e := r.one("SELECT value FROM scenes WHERE id=?", []any{id}, &raw)
	if e != nil || !found {
		return nil, e
	}
	value, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	if !truthy(value) {
		return nil, nil
	}
	o, ok := value.(*Object)
	if !ok {
		return nil, errors.New("assertion_scene_object_required")
	}
	return o, nil
}
