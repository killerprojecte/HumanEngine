package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// View exposes only scoped indexed source queries. Use it synchronously within
// Read; it is neither a connection nor permission to publish outside that scope.
type View struct {
	query         func(string, ...any) (Rows, error)
	ready         func() error
	stable        bool
	schemaKnown   bool
	schemaPresent bool
	schemaError   error
}

func (s *Store) view() *View {
	if s.maintenance != nil {
		return &View{ready: s.ready, query: func(q string, args ...any) (Rows, error) {
			return s.maintenanceTx.QueryContext(s.maintenanceContext, q, bind(args)...)
		}}
	}
	return &View{ready: s.ready, query: func(q string, args ...any) (Rows, error) { return s.session.Query(q, bind(args)...) }}
}
func (s *Store) Read(ctx context.Context, fn func(*View) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil || s.maintenance != nil {
		return fn(s.view())
	}
	return s.authority.Publish(ctx, s.main, func(conn *sql.Conn) ([]byte, error) {
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		v := &View{stable: true, ready: func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			if s.options.AssertReady != nil {
				return s.options.AssertReady()
			}
			return nil
		}, query: func(q string, args ...any) (Rows, error) {
			if !active.Load() {
				return nil, sql.ErrTxDone
			}
			return conn.QueryContext(ctx, q, bind(args)...)
		}}
		return nil, fn(v)
	}, func([]byte) error { return nil })
}
func (v *View) one(query string, args []any, dest ...any) (exists bool, err error) {
	rows, err := v.query(query, args...)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return true, rows.Scan(dest...)
	}
	return false, rows.Err()
}
func (v *View) exists() (bool, error) {
	if e := v.ready(); e != nil {
		return false, e
	}
	if v.stable && v.schemaKnown {
		return v.schemaPresent, v.schemaError
	}
	present, err := v.inspectSchema()
	if v.stable {
		v.schemaKnown = true
		v.schemaPresent = present
		v.schemaError = err
	}
	return present, err
}
func (v *View) inspectSchema() (bool, error) {
	rows, e := v.query("SELECT name FROM sqlite_master WHERE type='table' AND name IN ('source_event_heads','source_event_revisions','source_event_edit_keys','source_event_tombstones')")
	if e != nil {
		return false, e
	}
	n := 0
	for rows.Next() {
		n++
	}
	e = errors.Join(rows.Err(), rows.Close())
	if e != nil {
		return false, e
	}
	if n != 0 && n != 4 {
		return false, ErrCorrupt
	}
	return n == 4, nil
}
func (v *View) revoked(i Identity) (bool, error) {
	var one int
	return v.one("SELECT 1 FROM source_event_tombstones WHERE scene_id=? AND message_id=?", []any{i.SceneID, i.MessageID}, &one)
}
func (v *View) head(i Identity) (*Head, error) {
	key, e := IdentityKey(i)
	if e != nil {
		return nil, e
	}
	var storedKey, eventID, sceneID, messageID, value string
	exists, e := v.one("SELECT identity_key,event_id,scene_id,message_id,value FROM source_event_heads WHERE identity_key=?", []any{key}, &storedKey, &eventID, &sceneID, &messageID, &value)
	if e != nil || !exists {
		return nil, e
	}
	parsed, e := jsjson.Parse(value)
	if e != nil {
		return nil, ErrCorrupt
	}
	h, e := ParseHead(parsed)
	if e != nil {
		return nil, e
	}
	actual, _ := IdentityKey(h.Metadata.Identity)
	if actual != storedKey || h.Metadata.Ref.EventID != eventID || h.Metadata.Identity.SceneID != sceneID || h.Metadata.Identity.MessageID != messageID {
		return nil, ErrCorrupt
	}
	return &h, nil
}
func (v *View) HeadMetadata(i Identity) (*Metadata, error) {
	if !i.Valid() {
		return nil, ErrInvalid
	}
	exists, e := v.exists()
	if e != nil || !exists {
		return nil, e
	}
	revoked, e := v.revoked(i)
	if e != nil || revoked {
		return nil, e
	}
	h, e := v.head(i)
	if e != nil || h == nil {
		return nil, e
	}
	return &h.Metadata, nil
}
func (v *View) Registered(sceneID, messageID string) (*Metadata, error) {
	if !identifier(sceneID) || !identifier(messageID) {
		return nil, ErrInvalid
	}
	exists, e := v.exists()
	if e != nil || !exists {
		return nil, e
	}
	rows, e := v.query("SELECT value FROM source_event_heads WHERE scene_id=? AND message_id=?", sceneID, messageID)
	if e != nil {
		return nil, e
	}
	var values []string
	for rows.Next() {
		var value string
		if e = rows.Scan(&value); e != nil {
			break
		}
		values = append(values, value)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return nil, e
	}
	if len(values) > 1 {
		return nil, ErrCorrupt
	}
	if len(values) == 0 {
		return nil, nil
	}
	value, e := jsjson.Parse(values[0])
	if e != nil {
		return nil, ErrCorrupt
	}
	h, e := ParseHead(value)
	if e != nil {
		return nil, e
	}
	if h.Metadata.Identity.SceneID != sceneID || h.Metadata.Identity.MessageID != messageID {
		return nil, ErrCorrupt
	}
	return v.HeadMetadata(h.Metadata.Identity)
}
func (v *View) Current(ref Ref, i Identity) (Eligibility, error) { return v.eligibility(ref, i, false) }
func (v *View) Historical(ref Ref, i Identity) (Eligibility, error) {
	return v.eligibility(ref, i, true)
}
func (v *View) eligibility(ref Ref, i Identity, historical bool) (Eligibility, error) {
	if !ref.Valid() || !i.Valid() {
		return Eligibility{}, ErrInvalid
	}
	no := func(reason string) (Eligibility, error) { return Eligibility{Reason: reason}, nil }
	exists, e := v.exists()
	if e != nil {
		return Eligibility{}, e
	}
	if !exists {
		return no("missing")
	}
	revoked, e := v.revoked(i)
	if e != nil {
		return Eligibility{}, e
	}
	if revoked {
		return no("recalled")
	}
	head, e := v.head(i)
	if e != nil {
		return Eligibility{}, e
	}
	if head == nil {
		return no("missing")
	}
	if head.Metadata.Ref.EventID != ref.EventID {
		return no("scope_mismatch")
	}
	if head.Deleted || head.Metadata.Ref.DeletionEpoch != ref.DeletionEpoch {
		return no("recalled")
	}
	if !historical && head.Metadata.Ref.Revision != ref.Revision {
		return no("revision_changed")
	}
	var value string
	exists, e = v.one("SELECT value FROM source_event_revisions WHERE event_id=? AND revision=?", []any{ref.EventID, ref.Revision}, &value)
	if e != nil {
		return Eligibility{}, e
	}
	if !exists {
		return no("missing")
	}
	parsed, e := jsjson.Parse(value)
	if e != nil {
		return Eligibility{}, ErrCorrupt
	}
	m, e := ParseMetadata(parsed)
	if e != nil {
		return Eligibility{}, e
	}
	if m.Ref != ref || m.Identity != i {
		return Eligibility{}, ErrCorrupt
	}
	if head.Metadata.Ref == ref {
		a, _ := jsjson.Stringify(m.Value())
		b, _ := jsjson.Stringify(head.Metadata.Value())
		if a != b {
			return Eligibility{}, ErrCorrupt
		}
	}
	reason := "current"
	if historical {
		reason = "historical_metadata_only"
	}
	return Eligibility{true, reason, &m}, nil
}
func (v *View) HasEdits(i Identity) (bool, error) {
	exists, e := v.exists()
	if e != nil || !exists {
		return false, e
	}
	h, e := v.head(i)
	return h != nil && h.Metadata.Ref.Revision > 1, e
}
func (v *View) Capture(i Identity, bodyHash string, expected Ref) (*Ref, error) {
	exists, e := v.exists()
	if e != nil || !exists {
		return nil, e
	}
	h, e := v.head(i)
	if e != nil || h == nil {
		return nil, e
	}
	if h.Metadata.BodyHash != bodyHash {
		return nil, nil
	}
	if !expected.Valid() {
		return nil, ErrInvalid
	}
	if h.Metadata.Ref != expected {
		return nil, nil
	}
	eligible, e := v.Current(h.Metadata.Ref, i)
	if e != nil || !eligible.Eligible {
		return nil, e
	}
	r := h.Metadata.Ref
	return &r, nil
}
