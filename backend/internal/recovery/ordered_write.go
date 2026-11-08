package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// OrderedWriteStep is one reached source transaction step. BeforeIntent is a
// server-owned key-custody preparation only: it may load/create an installation
// key and encrypt in memory, but must not access main or authority, publish,
// perform network I/O, start background work, or reenter this writer/owner.
// Reentrancy would deadlock: the authority mutex and main reservation are held. It runs without a SQL capability.
// Apply has exactly the same restrictions as WritePlan.Apply. A failure prevents
// every later step, while preserving already committed authority intent.
type OrderedWriteStep struct {
	// EnsureSecurityState admits only the fixed source security-state schema,
	// after key preparation and before this step appends any durable intent.
	EnsureSecurityState bool
	BeforeIntent        func() error
	Intents             []Revocation
	Apply               func(*MainMutation) error
}
type OrderedWritePlan struct {
	MinimumReader int
	Steps         []OrderedWriteStep
}

// PrepareOrderedWrite preserves the original single-main-transaction contract
// while appending each step's durable intent immediately before its SQL effects.
// This API does not change Write/PrepareWrite semantics. Planning remains pure.
func (a *Authority) PrepareOrderedWrite(ctx context.Context, main *sql.DB, prepare func(*MainRead) (OrderedWritePlan, error)) (Head, error) {
	if prepare == nil {
		return Head{}, errors.New("recovery_write_invalid")
	}
	return a.executeOrderedWrite(ctx, main, prepare, writeHooks{})
}

func (a *Authority) applyOrderedStep(ctx context.Context, conn *sql.Conn, head Head, apply func(*MainMutation) error) (err error) {
	if err = beginOwned(ctx, a.conn, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		cleanup := rollbackOwned(a.conn)
		if cleanup != nil {
			a.cached = false
		}
		err = errors.Join(err, cleanup)
	}()
	if err = authorityWriteMode(ctx, a.conn); err != nil {
		return err
	}
	current, e := a.current(ctx)
	if e != nil {
		return e
	}
	if current != head {
		return errors.New("recovery_authority_head_changed")
	}
	if err = protectMutation(conn, true); err != nil {
		return err
	}
	mutation := &MainMutation{conn: conn, ctx: ctx, active: true}
	defer func() { err = mutation.finish(err) }()
	return apply(mutation)
}

func (a *Authority) executeOrderedWrite(ctx context.Context, main *sql.DB, prepare func(*MainRead) (OrderedWritePlan, error), hooks writeHooks) (head Head, err error) {
	conn, err := main.Conn(ctx)
	if err != nil {
		return head, err
	}
	defer conn.Close()
	if err = writableMain(ctx, conn, a.path); err != nil {
		return head, err
	}
	// Critically, neither authority's mutex nor its SQLite reservation is held
	// while waiting to acquire the main writer. Publication takes authority first
	// but only a WAL read snapshot, so it cannot wait on this writer reservation.
	if err = beginOwned(ctx, conn, "BEGIN IMMEDIATE"); err != nil {
		return head, err
	}
	defer func() {
		err = errors.Join(err, rollbackOwned(conn))
		if err != nil {
			head = Head{}
		}
	}()
	gate, err := ReadGate(ctx, conn)
	if err != nil {
		return head, err
	}
	if gate.Authority != a.path {
		return head, errors.New("recovery_authority_not_bound")
	}
	// BEGIN IMMEDIATE alone does not prove SQLite's handle is writable.
	if _, err = conn.ExecContext(ctx, "UPDATE main.recovery_gate SET id=id WHERE 0"); err != nil {
		return head, err
	}
	var plan OrderedWritePlan
	_, err = preparePlan(ctx, conn, func(read *MainRead) (WritePlan, error) {
		var e error
		plan, e = prepare(read)
		return WritePlan{}, e
	})
	if err != nil {
		return head, err
	}
	if plan.MinimumReader < 1 || plan.MinimumReader > ReaderCapability {
		return head, errors.New("recovery_write_invalid")
	}
	minimumReader := plan.MinimumReader
	events := make([][]writeEvent, len(plan.Steps))
	for i, step := range plan.Steps {
		events[i], err = encodePlan(step.Intents, minimumReader, step.Apply)
		if err != nil {
			return head, err
		}
	}
	if err := a.mu.LockContext(ctx); err != nil {
		return Head{}, err
	}
	defer a.mu.Unlock()
	if !a.writer {
		return head, errors.New("recovery_writer_required")
	}
	head = gate.Head
	for i, step := range plan.Steps {
		if err = ctx.Err(); err != nil {
			return head, err
		}
		if err = a.applyOrderedStep(ctx, conn, head, func(*MainMutation) error { return nil }); err != nil {
			return head, err
		}
		if step.BeforeIntent != nil {
			if err = step.BeforeIntent(); err != nil {
				return head, err
			}
		}
		if step.EnsureSecurityState {
			if err = ensureSecurityStateSchema(ctx, conn); err != nil {
				return head, err
			}
		}
		for _, event := range events[i] {
			head, err = a.appendLocked(ctx, event, head, false, hooks.commit)
			if err != nil {
				return head, err
			}
			if hooks.afterIntent != nil {
				if err = hooks.afterIntent(head); err != nil {
					return head, err
				}
			}
		}
		if err = a.applyOrderedStep(ctx, conn, head, step.Apply); err != nil {
			return head, err
		}
	}
	// There is a real cross-database gap after intent COMMIT. Re-reserve authority
	// and compare again: another authority writer must not be silently ACKed.
	if err = beginOwned(ctx, a.conn, "BEGIN IMMEDIATE"); err != nil {
		return head, err
	}
	defer func() {
		cleanup := rollbackOwned(a.conn)
		if cleanup != nil {
			a.cached = false
		}
		err = errors.Join(err, cleanup)
	}()
	if err = authorityWriteMode(ctx, a.conn); err != nil {
		return head, err
	}
	current, err := a.current(ctx)
	if err != nil {
		return head, err
	}
	if current != head {
		return head, errors.New("recovery_authority_head_changed")
	}
	if err = ctx.Err(); err != nil {
		return head, err
	}
	gate.Head = head
	if int64(minimumReader) > gate.MinimumReader {
		gate.MinimumReader = int64(minimumReader)
	}
	raw, err := json.Marshal(gate)
	if err != nil {
		return head, err
	}
	result, err := conn.ExecContext(ctx, "UPDATE main.recovery_gate SET value=? WHERE id=1", string(raw))
	if err != nil {
		return head, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return head, err
	}
	stored, err := ReadGate(ctx, conn)
	if err != nil {
		return head, err
	}
	if n != 1 || stored != gate {
		return head, errors.New("recovery_gate_invalid")
	}
	if hooks.beforeMainCommit != nil {
		if err = hooks.beforeMainCommit(); err != nil {
			return head, err
		}
	}
	if err = commitOwned(ctx, conn, "main", hooks.commit); err != nil {
		// Main's uncertain outcome is not permission to reuse any cached decision.
		a.cached = false
		return head, err
	}
	return head, nil
}
