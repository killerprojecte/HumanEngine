package workingset

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Storage borrows one active session. A held Storage expires with that scope.
// It performs metadata persistence, never content authorization.
type Storage struct {
	session *recovery.WriteSession
	scope   *Scope
}

func (s *Storage) AssertSupported() (bool, error) {
	if e := s.scope.CheckScope(); e != nil {
		return false, e
	}
	return s.scope.audit()
}
func bind(args []any) []any {
	out := make([]any, len(args))
	for i, v := range args {
		if x, ok := v.(string); ok {
			out[i] = x
			if y, e := jsjson.UTF8Text(x); e == nil {
				out[i] = y
			}
		} else {
			out[i] = v
		}
	}
	return out
}
func (s *Storage) exec(q string, args ...any) (sql.Result, error) {
	if s.session == nil {
		return nil, errors.New("working_set_transaction_required")
	}
	if e := s.scope.CheckScope(); e != nil {
		return nil, e
	}
	return s.session.Exec(q, bind(args)...)
}
func (s *Storage) one(q string, args []any, dest ...any) (found bool, err error) {
	rows, e := s.scope.Query(q, bind(args)...)
	if e != nil {
		return false, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return true, rows.Scan(dest...)
	}
	return false, rows.Err()
}
func (s *Storage) Exists() (bool, error) {
	var n int
	return s.one("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_working_set_items'", nil, &n)
}
func (s *Storage) Ensure() error {
	if s.session == nil {
		return errors.New("working_set_transaction_required")
	}
	if e := s.scope.CheckScope(); e != nil {
		return e
	}
	return s.session.EnsureWorkingSetSchema()
}
func readEntity[T any](s *Storage, table, scene string, ref Entity) (*T, error) {
	yes, e := s.Exists()
	if e != nil || !yes {
		return nil, e
	}
	var encoded any
	found, e := s.one("SELECT value FROM "+table+" WHERE scene_id=? AND kind=? AND entity_id=?", []any{scene, ref.Kind, ref.ID}, &encoded)
	if e != nil || !found {
		return nil, e
	}
	raw, ok := encoded.(string)
	if !ok {
		return nil, invalid()
	}
	v, e := Decode[T](raw)
	if e != nil {
		return nil, e
	}
	if !rowBinding(v, scene, ref) {
		return nil, invalid()
	}
	return &v, nil
}
func rowBinding(v any, scene string, ref Entity) bool {
	switch x := v.(type) {
	case PinPolicy:
		return x.SceneID == scene && x.Selection.Ref.Kind == ref.Kind && x.Selection.Ref.ID == ref.ID
	case Item:
		return x.Snapshot.SceneID == scene && x.Snapshot.Ref.Kind == ref.Kind && x.Snapshot.Ref.ID == ref.ID
	case Job:
		return x.Snapshot.SceneID == scene && x.Snapshot.Ref.Kind == ref.Kind && x.Snapshot.Ref.ID == ref.ID
	}
	return false
}
func entity(r Reference) Entity { return Entity{r.Kind, r.ID} }
func (s *Storage) Pin(scene string, ref Entity) (*PinPolicy, error) {
	return readEntity[PinPolicy](s, "memory_working_set_pins", scene, ref)
}
func (s *Storage) Item(scene string, ref Reference) (*Item, error) {
	return readEntity[Item](s, "memory_working_set_items", scene, entity(ref))
}
func (s *Storage) Job(scene string, ref Reference) (*Job, error) {
	return readEntity[Job](s, "memory_working_set_jobs", scene, entity(ref))
}
func (s *Storage) put(table, scene string, ref Reference, value any) error {
	raw, e := Encode(value)
	if e != nil {
		return e
	}
	_, e = s.exec("INSERT INTO "+table+" VALUES(?,?,?,?) ON CONFLICT(scene_id,kind,entity_id) DO UPDATE SET value=excluded.value", scene, ref.Kind, ref.ID, raw)
	return e
}
func (s *Storage) PutPin(v PinPolicy) error {
	return s.put("memory_working_set_pins", v.SceneID, v.Selection.Ref, v)
}
func (s *Storage) PutItem(v Item) error {
	return s.put("memory_working_set_items", v.Snapshot.SceneID, v.Snapshot.Ref, v)
}
func (s *Storage) PutJob(v Job) error {
	return s.put("memory_working_set_jobs", v.Snapshot.SceneID, v.Snapshot.Ref, v)
}
func listEntities[T any](s *Storage, q, scene string, args ...any) (out []T, err error) {
	out = []T{}
	yes, e := s.Exists()
	if e != nil || !yes {
		return out, e
	}
	rows, e := s.scope.Query(q, bind(args)...)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var rowScene, rowKind, rowID, encoded any
		if e = rows.Scan(&rowScene, &rowKind, &rowID, &encoded); e != nil {
			return nil, e
		}
		raw, ok := encoded.(string)
		if !ok {
			return nil, invalid()
		}
		kind, kok := rowKind.(string)
		id, iok := rowID.(string)
		if !kok || !iok {
			return nil, invalid()
		}
		v, e := Decode[T](raw)
		if e != nil {
			return nil, e
		}
		if rowScene != scene || !rowBinding(v, scene, Entity{kind, id}) {
			return nil, invalid()
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Storage) ListPins(scene string, limit int) ([]PinPolicy, error) {
	return listEntities[PinPolicy](s, "SELECT scene_id,kind,entity_id,value FROM memory_working_set_pins WHERE scene_id=? AND json_extract(value,'$.terminal')=0 ORDER BY rowid LIMIT ?", scene, scene, limit)
}
func (s *Storage) Jobs(scene string, now float64, limit int) ([]Job, error) {
	return listEntities[Job](s, "SELECT scene_id,kind,entity_id,value FROM memory_working_set_jobs WHERE scene_id=? AND (json_extract(value,'$.lease') IS NULL OR json_extract(value,'$.lease.expiresAt')<=?) ORDER BY rowid LIMIT ?", scene, scene, now, limit)
}
func (s *Storage) RemoveJob(scene string, ref Reference) error {
	_, e := s.exec("DELETE FROM memory_working_set_jobs WHERE scene_id=? AND kind=? AND entity_id=?", scene, ref.Kind, ref.ID)
	return e
}
func (s *Storage) Pending(scene string) (int, error) {
	yes, e := s.Exists()
	if e != nil || !yes {
		return 0, e
	}
	var n int
	_, e = s.one("SELECT count(*) FROM memory_working_set_jobs WHERE scene_id=?", []any{scene}, &n)
	return n, e
}
func (s *Storage) Receipt(key string) (*Receipt, error) {
	yes, e := s.Exists()
	if e != nil || !yes {
		return nil, e
	}
	var encoded any
	found, e := s.one("SELECT value FROM memory_working_set_receipts WHERE receipt_key=?", []any{key}, &encoded)
	if e != nil || !found {
		return nil, e
	}
	raw, ok := encoded.(string)
	if !ok {
		return nil, invalid()
	}
	v, e := Decode[Receipt](raw)
	return &v, e
}
func (s *Storage) PutReceipt(key string, v Receipt) error {
	raw, e := Encode(v)
	if e != nil {
		return e
	}
	_, e = s.exec("INSERT INTO memory_working_set_receipts VALUES(?,?,?)", key, v.SelectedAt, raw)
	return e
}
func (s *Storage) ReceiptCount() (int, error) {
	var n int
	_, e := s.one("SELECT count(*) FROM main.memory_working_set_receipts", nil, &n)
	return n, e
}
func (s *Storage) PruneReceipts(before float64) error {
	_, e := s.exec("DELETE FROM memory_working_set_receipts WHERE selected_at<?", before)
	return e
}
func (s *Storage) Invalidate(scene string, refs []Entity) error {
	yes, e := s.Exists()
	if e != nil || !yes {
		return e
	}
	for _, ref := range refs {
		for _, table := range []string{"memory_working_set_items", "memory_working_set_jobs"} {
			if _, e = s.exec("DELETE FROM "+table+" WHERE scene_id=? AND kind=? AND entity_id=?", scene, ref.Kind, ref.ID); e != nil {
				return e
			}
		}
		if _, e = s.exec("DELETE FROM memory_working_set_receipts WHERE json_extract(value,'$.sceneId')=? AND json_extract(value,'$.selection.ref.kind')=? AND json_extract(value,'$.selection.ref.id')=?", scene, ref.Kind, ref.ID); e != nil {
			return e
		}
	}
	return nil
}

// Reset discards derived metadata only. Offline recovery has a separate owner.
func (s *Storage) Reset() error {
	yes, e := s.AssertSupported()
	if e != nil || !yes {
		return e
	}
	for _, table := range []string{"memory_working_set_items", "memory_working_set_jobs", "memory_working_set_receipts"} {
		if _, e = s.exec("DELETE FROM " + table); e != nil {
			return e
		}
	}
	return nil
}
func (s *Storage) AffectedAssertionScenes(id string, after *string, limit int) (out []string, err error) {
	out = []string{}
	yes, e := s.AssertSupported()
	if e != nil || !yes {
		return out, e
	}
	var cursor any
	if after != nil {
		cursor = *after
	}
	rows, e := s.scope.Query(`SELECT scene_id FROM (
 SELECT scene_id FROM memory_working_set_pins WHERE kind='assertion' AND entity_id=?
 UNION SELECT scene_id FROM memory_working_set_items WHERE kind='assertion' AND entity_id=?
 UNION SELECT scene_id FROM memory_working_set_jobs WHERE kind='assertion' AND entity_id=?
 UNION SELECT json_extract(value,'$.sceneId') AS scene_id FROM memory_working_set_receipts WHERE json_extract(value,'$.selection.ref.kind')='assertion' AND json_extract(value,'$.selection.ref.id')=?
 ) WHERE (? IS NULL OR scene_id>?) ORDER BY scene_id COLLATE BINARY LIMIT ?`, bind([]any{id, id, id, id, cursor, cursor, limit})...)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var scene string
		if e = rows.Scan(&scene); e != nil {
			return nil, e
		}
		out = append(out, scene)
	}
	return out, rows.Err()
}
