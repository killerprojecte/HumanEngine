package providertransport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"reflect"
	"sort"
	"sync"
)

type Estimate struct {
	Source, EstimatorVersion, ContractVersion string
	Enforceable                               bool
	InputUpperTokens, OutputUpperTokens       int64
	InputCategories                           []string
}
type ProviderContract struct {
	Version          string
	OutputCapHonored bool
	InputCategories  []string
}
type BudgetPolicy struct {
	Mode                                        string
	DailyNanoUSD, MaxUnresolvedNanoUSD          int64
	LaneDailyNanoUSD                            map[string]int64
	PerTurnNanoUSD, PerTurnTokens, PerTurnCalls *int64
}
type Reservation struct {
	ID, RequestID, ProviderID, ModelID, Operation, Lane, TurnID string
	Attempt                                                     int
	Estimate                                                    Estimate
	Prices                                                      map[string]float64
}

// Ledger owns durable reservation transitions, not optional observability.
type Ledger interface {
	Reserve(context.Context, Reservation, BudgetPolicy) error
	Start(context.Context, string) (bool, error)
	Settle(context.Context, string, map[string]any) error
	Unknown(context.Context, string) error
	Release(context.Context, string) error
}
type Estimator func(Descriptor, map[string]any, int64, ProviderContract) (Estimate, error)
type NativeGate struct {
	Ledger          Ledger
	Policy          func(context.Context) (BudgetPolicy, error)
	Mode            string
	MaxOutputTokens int64
	Estimator       Estimator
	Contract        func(Descriptor) *ProviderContract
}
type requestScope struct {
	mu      sync.Mutex
	id      string
	attempt int
}
type scopeKey struct{}
type laneKey struct{}
type laneContext struct{ turn, lane string }

func newID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func WithBudgetRequest(ctx context.Context) (context.Context, error) {
	id, e := newID()
	if e != nil {
		return nil, e
	}
	return context.WithValue(ctx, scopeKey{}, &requestScope{id: id}), nil
}
func laneRank(lane string) int {
	switch lane {
	case "foreground":
		return 0
	case "realtime":
		return 1
	default:
		return 2
	}
}
func WithBudgetContext(ctx context.Context, turn, lane string) context.Context {
	if outer, ok := ctx.Value(laneKey{}).(laneContext); ok && laneRank(outer.lane) > laneRank(lane) {
		lane = outer.lane
	}
	return context.WithValue(ctx, laneKey{}, laneContext{turn, lane})
}
func uniqueSorted(v []string) []string {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	r := make([]string, 0, len(m))
	for s := range m {
		r = append(r, s)
	}
	sort.Strings(r)
	return r
}
func (g *NativeGate) Prepare(ctx context.Context, d Descriptor, body map[string]any) (BudgetAttempt, error) {
	a, e := g.prepare(ctx, d, body)
	if e != nil {
		return nil, budgetFailure(e)
	}
	return a, nil
}
func (g *NativeGate) prepare(ctx context.Context, d Descriptor, body map[string]any) (BudgetAttempt, error) {
	if g.Ledger == nil || g.Policy == nil {
		return nil, errors.New("budget_owner_required")
	}
	if g.MaxOutputTokens < 1 || g.MaxOutputTokens > 9007199254740991 {
		return nil, errors.New("budget_output_cap_required")
	}
	if d.Protocol != "openai-chat" && d.Protocol != "openai-responses" {
		return nil, errors.New("budget_protocol_unsupported")
	}
	if d.OpaqueInput {
		return nil, errors.New("budget_opaque_input_unsupported")
	}
	var contract *ProviderContract
	if g.Contract != nil {
		contract = g.Contract(d)
	}
	cap := g.MaxOutputTokens
	if d.Operation == "embedding" {
		cap = 0
	}
	if g.Mode == "strict" && (contract == nil || g.Estimator == nil) {
		return nil, errors.New("budget_verified_estimator_required")
	}
	if g.Mode == "strict" && cap > 0 && !contract.OutputCapHonored {
		return nil, errors.New("budget_output_cap_unverified")
	}
	limited := map[string]any{}
	for k, v := range body {
		limited[k] = v
	}
	if cap > 0 {
		key := "max_completion_tokens"
		if d.Protocol == "openai-responses" {
			key = "max_output_tokens"
		}
		limited[key] = float64(cap)
	}
	var estimate Estimate
	if g.Mode == "strict" {
		var e error
		estimate, e = g.Estimator(d, limited, cap, *contract)
		if e != nil {
			return nil, e
		}
		if !estimate.Enforceable || estimate.Source == "conservative_estimate" || estimate.OutputUpperTokens != cap || estimate.ContractVersion != contract.Version || !reflect.DeepEqual(uniqueSorted(estimate.InputCategories), uniqueSorted(contract.InputCategories)) {
			return nil, errors.New("budget_estimate_contract_mismatch")
		}
	} else {
		encoded, e := providerwire.EncodeJSON(limited)
		if e != nil {
			return nil, e
		}
		categories := []string{"inputUsdPerMillion", "cacheReadUsdPerMillion", "cacheWriteUsdPerMillion", "cacheWrite30mUsdPerMillion", "cacheWrite12hUsdPerMillion"}
		version := "unverified-native-text-v1"
		if contract != nil {
			version = contract.Version
			categories = append([]string(nil), contract.InputCategories...)
		}
		if d.Operation == "embedding" {
			categories = []string{"inputUsdPerMillion"}
		}
		estimate = Estimate{"conservative_estimate", "utf8-plus-1024-v1", version, false, int64(len(encoded)) + 1024, cap, categories}
	}
	scope, _ := ctx.Value(scopeKey{}).(*requestScope)
	if scope == nil {
		id, e := newID()
		if e != nil {
			return nil, e
		}
		scope = &requestScope{id: id}
	}
	scope.mu.Lock()
	scope.attempt++
	number := scope.attempt
	scope.mu.Unlock()
	basePriority := float64(1)
	if d.Role == "planner" || d.Role == "replyer" || d.Role == "vision" {
		basePriority = 0
	}
	active, hasActive := ctx.Value(laneKey{}).(laneContext)
	if hasActive {
		basePriority = float64(laneRank(active.lane))
	}
	priority := scheduler.EffectiveModelPriority(ctx, basePriority)
	lane := "maintenance"
	if priority == 0 {
		lane = "foreground"
	} else if priority == 1 {
		lane = "realtime"
	}
	turn := scope.id
	if hasActive {
		turn = active.turn
		if laneRank(active.lane) > laneRank(lane) {
			lane = active.lane
		}
	}

	id, e := newID()
	if e != nil {
		return nil, e
	}
	prices := map[string]float64{}
	for _, k := range []string{"inputUsdPerMillion", "outputUsdPerMillion", "cacheReadUsdPerMillion", "cacheWriteUsdPerMillion", "cacheWrite5mUsdPerMillion", "cacheWrite1hUsdPerMillion", "cacheWrite30mUsdPerMillion", "cacheWrite12hUsdPerMillion"} {
		if v, ok := d.Prices[k]; ok {
			prices[k] = v
		}
	}
	policy, e := g.Policy(ctx)
	if e != nil {
		return nil, e
	}
	policy.Mode = g.Mode
	reservation := Reservation{id, scope.id, d.ProviderID, d.ModelID, d.Operation, lane, turn, number, estimate, prices}
	if e = g.Ledger.Reserve(ctx, reservation, policy); e != nil {
		return nil, e
	}
	return &nativeAttempt{ledger: g.Ledger, id: id, body: limited}, nil
}

type nativeAttempt struct {
	mu               sync.Mutex
	ledger           Ledger
	id               string
	body             map[string]any
	started, settled bool
}

func (a *nativeAttempt) Body() map[string]any { return a.body }
func (a *nativeAttempt) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ok, e := a.ledger.Start(ctx, a.id)
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("budget_attempt_already_started")
	}
	a.started = true
	return nil
}
func (a *nativeAttempt) Settle(ctx context.Context, usage map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.ledger.Settle(ctx, a.id, usage); e != nil {
		return e
	}
	a.settled = true
	return nil
}
func (a *nativeAttempt) Fail(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled {
		return nil
	}
	var e error
	if a.started {
		e = a.ledger.Unknown(ctx, a.id)
	} else {
		e = a.ledger.Release(ctx, a.id)
	}
	if e == nil {
		a.settled = true
	}
	return e
}
