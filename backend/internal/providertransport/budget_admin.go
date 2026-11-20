package providertransport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
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

var ErrBudgetRecoveryConflict = errors.New("recovery_model_review_conflict")

// AcknowledgeBudgetRecovery updates only the currently held restoration. It
// neither appends an accounting event nor recalculates historical coverage.
func (s *BudgetService) AcknowledgeBudgetRecovery(ctx context.Context, authority *recovery.Authority, restoreID string) error {
	_, err := authority.Write(ctx, s.DB, nil, 1, func(tx *recovery.MainMutation) error {
		current, err := readRestoredHoldRows(func(statement string) (*recovery.MainRows, error) {
			return tx.Query(statement)
		}, tx.RestoredModelHoldSchema)
		if err != nil {
			return err
		}
		if current.RestoreID != restoreID {
			return ErrBudgetRecoveryConflict
		}
		if current.Held {
			_, err = tx.Exec("UPDATE main.recovery_model_hold SET status='manually_acknowledged' WHERE id=1 AND restore_id=? AND status='held'", restoreID)
		}
		return err
	})
	return err
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

func (s *BudgetService) ImportExternalCost(ctx context.Context, authority *recovery.Authority, input externalcost.ImportInput) (externalcost.Receipt, recovery.Head, error) {
	return externalcost.Import(ctx, authority, s.DB, input)
}

func (s *BudgetService) CorrectExternalCost(ctx context.Context, authority *recovery.Authority, input externalcost.CorrectionInput) (externalcost.Receipt, recovery.Head, error) {
	return externalcost.Correct(ctx, authority, s.DB, input)
}

// ReadExternalCosts lists only on the existing admitted snapshot. Validate
// all current receipts before paging so a corrupt off-page row cannot hide.
func (s *BudgetService) ReadExternalCosts(ctx context.Context, q *sql.Conn, page, size int, compare func(string, string) (int, error)) ([]byte, error) {
	rows, err := (externalcost.Storage{DB: q}).Current(ctx)
	if err != nil {
		return nil, err
	}
	var compareErr error
	slices.SortStableFunc(rows, func(a, b externalcost.Receipt) int {
		if n := strings.Compare(b.RecordedAt(), a.RecordedAt()); n != 0 {
			return n
		}
		if compareErr != nil {
			return 0
		}
		n, err := compare(a.ID(), b.ID())
		compareErr = err
		return n
	})
	if compareErr != nil {
		return nil, compareErr
	}
	total := len(rows)
	start := min((page-1)*size, total)
	end := min(start+size, total)
	return json.Marshal(struct {
		Items    []externalcost.Receipt `json:"items"`
		Total    int                    `json:"total"`
		Page     int                    `json:"page"`
		PageSize int                    `json:"pageSize"`
	}{rows[start:end], total, page, size})
}
