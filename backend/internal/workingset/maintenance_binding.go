package workingset

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

func (m *maintenanceScope) check() error { return m.tx.CheckBinding(m.ctx, m.owner) }
func NewMaintenanceService(owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context, ports Ports) (*Service, error) {
	if owner == nil || tx == nil || ports.Enabled == nil || ports.ResolveCurrent == nil || ports.BeforeWrite != nil || ports.ObservePinWrite != nil {
		return nil, errors.New("working_set_maintenance_dependencies_required")
	}
	if e := tx.CheckBinding(ctx, owner); e != nil {
		return nil, e
	}
	p := DefaultPolicy()
	if ports.Policy != nil {
		p = *ports.Policy
	}
	if e := p.Validate(); e != nil {
		return nil, e
	}
	if ports.Now == nil || ports.Token == nil {
		return nil, errors.New("working_set_maintenance_clock_required")
	}
	return &Service{maintenance: &maintenanceScope{owner, tx, ctx}, ports: ports, policy: &policyState{value: p}}, nil
}
func (s *Service) withMaintenanceChild(ctx context.Context, tx *recovery.MaintenanceTx) *Service {
	b := *s
	b.maintenance = &maintenanceScope{s.maintenance.owner, tx, ctx}
	b.bindingError = b.maintenance.check()
	return &b
}
func (s *Service) maintenanceScope() *Scope {
	m := s.maintenance
	return &Scope{maintenance: m.tx, check: m.check, query: func(q string, args ...any) (Rows, error) { return m.tx.QueryContext(m.ctx, q, args...) }, prepare: func(q string) (PreparedRead, error) {
		p, e := m.tx.PrepareQuery(m.ctx, q)
		return maintenanceStatement{p}, e
	}, audit: func() (bool, error) { return m.tx.WorkingSetSchemaSupported(m.ctx) }}
}

type maintenanceStatement struct {
	p *recovery.MaintenanceStatement
}

func (p maintenanceStatement) Query(args ...any) (Rows, error) { return p.p.Query(args...) }
func (p maintenanceStatement) Close() error                    { return p.p.Close() }
func (s *Service) requireReader(n int) error {
	if s.maintenance != nil {
		return s.maintenance.tx.Apply(s.maintenance.ctx, n, func(*recovery.MaintenanceTx) error { return nil })
	}
	return s.session.RequireReader(n)
}
func (s *Service) appendIntent(event recovery.Revocation) (recovery.Head, error) {
	if s.maintenance != nil {
		return recovery.Head{}, s.maintenance.tx.RecordReplayEffect(s.maintenance.ctx, event)
	}
	return s.session.AppendIntent(event)
}
func (s *Service) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *Service {
	if s.maintenance == nil {
		b := *s
		b.bindingError = errors.New("working_set_maintenance_required")
		return &b
	}
	return s.withMaintenanceChild(ctx, tx)
}
func (s *Scope) MaintenanceTx() *recovery.MaintenanceTx { return s.maintenance }
