package providertransport

import (
	"context"
	"database/sql"
	"encoding/json"
)

type BudgetRecovery struct {
	RestoreID           string `json:"restoreId,omitempty"`
	Held                bool   `json:"held"`
	RestorationCoverage string `json:"restorationCoverage"`
}

// ReadBudgetRecovery projects restoration status on the caller's admitted
// read snapshot. Unlike provider admission, a held installation is readable.
func (s *BudgetService) ReadBudgetRecovery(ctx context.Context, q *sql.Conn) ([]byte, error) {
	hold, err := readRestoredHold(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(hold)
}

// ReadBudgetCoverage reuses the ledger's arithmetic on an existing read
// snapshot. It does not acquire a writer or change any accounting state.
func (s *BudgetService) ReadBudgetCoverage(ctx context.Context, q *sql.Conn) ([]byte, error) {
	hold, err := readRestoredHold(ctx, q)
	if err != nil {
		return nil, err
	}
	settings, err := readBudgetSettings(ctx, q)
	if err != nil {
		return nil, err
	}
	policy, err := settingsPolicy(settings)
	if err != nil {
		return nil, err
	}
	coverage, err := s.Ledger.coverage(ctx, q, policy)
	if err != nil {
		return nil, err
	}
	var partial, unknown, pending int64
	err = q.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE state='partial'), count(*) FILTER (WHERE state='unknown'),
		count(*) FILTER (WHERE state IN ('reserved','started'))
		FROM budget_attempts WHERE state IN ('reserved','started','partial','unknown')`).Scan(&partial, &unknown, &pending)
	if err != nil {
		return nil, err
	}
	status := "settled"
	switch {
	case coverage.ExternalUnknownReceipts != 0:
		status = "external_incomplete"
	case coverage.UnboundedAttempts != 0:
		status = "unbounded"
	case partial != 0 || unknown != 0 || pending != 0:
		status = "pending"
	}
	externalStatus := "known_imported_only"
	if coverage.ExternalUnknownReceipts != 0 {
		externalStatus = "incomplete"
	}
	estimate := "best_effort_not_hard_limit"
	if policy.Mode == "strict" {
		estimate = "verified_contract_required"
	}
	result := coverage.wire()
	result["externalCosts"] = map[string]any{
		"currency": "USD", "windowStart": coverage.WindowStart,
		"knownTodayNanoUsd": coverage.ExternalKnownNanoUSD, "knownAllTimeNanoUsd": coverage.externalKnownAllTime,
		"unknownReceipts": coverage.ExternalUnknownReceipts, "knownReceipts": coverage.externalKnownReceipts,
		"coverage": externalStatus,
	}
	result["admissionHold"] = hold.Held
	result["restorationCoverage"] = hold.RestorationCoverage
	if hold.RestoreID != "" {
		result["restorationId"] = hold.RestoreID
	}
	enabled, _ := settings.Get("budgetExperimentEnabled")
	result["enabled"] = enabled
	result["mode"] = policy.Mode
	result["estimateKind"] = estimate
	result["amountUnit"] = "nanoUSD"
	result["usdPerNano"] = 1e-9
	result["knownUsd"] = float64(coverage.KnownNanoUSD) / 1e9
	result["heldUsd"] = float64(coverage.HeldNanoUSD) / 1e9
	result["remainingUsd"] = float64(coverage.RemainingNanoUSD) / 1e9
	result["partialAttempts"] = partial
	result["unknownAttempts"] = unknown
	result["pendingAttempts"] = pending
	result["coverage"] = status
	return json.Marshal(result)
}
