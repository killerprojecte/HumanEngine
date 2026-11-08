package projections

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type maintenanceScope struct {
	owner *recovery.Maintenance
	tx    *recovery.MaintenanceTx
	ctx   context.Context
}

func (o *Outbox) withMaintenanceChild(ctx context.Context, tx *recovery.MaintenanceTx) *Outbox {
	n := *o
	if o.maintenance == nil {
		n.bindingError = errors.New("projection_maintenance_required")
		return &n
	}
	n.maintenance = &maintenanceScope{o.maintenance.owner, tx, ctx}
	n.bindingError = tx.CheckBinding(ctx, n.maintenance.owner)
	return &n
}

type maintenanceRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

func (o *Outbox) query(q string, args ...any) (maintenanceRows, error) {
	if e := o.check(); e != nil {
		return nil, e
	}
	if o.maintenance != nil {
		return o.maintenance.tx.QueryContext(o.maintenance.ctx, q, args...)
	}
	return o.session.Query(q, args...)
}

// NewMaintenanceCoordinator assumes the real source initialization has already
// installed projection schema. It never manufactures a ready/live session.
func NewMaintenanceCoordinator(owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context, enabled func() bool, eligible func(*recovery.MaintenanceTx, string) (*Object, error), options Options) (*Coordinator, error) {
	if owner == nil || tx == nil || enabled == nil || eligible == nil || options.Now == nil || options.Token == nil {
		return nil, errors.New("projection_maintenance_dependencies_required")
	}
	if e := tx.CheckBinding(ctx, owner); e != nil {
		return nil, e
	}
	o := &Outbox{maintenance: &maintenanceScope{owner, tx, ctx}, options: options}
	p := &Processor{outbox: o, maintenanceEligible: eligible, now: options.Now}
	return &Coordinator{outbox: o, processor: p, enabled: enabled}, nil
}
func (c *Coordinator) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *Coordinator {
	n := *c
	n.outbox = c.outbox.withMaintenanceChild(ctx, tx)
	p := *c.processor
	p.outbox = n.outbox
	n.processor = &p
	return &n
}
