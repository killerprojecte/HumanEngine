package workingset

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"sync"
)

type Policy struct {
	ColdAfterMs        float64 `json:"coldAfterMs"`
	ReviewAfterMs      float64 `json:"reviewAfterMs"`
	LeaseMs            float64 `json:"leaseMs"`
	MaxBatch           float64 `json:"maxBatch"`
	MaxReceipts        float64 `json:"maxReceipts"`
	ReceiptRetentionMs float64 `json:"receiptRetentionMs"`
	SelectionMaxAgeMs  float64 `json:"selectionMaxAgeMs"`
}

func DefaultPolicy() Policy {
	return Policy{30 * 86400000, 14 * 86400000, 30000, 100, 10000, 86400000, 3600000}
}
func (p Policy) Validate() error {
	for _, n := range []float64{p.ColdAfterMs, p.ReviewAfterMs, p.LeaseMs, p.MaxBatch, p.MaxReceipts, p.ReceiptRetentionMs, p.SelectionMaxAgeMs} {
		if !safe(n) || n <= 0 {
			return errors.New("working_set_policy_invalid")
		}
	}
	if p.MaxBatch > 200 || p.MaxReceipts > 100000 || p.ReceiptRetentionMs <= p.SelectionMaxAgeMs {
		return errors.New("working_set_policy_invalid")
	}
	return nil
}
func EntityKey(r Reference) string { s, _ := jsjson.Stringify([]any{r.Kind, r.ID}); return s }
func ReferenceKey(r Reference) string {
	var mv any
	if r.Kind == "commitment" && r.MutationVersion != nil {
		mv = *r.MutationVersion
	}
	s, _ := jsjson.Stringify([]any{r.Kind, r.ID, r.Version, r.DeletionEpoch, mv})
	return s
}
func SelectionKey(s Selection) string {
	v, _ := jsjson.Stringify([]any{ReferenceKey(s.Ref), s.DependencyKey})
	return v
}
func SameSelection(a, b Selection) bool { return SelectionKey(a) == SelectionKey(b) }

type Classification struct {
	State     string `json:"state"`
	ReviewDue bool   `json:"reviewDue"`
}

func Classify(snapshot Snapshot, item *Item, now float64, policy Policy, pinned, unresolved bool) Classification {
	last := snapshot.RecordedAt
	if item != nil && item.LastSelectedAt != nil {
		last = item.LastSelectedAt
	}
	state := "unknown"
	if pinned || unresolved || snapshot.Protection != nil {
		state = "hot"
	} else if item != nil && last != nil {
		if math.Max(0, now-*last) < policy.ColdAfterMs {
			state = "hot"
		} else {
			state = "cold"
		}
	}
	review := snapshot.RecordedAt != nil && snapshot.EpistemicKind != nil && oneOf(*snapshot.EpistemicKind, "inferred", "hypothesis") && math.Max(0, now-*snapshot.RecordedAt) >= policy.ReviewAfterMs
	return Classification{state, review}
}

// Configuration publishes immutable policy values. No lock spans a resolver
// or observer callback, which may synchronously reconfigure the service.
type policyState struct {
	mu    sync.RWMutex
	value Policy
}

func (p *policyState) load() Policy { p.mu.RLock(); defer p.mu.RUnlock(); return p.value }
func (p *policyState) configure(cold, review *float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.value
	if cold != nil {
		next.ColdAfterMs = *cold
	}
	if review != nil {
		next.ReviewAfterMs = *review
	}
	if e := next.Validate(); e != nil {
		return e
	}
	p.value = next
	return nil
}
func (p *policyState) store(value Policy) { p.mu.Lock(); defer p.mu.Unlock(); p.value = value }
