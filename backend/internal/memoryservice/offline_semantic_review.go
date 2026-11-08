package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

func (o *OfflineLifecycle) ReviewSemantic(ctx context.Context, scope *recovery.MaintenanceTx, scene, id string, version float64, status string) (out *Object, err error) {
	err = o.inScope(ctx, scope, func(s *offlineFacade, tx *assertions.RevisionTxn) error {
		return tx.Store.WithMaintenanceScope(func(child *recovery.MaintenanceTx) error {
			raw := string(contract.DefaultSettingsInput())
			rows, e := child.QueryContext(ctx, "SELECT value FROM config WHERE key='settings'")
			if e != nil {
				return e
			}
			if rows.Next() {
				e = rows.Scan(&raw)
			}
			if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
				return e
			}
			schemas, e := contract.SharedSchemas()
			if e != nil {
				return e
			}
			settings, e := schemas.Parse("Settings", []byte(raw))
			if e != nil {
				return e
			}
			out, e = s.d.Semantic.WithMaintenanceTx(ctx, child).Review(semantic.Context{SceneID: scene}, id, version, status, settings["memorySemanticEnabled"] == true, s.semanticPrivacyAuthority(ctx, tx, child))
			return e
		})
	})
	return
}
