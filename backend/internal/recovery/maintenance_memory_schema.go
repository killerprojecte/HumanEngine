package recovery

import (
	"context"
	"errors"
	"strings"
)

// InitializeOfflineMemory preserves the source constructor's independent base,
// lineage, relation, commitment, projection and history schema phases.
func (m *Maintenance) InitializeOfflineMemory(ctx context.Context) error {
	for _, statement := range strings.Split(memoryStorageDDL, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if e := m.Apply(ctx, 1, func(tx *MaintenanceTx) error { _, e := tx.ExecContext(ctx, statement); return e }); e != nil {
			return e
		}
	}
	if e := m.Apply(ctx, 1, func(tx *MaintenanceTx) error {
		if _, e := tx.ExecContext(ctx, memorySupportsDDL); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE memories SET value=json_set(value,'$.lineageStatus','unverified') WHERE json_extract(value,'$.origin')='learned' AND json_extract(value,'$.lineageStatus') IS NULL`)
		return e
	}); e != nil {
		return e
	}
	if e := m.Apply(ctx, 1, func(tx *MaintenanceTx) error { return tx.EnsureRelationStorageSchema(ctx) }); e != nil {
		return e
	}
	if e := m.Apply(ctx, 1, func(tx *MaintenanceTx) error { return tx.EnsureCommitmentStorageSchema(ctx) }); e != nil {
		return e
	}
	if e := m.Apply(ctx, 1, func(tx *MaintenanceTx) error { _, e := tx.ExecContext(ctx, projectionOutboxDDL); return e }); e != nil {
		return e
	}
	if e := m.Apply(ctx, 1, func(tx *MaintenanceTx) error {
		var enabled bool
		if e := tx.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&enabled); e != nil {
			return e
		}
		if !enabled {
			return errors.New("projection_fts5_required: build backend with -tags sqlite_fts5")
		}
		_, e := tx.ExecContext(ctx, projectionProcessorDDL)
		return e
	}); e != nil {
		return e
	}
	return m.Apply(ctx, 1, func(tx *MaintenanceTx) error {
		var exists bool
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_assertion_revision_parents')").Scan(&exists); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, assertionHistoryDDL); e != nil {
			return e
		}
		if !exists {
			_, e := tx.ExecContext(ctx, assertionParentBackfill)
			return e
		}
		return nil
	})
}
