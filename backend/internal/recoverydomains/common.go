// Package recoverydomains owns offline lifecycle mutations for habits, media and experience.
// It uses only the admitted maintenance connection and never learns new evidence.
package recoverydomains

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sort"
	"time"
)

type object = jsjson.Object
type Options struct {
	Compare func(string, string) (int, error)
	Now     func() time.Time
}

func get(o *object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(v any) string  { s, _ := v.(string); return s }
func num(v any) float64 { n, _ := v.(float64); return n }
func obj(v any) *object { o, _ := v.(*object); return o }
func arr(v any) []any   { a, _ := v.([]any); return a }
func requiredArray(o *object, k string) ([]any, error) {
	v, ok := get(o, k).([]any)
	if !ok {
		return nil, errors.New("recovery_domain_material_invalid")
	}
	return v, nil
}
func yes(v any) bool                 { b, _ := v.(bool); return b }
func set(o *object, k string, v any) { _ = o.Set(k, v) }
func makeObject(kv ...any) *object {
	o := new(object)
	for i := 0; i < len(kv); i += 2 {
		set(o, kv[i].(string), kv[i+1])
	}
	return o
}
func encode(v any) (string, error) { return jsjson.Stringify(v) }
func digest(raw string) string     { return fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) }
func key(parts ...string) string {
	a := make([]any, len(parts))
	for i := range parts {
		a[i] = parts[i]
	}
	s, _ := encode(a)
	return digest(s)
}
func canonical(v any, compare func(string, string) (int, error)) (any, error) {
	switch x := v.(type) {
	case *object:
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
		out := new(object)
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
func hash(v any, opts Options) (string, error) {
	v, e := canonical(v, opts.Compare)
	if e != nil {
		return "", e
	}
	s, e := encode(v)
	return digest(s), e
}
func read(ctx context.Context, tx *recovery.MaintenanceTx, q string, args ...any) (*object, error) {
	var raw string
	e := tx.QueryRowContext(ctx, q, args...).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o := obj(v)
	if o == nil {
		return nil, errors.New("recovery_domain_material_invalid")
	}
	return o, nil
}
func values(ctx context.Context, tx *recovery.MaintenanceTx, q string, args ...any) ([]*object, error) {
	rows, e := tx.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []*object{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		o := obj(v)
		if o == nil {
			return nil, errors.New("recovery_domain_material_invalid")
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func exec(ctx context.Context, tx *recovery.MaintenanceTx, q string, args ...any) error {
	_, e := tx.ExecContext(ctx, q, args...)
	return e
}
func exists(ctx context.Context, tx *recovery.MaintenanceTx, q string, args ...any) (bool, error) {
	var n int
	e := tx.QueryRowContext(ctx, q, args...).Scan(&n)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return e == nil, e
}
func recalled(ctx context.Context, tx *recovery.MaintenanceTx, scene, message string) (bool, error) {
	return exists(ctx, tx, "SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", scene, message)
}
func sourceCurrent(ctx context.Context, tx *recovery.MaintenanceTx, scene string, source *object, snapshot, media bool) (bool, error) {
	message := str(get(source, "messageId"))
	rev, e := recalled(ctx, tx, scene, message)
	if e != nil || rev {
		return false, e
	}
	view, e := sourceevents.BorrowReadScope(func() error { return tx.CheckScope(ctx) }, func(q string, a ...any) (sourceevents.Rows, error) { return tx.QueryContext(ctx, q, a...) })
	if e != nil {
		return false, e
	}
	registered, e := view.Registered(scene, message)
	if e != nil {
		return false, e
	}
	ref := get(source, "eventRef")

	if !media && registered == nil {
		return !snapshot || ref == nil, nil
	}
	if media && registered == nil && ref == nil {
		return true, nil
	}
	var identity sourceevents.Identity
	if registered != nil {
		identity = registered.Identity
	}
	sender := str(get(source, "senderId"))
	if !media && sender != "" && identity.SenderID != sender {
		return false, nil
	}
	if media {
		sc, e := read(ctx, tx, "SELECT value FROM scenes WHERE id=?", scene)
		if e != nil || sc == nil {
			return false, e
		}
		identity = sourceevents.Identity{SceneID: scene, MessageID: message, SenderID: sender, AdapterID: str(get(sc, "adapterId")), AccountID: str(get(sc, "accountId"))}
		if registered != nil && (registered.Identity.AdapterID != identity.AdapterID || registered.Identity.AccountID != identity.AccountID || registered.Identity.SenderID != identity.SenderID) {
			return false, nil
		}
	}

	if ref != nil && (media || snapshot) {
		r, e := sourceevents.ParseRef(ref)
		if e != nil {
			return false, e
		}
		status, e := view.Current(r, identity)
		return status.Eligible, e
	}
	if snapshot {
		return false, nil
	}
	edited, e := view.HasEdits(identity)
	return !edited, e
}
func parseEvent(raw string) (*object, error) {
	if e := recovery.ValidateEvent(raw); e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	return obj(v), e
}
func contains(a []any, v any) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
