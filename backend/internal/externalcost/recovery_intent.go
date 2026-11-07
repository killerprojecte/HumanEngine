package externalcost

import "github.com/killerprojecte/HumanEngine/backend/internal/recovery"

// RecoveryEvent qualifies this storage receipt against the recovery contract's
// stricter revision/correction relationship before constructing a typed intent.
// It does not append authority, enroll a reader, or change application state.
func (r Receipt) RecoveryEvent() (recovery.ExternalCostReceipt, error) {
	return recovery.ParseExternalCostReceiptIntent(r.JSON())
}
