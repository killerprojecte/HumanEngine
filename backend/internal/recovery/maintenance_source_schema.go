package recovery

import "context"

func (t *MaintenanceTx) EnsureSourceEventSchema(ctx context.Context) error {
	_, e := t.ExecContext(ctx, sourceEventDDL)
	return e
}
