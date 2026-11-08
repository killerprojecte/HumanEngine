package recovery

import "context"

func (t *MaintenanceTx) RequireReader(ctx context.Context, minimum int) error {
	return t.Apply(ctx, minimum, func(*MaintenanceTx) error { return nil })
}
func (t *MaintenanceTx) EnsureKnowledgeStorageSchema(ctx context.Context) error {
	return t.Apply(ctx, 1, func(c *MaintenanceTx) error { _, e := c.ExecContext(ctx, knowledgeStorageDDL); return e })
}
func (t *MaintenanceTx) EnsureRelationStorageSchema(ctx context.Context) error {
	return t.Apply(ctx, 1, func(c *MaintenanceTx) error { _, e := c.ExecContext(ctx, relationStorageDDL); return e })
}
func (t *MaintenanceTx) EnsureSemanticCandidatesSchema(ctx context.Context) error {
	_, e := t.ExecContext(ctx, semanticCandidatesDDL)
	return e
}
func (t *MaintenanceTx) EnsureSemanticAttemptsSchema(ctx context.Context) error {
	_, e := t.ExecContext(ctx, semanticAttemptsDDL)
	return e
}
