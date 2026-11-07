package recovery

import "context"

// ValidateReplayEffect reuses the live typed codec without appending an intent,
// changing the authority, or ACKing a head. Source maintenance still validates
// recordCommitmentMutation before revoke suppresses its durable append.
func (t *MaintenanceTx) ValidateReplayEffect(ctx context.Context, event Revocation) error {
	if e := t.CheckScope(ctx); e != nil {
		return e
	}
	_, e := encodeRevocation(event)
	return e
}
