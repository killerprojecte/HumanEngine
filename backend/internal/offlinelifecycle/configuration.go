package offlinelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
)

func replayConfiguration(ctx context.Context, owner *recovery.Maintenance, v *object) error {
	return owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		flags := obj(get(v, "flags"))
		if get(v, "kind") == "configuration_flags" {
			raw := string(contract.DefaultSettingsInput())
			var stored string
			e := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE key='settings'").Scan(&stored)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil {
				raw = stored
			}
			schema, e := contract.NewSchemas()
			if e != nil {
				return e
			}
			next, e := schema.ParseOrdered("Settings", []byte(raw))
			if e != nil {
				return e
			}
			for _, m := range flags.Members() {
				_ = next.Set(m.Key, m.Value)
			}
			return settingsstore.SetMaintenance(ctx, tx, next)
		}
		id, e := jsjson.UTF8Text(str(get(v, "sceneId")))
		if e != nil {
			return e
		}
		var raw string
		e = tx.QueryRowContext(ctx, "SELECT value FROM scenes WHERE id=?", id).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			return errors.New("recovery_scene_identity_mismatch")
		}
		if e != nil {
			return e
		}
		parsed, e := jsjson.Parse(raw)
		if e != nil {
			return e
		}
		previous := obj(parsed)
		if previous == nil {
			return errors.New("recovery_scene_identity_mismatch")
		}
		hash, e := scenestore.IdentityHash(previous)
		if e != nil {
			return e
		}
		if hash != get(v, "identityHash") {
			return errors.New("recovery_scene_identity_mismatch")
		}
		next := previous.Clone()
		for _, m := range flags.Members() {
			_ = next.Set(m.Key, m.Value)
		}
		plan, e := scenestore.Prepare(previous, next)
		if e != nil {
			return e
		}
		if plan.Intent != nil {
			if e = tx.RecordReplayEffect(ctx, *plan.Intent); e != nil {
				return e
			}
		}
		if e = tx.RequireReader(ctx, plan.MinimumReader); e != nil {
			return e
		}
		raw, e = jsjson.Stringify(plan.Next)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO scenes(id,value) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", id, raw)
		return e
	})
}
