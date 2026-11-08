package recovery

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// FinalizeVerified is reserved for the offline reconciliation owner. verify
// must run every domain's final proof and working-set reset in the supplied
// scope. It must not call a root Maintenance method, perform filesystem work,
// or change the gate. A nil verifier is never evidence of readiness.
//
// The gate is the main database's ACK. No new authority event is appended.
// Authority stays read-locked through the main commit; an uncertain commit is
// returned unchanged, never retried. The staging connection remains exclusively
// owned by the caller. Live entrypoints still require their normal admission.
func (m *Maintenance) FinalizeVerified(ctx context.Context, verify func(*MaintenanceTx) error) (Head, error) {
	return m.finalizeVerified(ctx, verify, nil)
}
func (m *Maintenance) finalizeVerified(ctx context.Context, verify func(*MaintenanceTx) error, commit commitExec) (head Head, err error) {
	if verify == nil {
		return head, errors.New("recovery_verification_required")
	}
	if err = m.mu.LockContext(ctx); err != nil {
		return head, err
	}
	defer m.mu.Unlock()
	m.failed = nil
	nested, err := transactionActive(m.conn)
	if err != nil {
		return head, err
	}
	const savepoint = "recovery_offline_finalize"
	if nested {
		_, err = m.conn.ExecContext(ctx, "SAVEPOINT "+savepoint)
	} else {
		err = beginOwned(ctx, m.conn, "BEGIN IMMEDIATE")
	}
	if err != nil {
		return head, err
	}
	done := false
	defer func() {
		if done {
			return
		}
		head = Head{}
		if !nested {
			err = errors.Join(err, rollbackOwned(m.conn))
			return
		}
		active, e := transactionActive(m.conn)
		err = errors.Join(err, e)
		if active {
			_, e = m.conn.ExecContext(context.Background(), "ROLLBACK TO "+savepoint)
			err = errors.Join(err, e)
			_, e = m.conn.ExecContext(context.Background(), "RELEASE "+savepoint)
			err = errors.Join(err, e)
		}
	}()
	if nested {
		if _, err = m.conn.ExecContext(ctx, "UPDATE main.recovery_gate SET value=value WHERE 0"); err != nil {
			return head, err
		}
	}
	err = m.snapshot(ctx, func(current Head) error {
		if current != m.initial {
			return errors.New("recovery_authority_head_changed")
		}
		gate, _, e := m.checkGate(ctx, current)
		if e != nil {
			return e
		}
		if gate.Head != m.gate {
			return errors.New("recovery_prefix_invalid")
		}
		tx := &MaintenanceTx{m: m, active: true}
		func() {
			defer func() { e = tx.finish(e) }()
			e = verify(tx)
			if e == nil {
				e = markRestoredHold(ctx, tx)
			}
		}()
		if e != nil {
			return e
		}
		// Domain verification can transactionally upgrade the reader; never restore
		// the pre-verification reader over that upgrade.
		after, raw, e := m.checkGate(ctx, current)
		if e != nil {
			return e
		}
		if after.Head != gate.Head || after.MinimumReader < gate.MinimumReader {
			return errors.New("recovery_gate_invalid")
		}
		value, e := jsjson.NewObject(jsjson.Member{Key: "instanceId", Value: current.InstanceID}, jsjson.Member{Key: "sequence", Value: float64(current.Sequence)}, jsjson.Member{Key: "hash", Value: current.Hash})
		if e != nil {
			return e
		}
		if e = raw.Set("head", value); e != nil {
			return e
		}
		if e = raw.Set("state", "ready"); e != nil {
			return e
		}
		encoded, e := jsjson.Stringify(raw)
		if e != nil {
			return e
		}
		if _, e = m.conn.ExecContext(ctx, "UPDATE main.recovery_gate SET value=? WHERE id=1", encoded); e != nil {
			return e
		}
		active, e := transactionActive(m.conn)
		if e != nil {
			return e
		}
		if !active {
			return errors.New("recovery_main_transaction_lost")
		}
		actual, _, e := maintenanceMetadata(ctx, m.conn)
		if e != nil {
			return e
		}
		if actual.Head != current || actual.State != "ready" || actual.Authority != gate.Authority || actual.MinimumReader != after.MinimumReader {
			return errors.New("recovery_gate_invalid")
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if nested {
			_, e = m.conn.ExecContext(ctx, "RELEASE "+savepoint)
		} else {
			e = commitOwned(ctx, m.conn, "maintenance", commit)
		}
		if e == nil {
			head = current
		}
		return e
	})
	done = err == nil
	return head, err
}

// Restores always receive a fresh hold, including copies of an acknowledged
// snapshot. The old table and row are validated before any replacement.
func markRestoredHold(ctx context.Context, tx *MaintenanceTx) error {
	var kind string
	rows, e := tx.QueryContext(ctx, "SELECT type FROM sqlite_master WHERE name='recovery_model_hold'")
	if e != nil {
		return e
	}
	exists := rows.Next()
	if exists {
		e = rows.Scan(&kind)
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return e
	}
	if exists {
		if kind != "table" {
			return errors.New("recovery_model_hold_invalid")
		}
		if e = exactColumns(ctx, tx, "recovery_model_hold", []string{"id", "restore_id", "status"}, false, "recovery_model_hold_invalid"); e != nil {
			return e
		}
		rows, e = tx.QueryContext(ctx, "SELECT id,restore_id,status FROM recovery_model_hold LIMIT 2")
		if e != nil {
			return e
		}
		count := 0
		for rows.Next() {
			var id, restore, status any
			if e = rows.Scan(&id, &restore, &status); e != nil {
				break
			}
			count++
			r, ok := restore.(string)
			if id != int64(1) || !ok || !uuid4Pattern.MatchString(r) || (status != "held" && status != "manually_acknowledged") {
				e = errors.New("recovery_model_hold_invalid")
				break
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return e
		}
		if count != 1 {
			return errors.New("recovery_model_hold_invalid")
		}
	}
	id, e := installationUUID()
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS recovery_model_hold(id INTEGER PRIMARY KEY CHECK(id=1),restore_id TEXT NOT NULL,status TEXT NOT NULL)"); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO recovery_model_hold VALUES(1,?,'held') ON CONFLICT(id) DO UPDATE SET restore_id=excluded.restore_id,status='held'", id)
	return e
}
