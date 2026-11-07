package recovery

import (
	"context"
	"errors"
)

// EnsureWorkingSetSchema is the offline equivalent of storage.ensure: it
// deliberately applies the live ordered-column audit (not the offline sorted
// xinfo audit), then creates only the fixed four tables when wholly absent.
// It neither enrolls a reader nor appends an intent or ACKs an authority head.
func (t *MaintenanceTx) EnsureWorkingSetSchema(ctx context.Context) error {
	if e := t.CheckScope(ctx); e != nil {
		return e
	}
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	if e := t.usable(ctx); e != nil {
		return e
	}
	exists, e := workingSetSchemaSupported(ctx, t.m.conn)
	if e != nil {
		return errors.Join(e, t.usable(ctx))
	}
	if exists {
		return nil
	}
	_, e = t.m.conn.ExecContext(ctx, workingSetDDL)
	return t.fail(e)
}
