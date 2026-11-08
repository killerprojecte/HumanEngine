package settingsstore

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// SetMaintenance uses the same transition planner as live Store.Set, borrowing
// the final caller's staging transaction. It emits no new durable authority.
func SetMaintenance(ctx context.Context, tx *recovery.MaintenanceTx, input any) error {
	read := func(key string, fallback any) (any, error) {
		var raw string
		e := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", key).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			return fallback, nil
		}
		if e != nil {
			return nil, e
		}
		return jsjson.Parse(raw)
	}
	previous, e := read("settings", nil)
	if e != nil {
		return e
	}
	p, e := prepare(previous, input, func(key string) (any, error) { return read(key, float64(0)) })
	if e != nil {
		return e
	}
	for _, intent := range p.intents {
		if e = tx.RecordReplayEffect(ctx, intent); e != nil {
			return e
		}
	}
	for _, u := range p.updates {
		if _, e = tx.ExecContext(ctx, "INSERT OR REPLACE INTO config(key,value) VALUES(?,?)", u.key, u.raw); e != nil {
			return e
		}
	}
	if p.afterFlagsError != nil {
		return p.afterFlagsError
	}
	if p.securityChanged {
		revision, e := securityRevision()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS recovery_security_state(id INTEGER PRIMARY KEY CHECK(id=1),revision TEXT NOT NULL)"); e != nil {
			return e
		}
		if e = tx.RecordReplayEffect(ctx, recovery.SecurityState{Revision: revision}); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO recovery_security_state VALUES(1,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision", revision); e != nil {
			return e
		}
	}
	raw, e := jsjson.Stringify(input)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO config(key,value) VALUES('settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", raw)
	return e
}
