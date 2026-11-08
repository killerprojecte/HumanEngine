package providertransport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"regexp"
	"time"
)

// SQLLedger owns transaction boundaries, not database readiness or recovery
// authority. The caller must supply an admitted operational database.
type SQLLedger struct {
	DB     *sql.DB
	BootID string
	Now    func() time.Time
}

var _ Ledger = (*SQLLedger)(nil)
var budgetIdentity = regexp.MustCompile(`^[a-zA-Z0-9_.:@/-]{1,160}$`)

func validBudgetID(s string) error {
	if !budgetIdentity.MatchString(s) {
		return errors.New("invalid_budget_identity")
	}
	for i := 0; i+2 < len(s); i++ {
		if s[i:i+3] == "://" {
			return errors.New("invalid_budget_identity")
		}
	}
	return nil
}
func budgetHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func budgetObject(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func canonicalBudget(r Reservation) (string, error) {
	p := new(jsjson.Object)
	for k := range r.Prices {
		found := false
		for _, key := range budgetPriceKeys {
			if k == key {
				found = true
			}
		}
		if !found {
			return "", errors.New("invalid_budget_metadata")
		}
	}
	if e := budgetInteger(int64(r.Attempt)); e != nil {
		return "", e
	}
	if r.Attempt < 1 || (r.Lane != "foreground" && r.Lane != "realtime" && r.Lane != "maintenance") || (r.Operation != "complete" && r.Operation != "embedding") {
		return "", errors.New("invalid_budget_request")
	}
	for _, k := range budgetPriceKeys {
		if v, ok := r.Prices[k]; ok {
			if _, e := budgetRate(v); e != nil {
				return "", e
			}
			_ = p.Set(k, v)
		}
	}
	if r.Operation == "embedding" && r.Estimate.OutputUpperTokens != 0 {
		return "", errors.New("invalid_budget_estimate")
	}
	for _, s := range []string{r.ID, r.RequestID, r.ProviderID, r.ModelID, r.Estimate.EstimatorVersion, r.Estimate.ContractVersion} {
		if e := validBudgetID(s); e != nil {
			return "", e
		}
	}
	if r.TurnID != "" {
		if e := validBudgetID(r.TurnID); e != nil {
			return "", e
		}
	}
	categories := []any{}
	for _, s := range uniqueSorted(r.Estimate.InputCategories) {
		categories = append(categories, s)
	}
	e := r.Estimate
	estimate := budgetObject("source", e.Source, "estimatorVersion", e.EstimatorVersion, "contractVersion", e.ContractVersion, "enforceable", e.Enforceable, "inputUpperTokens", float64(e.InputUpperTokens), "outputUpperTokens", float64(e.OutputUpperTokens), "inputCategories", categories)
	o := budgetObject("id", r.ID, "requestId", r.RequestID, "attempt", float64(r.Attempt), "providerId", r.ProviderID, "modelId", r.ModelID, "operation", r.Operation, "lane", r.Lane)
	if r.TurnID != "" {
		_ = o.Set("turnId", r.TurnID)
	}
	_ = o.Set("estimate", estimate)
	_ = o.Set("prices", p)
	return jsjson.Stringify(o)
}
func validBudgetPolicy(p BudgetPolicy) error {
	if p.Mode != "" && p.Mode != "strict" && p.Mode != "best_effort" {
		return errors.New("invalid_budget_mode")
	}
	ns := []int64{p.DailyNanoUSD, p.MaxUnresolvedNanoUSD}
	for k, v := range p.LaneDailyNanoUSD {
		if k != "foreground" && k != "realtime" && k != "maintenance" {
			return errors.New("invalid_budget_metadata")
		}
		ns = append(ns, v)
	}
	for _, v := range []*int64{p.PerTurnNanoUSD, p.PerTurnTokens, p.PerTurnCalls} {
		if v != nil {
			ns = append(ns, *v)
		}
	}
	for _, n := range ns {
		if e := budgetInteger(n); e != nil {
			return e
		}
	}
	return nil
}
func policyJSON(p BudgetPolicy) string {
	o := budgetObject()
	if p.Mode != "" {
		_ = o.Set("mode", p.Mode)
	}
	_ = o.Set("dailyNanoUsd", float64(p.DailyNanoUSD))
	_ = o.Set("maxUnresolvedNanoUsd", float64(p.MaxUnresolvedNanoUSD))
	if p.LaneDailyNanoUSD != nil {
		lanes := new(jsjson.Object)
		for _, k := range []string{"foreground", "realtime", "maintenance"} {
			if v, ok := p.LaneDailyNanoUSD[k]; ok {
				_ = lanes.Set(k, float64(v))
			}
		}
		_ = o.Set("laneDailyNanoUsd", lanes)
	}
	for i, v := range []*int64{p.PerTurnNanoUSD, p.PerTurnTokens, p.PerTurnCalls} {
		if v != nil {
			_ = o.Set([]string{"perTurnNanoUsd", "perTurnTokens", "perTurnCalls"}[i], float64(*v))
		}
	}
	s, _ := jsjson.Stringify(o)
	return s
}
func (l *SQLLedger) at() string {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	return now().UTC().Format("2006-01-02T15:04:05.000Z")
}
func (l *SQLLedger) transaction(ctx context.Context, fn func(*sql.Conn) error) (err error) {
	c, err := l.DB.Conn(ctx)
	if err != nil {
		return
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	if err = fn(c); err != nil {
		return
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return
}
func NewSQLLedger(ctx context.Context, db *sql.DB, bootID string, now func() time.Time) (*SQLLedger, error) {
	if e := validBudgetID(bootID); e != nil {
		return nil, e
	}
	l := &SQLLedger{db, bootID, now}
	err := l.transaction(ctx, func(c *sql.Conn) error {
		_, e := c.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS budget_attempts(id TEXT PRIMARY KEY,payload_hash TEXT NOT NULL,payload TEXT NOT NULL,policy TEXT NOT NULL,boot_id TEXT NOT NULL,state TEXT NOT NULL,window_start TEXT NOT NULL,lane TEXT NOT NULL,turn_id TEXT,reserved_nano INTEGER NOT NULL,known_nano INTEGER NOT NULL,held_nano INTEGER NOT NULL,created_at TEXT NOT NULL,started_at TEXT,settled_at TEXT,usage TEXT,receipt_hash TEXT,overrun INTEGER NOT NULL DEFAULT 0,unbounded_exposure INTEGER NOT NULL DEFAULT 0); CREATE INDEX IF NOT EXISTS budget_window_idx ON budget_attempts(window_start,lane); CREATE INDEX IF NOT EXISTS budget_turn_idx ON budget_attempts(turn_id);CREATE TABLE IF NOT EXISTS budget_reconciliations(evidence_id TEXT PRIMARY KEY,attempt_id TEXT NOT NULL,receipt_hash TEXT NOT NULL,reconciled_at TEXT NOT NULL);`)
		if e != nil {
			return e
		}
		rows, e := c.QueryContext(ctx, "PRAGMA table_info(budget_attempts)")
		if e != nil {
			return e
		}
		has := false
		for rows.Next() {
			var cid, nn, pk int
			var name, typ string
			var def any
			if e = rows.Scan(&cid, &name, &typ, &nn, &def, &pk); e != nil {
				rows.Close()
				return e
			}
			if name == "unbounded_exposure" {
				has = true
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if !has {
			if _, e = c.ExecContext(ctx, "ALTER TABLE budget_attempts ADD COLUMN unbounded_exposure INTEGER NOT NULL DEFAULT 0"); e != nil {
				return e
			}
		}
		_, e = c.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS budget_held_nonzero_idx ON budget_attempts(id) WHERE held_nano IS NOT 0;CREATE INDEX IF NOT EXISTS budget_overrun_idx ON budget_attempts(id) WHERE overrun IS NOT 0;CREATE INDEX IF NOT EXISTS budget_unbounded_idx ON budget_attempts(id) WHERE unbounded_exposure IS NOT 0;CREATE INDEX IF NOT EXISTS budget_request_attempt_idx ON budget_attempts(json_extract(payload,'$.requestId'),json_extract(payload,'$.attempt'));`)
		return e
	})
	return l, err
}

type BudgetAttemptView struct {
	Currency                                   string
	Request                                    Reservation
	Policy                                     BudgetPolicy
	BootID, State, WindowStart, CreatedAt      string
	StartedAt, SettledAt, Usage, ReceiptHash   sql.NullString
	ReservedNanoUSD, KnownNanoUSD, HeldNanoUSD int64
	Overrun, UnboundedExposure                 bool
}
type budgetRow struct {
	BudgetAttemptView
	payload, hash string
}

const budgetColumns = "payload,payload_hash,policy,boot_id,state,window_start,reserved_nano,known_nano,held_nano,created_at,started_at,settled_at,usage,receipt_hash,overrun,unbounded_exposure"

func readBudgetRow(scan interface{ Scan(...any) error }) (r budgetRow, err error) {
	var policy string
	r.Currency = "USD"
	err = scan.Scan(&r.payload, &r.hash, &policy, &r.BootID, &r.State, &r.WindowStart, &r.ReservedNanoUSD, &r.KnownNanoUSD, &r.HeldNanoUSD, &r.CreatedAt, &r.StartedAt, &r.SettledAt, &r.Usage, &r.ReceiptHash, &r.Overrun, &r.UnboundedExposure)
	if err != nil {
		return
	}
	if err = json.Unmarshal([]byte(r.payload), &r.Request); err != nil {
		return
	}
	err = json.Unmarshal([]byte(policy), &r.Policy)
	return
}
func ledgerRow(ctx context.Context, c *sql.Conn, id string) (budgetRow, error) {
	r, e := readBudgetRow(c.QueryRowContext(ctx, "SELECT "+budgetColumns+" FROM budget_attempts WHERE id=?", id))
	if errors.Is(e, sql.ErrNoRows) {
		e = errors.New("budget_attempt_missing")
	}
	return r, e
}
func ledgerRows(ctx context.Context, c *sql.Conn, where string, args ...any) ([]budgetRow, error) {
	rows, e := c.QueryContext(ctx, "SELECT "+budgetColumns+" FROM budget_attempts "+where, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []budgetRow{}
	for rows.Next() {
		r, e := readBudgetRow(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (l *SQLLedger) Get(ctx context.Context, id string) (v BudgetAttemptView, err error) {
	err = l.transaction(ctx, func(c *sql.Conn) error { r, e := ledgerRow(ctx, c, id); v = r.BudgetAttemptView; return e })
	return
}

type BudgetCoverage struct {
	Currency, WindowStart                                                                                                                                   string
	ExternalKnownNanoUSD, ExternalUnknownReceipts, KnownNanoUSD, HeldNanoUSD, CarriedNanoUSD, UnresolvedNanoUSD, AdmissionExposureNanoUSD, RemainingNanoUSD int64
	LaneExposureNanoUSD                                                                                                                                     map[string]int64
	OverrunAttempts, UnboundedAttempts                                                                                                                      int64
}

// These four indexed projections preserve globally carried liabilities without
// materializing historical settled payloads or depending on their JSON shape.
type budgetCoverageRow struct {
	ID, WindowStart, Lane, State string
	KnownNanoUSD, HeldNanoUSD    int64
	Overrun, UnboundedExposure   bool
}

func budgetCoverageRows(ctx context.Context, c *sql.Conn, day string) ([]budgetCoverageRow, error) {
	const projection = "id,window_start,lane,state,known_nano,held_nano,overrun,unbounded_exposure"
	rows, e := c.QueryContext(ctx, "SELECT "+projection+" FROM budget_attempts INDEXED BY budget_window_idx WHERE window_start=? UNION ALL SELECT "+projection+" FROM budget_attempts INDEXED BY budget_held_nonzero_idx WHERE held_nano IS NOT 0 UNION ALL SELECT "+projection+" FROM budget_attempts INDEXED BY budget_overrun_idx WHERE overrun IS NOT 0 UNION ALL SELECT "+projection+" FROM budget_attempts INDEXED BY budget_unbounded_idx WHERE unbounded_exposure IS NOT 0", day)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []budgetCoverageRow{}
	for rows.Next() {
		var r budgetCoverageRow
		if e = rows.Scan(&r.ID, &r.WindowStart, &r.Lane, &r.State, &r.KnownNanoUSD, &r.HeldNanoUSD, &r.Overrun, &r.UnboundedExposure); e != nil {
			return nil, e
		}
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out, rows.Err()
}
func (l *SQLLedger) coverage(ctx context.Context, c *sql.Conn, p BudgetPolicy) (v BudgetCoverage, err error) {
	if err = validBudgetPolicy(p); err != nil {
		return
	}
	v.Currency = "USD"
	v.WindowStart = l.at()[:10]
	v.LaneExposureNanoUSD = map[string]int64{"foreground": 0, "realtime": 0, "maintenance": 0}
	rows, err := budgetCoverageRows(ctx, c, v.WindowStart)
	if err != nil {
		return
	}
	receipts, err := (externalcost.Storage{DB: c}).Current(ctx)
	if err != nil {
		return
	}
	var externalAllTime int64
	for _, receipt := range receipts {
		var r struct {
			Receipt struct {
				Status, BillingDate string
				AmountNanoUSD       int64
			}
		}
		if err = json.Unmarshal([]byte(receipt.JSON()), &r); err != nil {
			return
		}
		if r.Receipt.Status != "known" {
			v.ExternalUnknownReceipts++
		} else {
			externalAllTime, err = budgetSum(externalAllTime, r.Receipt.AmountNanoUSD)
			if err != nil {
				return
			}
			if r.Receipt.BillingDate == v.WindowStart {
				v.ExternalKnownNanoUSD, err = budgetSum(v.ExternalKnownNanoUSD, r.Receipt.AmountNanoUSD)
				if err != nil {
					return
				}
			}
		}
	}
	v.KnownNanoUSD = v.ExternalKnownNanoUSD
	for _, r := range rows {
		current := r.WindowStart == v.WindowStart
		if current {
			v.KnownNanoUSD, err = budgetSum(v.KnownNanoUSD, r.KnownNanoUSD)
			if err != nil {
				return
			}
			v.HeldNanoUSD, err = budgetSum(v.HeldNanoUSD, r.HeldNanoUSD)
		} else {
			v.CarriedNanoUSD, err = budgetSum(v.CarriedNanoUSD, r.HeldNanoUSD)
		}
		if err != nil {
			return
		}
		if r.State == "unknown" || r.State == "partial" {
			v.UnresolvedNanoUSD, err = budgetSum(v.UnresolvedNanoUSD, r.HeldNanoUSD)
			if err != nil {
				return
			}
		}
		known := int64(0)
		if current {
			known = r.KnownNanoUSD
		}
		v.LaneExposureNanoUSD[r.Lane], err = budgetSum(v.LaneExposureNanoUSD[r.Lane], r.HeldNanoUSD, known)
		if err != nil {
			return
		}
		if r.Overrun {
			v.OverrunAttempts++
		}
		if r.UnboundedExposure {
			v.UnboundedAttempts++
		}
	}
	v.AdmissionExposureNanoUSD, err = budgetSum(v.KnownNanoUSD, v.HeldNanoUSD, v.CarriedNanoUSD)
	if err != nil {
		return
	}
	if v.ExternalUnknownReceipts == 0 && (p.Mode == "best_effort" || v.UnboundedAttempts == 0) {
		v.RemainingNanoUSD = max(int64(0), p.DailyNanoUSD-v.AdmissionExposureNanoUSD)
	}
	return
}
func (l *SQLLedger) Coverage(ctx context.Context, p BudgetPolicy) (v BudgetCoverage, e error) {
	e = l.transaction(ctx, func(c *sql.Conn) error { var err error; v, err = l.coverage(ctx, c, p); return err })
	return
}
func (l *SQLLedger) Reserve(ctx context.Context, r Reservation, p BudgetPolicy) error {
	payload, e := canonicalBudget(r)
	if e != nil {
		return e
	}
	amount, e := QuoteEstimate(r.Estimate, r.Prices, p.Mode)
	if e != nil {
		return e
	}
	if e = validBudgetPolicy(p); e != nil {
		return e
	}
	return l.transaction(ctx, func(c *sql.Conn) error {
		var hash string
		e := c.QueryRowContext(ctx, "SELECT payload_hash FROM budget_attempts WHERE id=?", r.ID).Scan(&hash)
		if e == nil {
			if hash != budgetHash(payload) {
				return errors.New("budget_idempotency_conflict")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var duplicate string
		e = c.QueryRowContext(ctx, "SELECT id FROM budget_attempts WHERE json_extract(payload,'$.requestId')=? AND json_extract(payload,'$.attempt')=?", r.RequestID, r.Attempt).Scan(&duplicate)
		if e == nil {
			return errors.New("budget_attempt_identity_conflict")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		coverage, e := l.coverage(ctx, c, p)
		if e != nil {
			return e
		}
		if coverage.ExternalUnknownReceipts > 0 {
			return errors.New("budget_external_cost_unknown")
		}
		if p.Mode != "best_effort" && coverage.UnboundedAttempts > 0 {
			return errors.New("budget_unbounded_exposure")
		}
		overruns, e := ledgerRows(ctx, c, "WHERE overrun IS NOT 0")
		if e != nil {
			return e
		}
		for _, old := range overruns {
			prior := old.Request
			if prior.ProviderID == r.ProviderID && prior.ModelID == r.ModelID && prior.Estimate.ContractVersion == r.Estimate.ContractVersion && prior.Estimate.EstimatorVersion == r.Estimate.EstimatorVersion {
				return errors.New("budget_contract_overrun")
			}
		}
		if coverage.UnresolvedNanoUSD > p.MaxUnresolvedNanoUSD {
			return errors.New("budget_unresolved_limit")
		}
		if amount > coverage.RemainingNanoUSD {
			return errors.New("budget_daily_limit")
		}
		if cap, ok := p.LaneDailyNanoUSD[r.Lane]; ok {
			sum, e := budgetSum(coverage.LaneExposureNanoUSD[r.Lane], amount)
			if e != nil {
				return e
			}
			if sum > cap {
				return errors.New("budget_lane_limit")
			}
		}
		if r.TurnID != "" {
			prior, e := ledgerRows(ctx, c, "WHERE turn_id=? AND state!='released'", r.TurnID)
			if e != nil {
				return e
			}
			if p.PerTurnCalls != nil && int64(len(prior)) >= *p.PerTurnCalls {
				return errors.New("budget_turn_calls")
			}
			amounts := []int64{amount}
			tokens := []int64{r.Estimate.InputUpperTokens, r.Estimate.OutputUpperTokens}
			for _, row := range prior {
				amounts = append(amounts, row.KnownNanoUSD, row.HeldNanoUSD)
				tokens = append(tokens, row.Request.Estimate.InputUpperTokens, row.Request.Estimate.OutputUpperTokens)
			}
			if p.PerTurnNanoUSD != nil {
				sum, e := budgetSum(amounts...)
				if e != nil {
					return e
				}
				if sum > *p.PerTurnNanoUSD {
					return errors.New("budget_turn_amount")
				}
			}
			sum, e := budgetSum(tokens...)
			if e != nil {
				return e
			}
			if p.PerTurnTokens != nil && sum > *p.PerTurnTokens {
				return errors.New("budget_turn_tokens")
			}
		} else if p.PerTurnNanoUSD != nil || p.PerTurnTokens != nil || p.PerTurnCalls != nil {
			return errors.New("budget_turn_identity_required")
		}
		at := l.at()
		var turn any
		if r.TurnID != "" {
			turn = r.TurnID
		}
		_, e = c.ExecContext(ctx, "INSERT INTO budget_attempts(id,payload_hash,payload,policy,boot_id,state,window_start,lane,turn_id,reserved_nano,known_nano,held_nano,created_at) VALUES(?,?,?,?,?,'reserved',?,?,?,?,0,?,?)", r.ID, budgetHash(payload), payload, policyJSON(p), l.BootID, at[:10], r.Lane, turn, amount, amount, at)
		return e
	})
}
func (l *SQLLedger) Start(ctx context.Context, id string) (started bool, e error) {
	e = l.transaction(ctx, func(c *sql.Conn) error {
		r, e := ledgerRow(ctx, c, id)
		if e != nil {
			return e
		}
		if r.BootID != l.BootID {
			return errors.New("budget_attempt_owner")
		}
		if r.State != "reserved" {
			return nil
		}
		v, e := l.coverage(ctx, c, r.Policy)
		if e != nil {
			return e
		}
		if v.ExternalUnknownReceipts > 0 {
			return errors.New("budget_external_cost_unknown")
		}
		if v.AdmissionExposureNanoUSD > r.Policy.DailyNanoUSD {
			return errors.New("budget_daily_limit")
		}
		_, e = c.ExecContext(ctx, "UPDATE budget_attempts SET state='started',started_at=? WHERE id=?", l.at(), id)
		started = e == nil
		return e
	})
	if e != nil {
		started = false
	}
	return
}
func (l *SQLLedger) Release(ctx context.Context, id string) error {
	return l.transaction(ctx, func(c *sql.Conn) error {
		r, e := ledgerRow(ctx, c, id)
		if e != nil {
			return e
		}
		if r.State == "released" {
			return nil
		}
		if r.BootID != l.BootID || r.State != "reserved" {
			return errors.New("budget_release_started")
		}
		_, e = c.ExecContext(ctx, "UPDATE budget_attempts SET state='released',held_nano=0,settled_at=? WHERE id=?", l.at(), id)
		return e
	})
}
func (l *SQLLedger) Unknown(ctx context.Context, id string) error {
	return l.transaction(ctx, func(c *sql.Conn) error {
		r, e := ledgerRow(ctx, c, id)
		if e != nil {
			return e
		}
		if r.State == "unknown" {
			return nil
		}
		if r.State != "started" {
			return errors.New("budget_invalid_transition")
		}
		_, e = c.ExecContext(ctx, "UPDATE budget_attempts SET state='unknown',settled_at=? WHERE id=?", l.at(), id)
		return e
	})
}
func (l *SQLLedger) settle(ctx context.Context, c *sql.Conn, id string, u map[string]any, snapshot string) error {
	r, e := ledgerRow(ctx, c, id)
	if e != nil {
		return e
	}
	hash := budgetHash(snapshot)
	if r.ReceiptHash.Valid && r.ReceiptHash.String != "" {
		if r.ReceiptHash.String != hash {
			return errors.New("budget_receipt_conflict")
		}
		return nil
	}
	if r.State != "started" && r.State != "unknown" {
		return errors.New("budget_invalid_transition")
	}
	known, complete, useful, e := budgetActual(u, r.Request.Prices)
	if e != nil {
		return e
	}
	ordinary, partition := budgetPartition(u)
	if !partition {
		ordinary = usageN(u, "ordinaryInputTokens")
	}
	ttl := int64(0)
	for _, k := range budgetTokenKeys[5:] {
		ttl += usageN(u, k)
	}
	allowed := map[string]bool{}
	for _, k := range r.Request.Estimate.InputCategories {
		allowed[k] = true
	}
	violation := false
	if validBudgetUsage(u) {
		violation = ordinary > 0 && !allowed["inputUsdPerMillion"] || usageN(u, "cacheReadInputTokens") > 0 && !allowed["cacheReadUsdPerMillion"] || usageN(u, "cacheWriteInputTokens")-ttl > 0 && !allowed["cacheWriteUsdPerMillion"]
		for i, k := range budgetTokenKeys[5:] {
			violation = violation || usageN(u, k) > 0 && !allowed[budgetPriceKeys[4+i]]
		}
	}
	overrun := violation || known > r.ReservedNanoUSD || usageN(u, "inputTokens") > r.Request.Estimate.InputUpperTokens || usageN(u, "outputTokens") > r.Request.Estimate.OutputUpperTokens
	state := "unknown"
	held := max(int64(0), r.ReservedNanoUSD-known)
	if useful {
		state = "partial"
	}
	if complete {
		state = "complete"
		held = 0
	}
	_, e = c.ExecContext(ctx, "UPDATE budget_attempts SET state=?,known_nano=?,held_nano=?,usage=?,receipt_hash=?,settled_at=?,overrun=?,unbounded_exposure=? WHERE id=?", state, known, held, snapshot, hash, l.at(), overrun, overrun && !complete, id)
	return e
}
func (l *SQLLedger) Settle(ctx context.Context, id string, input map[string]any) error {
	u, s := budgetUsage(input)
	return l.transaction(ctx, func(c *sql.Conn) error { return l.settle(ctx, c, id, u, s) })
}

// Reconcile requires independently verified receipt evidence. It never accepts
// an estimate as proof and atomically records the evidence identity with settlement.
func (l *SQLLedger) Reconcile(ctx context.Context, id string, input map[string]any, evidenceID string) error {
	if e := validBudgetID(evidenceID); e != nil {
		return e
	}
	u, s := budgetUsage(input)
	return l.transaction(ctx, func(c *sql.Conn) error {
		var priorID, hash string
		e := c.QueryRowContext(ctx, "SELECT attempt_id,receipt_hash FROM budget_reconciliations WHERE evidence_id=?", evidenceID).Scan(&priorID, &hash)
		if e == nil {
			if priorID != id || hash != budgetHash(s) {
				return errors.New("budget_reconciliation_conflict")
			}
			_, e = ledgerRow(ctx, c, id)
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		r, e := ledgerRow(ctx, c, id)
		if e != nil {
			return e
		}
		if r.State != "unknown" && r.State != "partial" {
			return errors.New("budget_invalid_transition")
		}
		_, complete, _, e := budgetActual(u, r.Request.Prices)
		if e != nil {
			return e
		}
		if !complete {
			return errors.New("budget_reconciliation_incomplete")
		}
		if _, e = c.ExecContext(ctx, "UPDATE budget_attempts SET state='unknown',receipt_hash=NULL WHERE id=?", id); e != nil {
			return e
		}
		if e = l.settle(ctx, c, id, u, s); e != nil {
			return e
		}
		_, e = c.ExecContext(ctx, "INSERT INTO budget_reconciliations VALUES(?,?,?,?)", evidenceID, id, budgetHash(s), l.at())
		return e
	})
}

// RecoverDeadBoot is deliberately explicit: elapsed time is not proof of death.
func (l *SQLLedger) RecoverDeadBoot(ctx context.Context, confirmedDeadBootID string) (count int64, e error) {
	if e = validBudgetID(confirmedDeadBootID); e != nil {
		return
	}
	if confirmedDeadBootID == l.BootID {
		return 0, errors.New("budget_active_boot")
	}
	e = l.transaction(ctx, func(c *sql.Conn) error {
		r, e := c.ExecContext(ctx, "UPDATE budget_attempts SET state='released',held_nano=0,settled_at=? WHERE boot_id=? AND state='reserved'", l.at(), confirmedDeadBootID)
		if e != nil {
			return e
		}
		count, e = r.RowsAffected()
		if e != nil {
			return e
		}
		r, e = c.ExecContext(ctx, "UPDATE budget_attempts SET state='unknown',settled_at=? WHERE boot_id=? AND state='started'", l.at(), confirmedDeadBootID)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		count += n
		return e
	})
	if e != nil {
		count = 0
	}
	return
}

// MarshalJSON retains the source's public view, while SQL nulls and canonical
// persisted usage remain available to native callers without re-encoding them.
func (v BudgetAttemptView) MarshalJSON() ([]byte, error) {
	payload, e := canonicalBudget(v.Request)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"request": json.RawMessage(payload), "bootId": v.BootID, "currency": "USD", "policy": json.RawMessage(policyJSON(v.Policy)), "state": v.State, "windowStart": v.WindowStart, "createdAt": v.CreatedAt, "reservedNanoUsd": v.ReservedNanoUSD, "knownNanoUsd": v.KnownNanoUSD, "heldNanoUsd": v.HeldNanoUSD, "overrun": v.Overrun, "unboundedExposure": v.UnboundedExposure}
	if v.StartedAt.Valid && v.StartedAt.String != "" {
		out["startedAt"] = v.StartedAt.String
	}
	if v.SettledAt.Valid && v.SettledAt.String != "" {
		out["settledAt"] = v.SettledAt.String
	}
	if v.Usage.Valid && v.Usage.String != "" {
		out["usage"] = json.RawMessage(v.Usage.String)
	}
	return json.Marshal(out)
}
func (v BudgetCoverage) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"currency": v.Currency, "windowStart": v.WindowStart, "externalKnownNanoUsd": v.ExternalKnownNanoUSD, "externalUnknownReceipts": v.ExternalUnknownReceipts, "knownNanoUsd": v.KnownNanoUSD, "heldNanoUsd": v.HeldNanoUSD, "carriedNanoUsd": v.CarriedNanoUSD, "unresolvedNanoUsd": v.UnresolvedNanoUSD, "admissionExposureNanoUsd": v.AdmissionExposureNanoUSD, "remainingNanoUsd": v.RemainingNanoUSD, "laneExposureNanoUsd": v.LaneExposureNanoUSD, "overrunAttempts": v.OverrunAttempts, "unboundedAttempts": v.UnboundedAttempts})
}
