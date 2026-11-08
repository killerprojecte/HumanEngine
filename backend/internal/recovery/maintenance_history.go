package recovery

import (
	"context"
	"errors"
)

// RecoveryEntry retains the authority's original bytes. Historical hashes are
// never recomputed from a normalized/reencoded domain event.
type RecoveryEntry struct {
	Sequence int64
	Payload  string
}

// History reads the complete audited history under the same authority snapshot
// as admission. It returns duplicates in sequence order, including forced
// identical events, and does not turn a diagnostic Head into an authorization.
func (m *Maintenance) History(ctx context.Context) (entries []RecoveryEntry, err error) {
	if err = m.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	err = m.snapshot(ctx, func(head Head) error {
		if head != m.initial {
			return errors.New("recovery_authority_head_changed")
		}
		gate, _, e := m.checkGate(ctx, head)
		if e != nil {
			return e
		}
		if gate.Head != m.gate {
			return errors.New("recovery_prefix_invalid")
		}
		rows, e := m.authority.conn.QueryContext(ctx, "SELECT sequence,payload FROM main.revocations ORDER BY sequence")
		if e != nil {
			return e
		}
		entries = []RecoveryEntry{}
		for rows.Next() {
			var entry RecoveryEntry
			if e = rows.Scan(&entry.Sequence, &entry.Payload); e != nil {
				break
			}
			entries = append(entries, entry)
		}
		return errors.Join(e, rows.Err(), rows.Close())
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// SnapshotSequence is the verified staging prefix, not the authority head.
func (m *Maintenance) SnapshotSequence() int64 { return m.gate.Sequence }

// CheckHead is used at every replay step and after filesystem effects. The
// finalizer must check again while holding authority through main's commit.
func (m *Maintenance) CheckHead(ctx context.Context) error {
	if e := m.mu.LockContext(ctx); e != nil {
		return e
	}
	defer m.mu.Unlock()
	return m.snapshot(ctx, func(head Head) error {
		if head != m.initial {
			return errors.New("recovery_authority_head_changed")
		}
		gate, _, e := m.checkGate(ctx, head)
		if e != nil {
			return e
		}
		if gate.Head != m.gate {
			return errors.New("recovery_prefix_invalid")
		}
		return nil
	})
}
