package providertransport

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/big"
	"regexp"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
)

// BudgetService is the SQL-backed gate source. It checks restoration admission
// even when budgeting is disabled, and captures mode/output-cap identity while
// rereading current spending policy for each reservation. Database bootstrap and
// authority admission are owned by installation/runtime composition.
type BudgetService struct {
	DB        *sql.DB
	Ledger    *SQLLedger
	Estimator Estimator
	Contract  func(Descriptor) *ProviderContract
}

func (s *BudgetService) Current(ctx context.Context) (BudgetGate, error) {
	settings, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	enabled, _ := settings.Get("budgetExperimentEnabled")
	if enabled != true {
		return nil, nil
	}
	modeValue, _ := settings.Get("budgetMode")
	mode := modeValue.(string)
	capValue, _ := settings.Get("budgetMaxOutputTokens")
	cap := int64(capValue.(float64))
	if s.Ledger == nil {
		return nil, errors.New("budget_owner_required")
	}
	return &NativeGate{Ledger: s.Ledger, Mode: mode, MaxOutputTokens: cap, Estimator: s.Estimator, Contract: s.Contract, Policy: func(ctx context.Context) (BudgetPolicy, error) {
		current, e := s.settings(ctx)
		if e != nil {
			return BudgetPolicy{}, e
		}
		enabled, _ := current.Get("budgetExperimentEnabled")
		newMode, _ := current.Get("budgetMode")
		newCap, _ := current.Get("budgetMaxOutputTokens")
		if enabled != true || newMode != mode || int64(newCap.(float64)) != cap {
			return BudgetPolicy{}, errors.New("budget_configuration_changed")
		}
		return settingsPolicy(current)
	}}, nil
}
func (s *BudgetService) settings(ctx context.Context) (*jsjson.Object, error) {
	if s.DB == nil {
		return nil, errors.New("budget_owner_required")
	}
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	held, e := readRestoredHold(ctx, tx)
	if e != nil {
		return nil, e
	}
	if held {
		return nil, errors.New("budget_restore_accounting_hold")
	}
	var encoded string
	e = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE key='settings'").Scan(&encoded)
	if e == sql.ErrNoRows {
		encoded = "{}"
	} else if e != nil {
		return nil, e
	}
	raw, e := jsjson.Parse(encoded)
	if e != nil {
		return nil, e
	}
	settings, e := settingsstore.ParseBudgetSettings(raw)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return settings, nil
}

var restoredUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func readRestoredHold(ctx context.Context, tx *sql.Tx) (bool, error) {
	invalid := func() (bool, error) { return false, errors.New("recovery_model_hold_invalid") }
	var kind string
	e := tx.QueryRowContext(ctx, "SELECT type FROM sqlite_master WHERE name='recovery_model_hold'").Scan(&kind)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if kind != "table" {
		return invalid()
	}
	rows, e := tx.QueryContext(ctx, "PRAGMA table_xinfo(recovery_model_hold)")
	if e != nil {
		return false, e
	}
	names := []string{}
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var name, typ string
		var defaultValue any
		if e = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk, &hidden); e != nil {
			rows.Close()
			return false, e
		}
		names = append(names, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	if strings.Join(names, ",") != "id,restore_id,status" {
		return invalid()
	}
	rows, e = tx.QueryContext(ctx, "SELECT id,restore_id,status FROM recovery_model_hold LIMIT 2")
	if e != nil {
		return false, e
	}
	defer rows.Close()
	count := 0
	held := false
	for rows.Next() {
		var id any
		var restoreID, status any
		if e = rows.Scan(&id, &restoreID, &status); e != nil {
			return false, e
		}
		count++
		idValue, ok := id.(int64)
		text, okText := restoreID.(string)
		state, okState := status.(string)
		if !ok || idValue != 1 || !okText || !restoredUUID.MatchString(text) || !okState || (state != "held" && state != "manually_acknowledged") {
			return invalid()
		}
		held = state == "held"
	}
	if e = rows.Err(); e != nil {
		return false, e
	}
	if count != 1 {
		return invalid()
	}
	return held, nil
}
func settingsPolicy(settings *jsjson.Object) (BudgetPolicy, error) {
	number := func(k string) float64 { v, _ := settings.Get(k); return v.(float64) }
	nano := func(k string) (int64, error) {
		r, e := budgetRate(number(k))
		if e != nil {
			return 0, e
		}
		r.Mul(r, big.NewRat(1000000000, 1))
		return budgetCeil(r)
	}
	daily, e := nano("budgetDailyUsd")
	if e != nil {
		return BudgetPolicy{}, e
	}
	unresolved, e := nano("budgetMaxUnresolvedUsd")
	if e != nil {
		return BudgetPolicy{}, e
	}
	turn, e := nano("budgetPerTurnUsd")
	if e != nil {
		return BudgetPolicy{}, e
	}
	tokens, calls := int64(number("budgetPerTurnTokens")), int64(number("budgetPerTurnCalls"))
	mode, _ := settings.Get("budgetMode")
	return BudgetPolicy{Mode: mode.(string), DailyNanoUSD: daily, MaxUnresolvedNanoUSD: unresolved, PerTurnNanoUSD: &turn, PerTurnTokens: &tokens, PerTurnCalls: &calls, LaneDailyNanoUSD: map[string]int64{"realtime": int64(math.Floor(float64(daily) * number("budgetRealtimeRatio"))), "maintenance": int64(math.Floor(float64(daily) * number("budgetMaintenanceRatio")))}}, nil
}
