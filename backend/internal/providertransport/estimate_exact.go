package providertransport

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"math/big"
)

const maxBudgetInteger int64 = 9007199254740991

var budgetPriceKeys = []string{"inputUsdPerMillion", "outputUsdPerMillion", "cacheReadUsdPerMillion", "cacheWriteUsdPerMillion", "cacheWrite5mUsdPerMillion", "cacheWrite1hUsdPerMillion", "cacheWrite30mUsdPerMillion", "cacheWrite12hUsdPerMillion"}
var budgetTokenKeys = []string{"inputTokens", "outputTokens", "ordinaryInputTokens", "cacheReadInputTokens", "cacheWriteInputTokens", "cacheWrite5mInputTokens", "cacheWrite1hInputTokens", "cacheWrite30mInputTokens", "cacheWrite12hInputTokens"}

func budgetInteger(n int64) error {
	if n < 0 || n > maxBudgetInteger {
		return errors.New("invalid_budget_integer")
	}
	return nil
}
func budgetSum(ns ...int64) (int64, error) {
	var n int64
	for _, v := range ns {
		if v < 0 || v > maxBudgetInteger-n {
			return 0, errors.New("budget_amount_overflow")
		}
		n += v
	}
	return n, nil
}
func budgetRate(v float64) (*big.Rat, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil, errors.New("invalid_budget_price")
	}
	s, e := jsjson.Stringify(v)
	if e != nil {
		return nil, e
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, errors.New("invalid_budget_price")
	}
	return r, nil
}
func budgetCeil(r *big.Rat) (int64, error) {
	n := new(big.Int).Add(r.Num(), new(big.Int).Sub(r.Denom(), big.NewInt(1)))
	n.Quo(n, r.Denom())
	if n.Sign() < 0 || !n.IsInt64() || n.Int64() > maxBudgetInteger {
		return 0, errors.New("budget_amount_overflow")
	}
	return n.Int64(), nil
}

type budgetBucket struct {
	tokens int64
	rate   float64
}

func exactBudgetSubtotal(bs []budgetBucket) (int64, error) {
	total := new(big.Rat)
	for _, b := range bs {
		if e := budgetInteger(b.tokens); e != nil {
			return 0, e
		}
		if b.tokens == 0 {
			continue
		}
		r, e := budgetRate(b.rate)
		if e != nil {
			return 0, e
		}
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Mul(big.NewInt(b.tokens), big.NewInt(1000))))
		total.Add(total, r)
	}
	return budgetCeil(total)
}

// QuoteEstimate rounds independent input and output upper bounds upward.
func QuoteEstimate(e Estimate, p map[string]float64, mode string) (int64, error) {
	if err := budgetInteger(e.InputUpperTokens); err != nil {
		return 0, err
	}
	if err := budgetInteger(e.OutputUpperTokens); err != nil {
		return 0, err
	}
	if !(e.Enforceable && (e.Source == "verified_contract" || e.Source == "verified_tokenizer") || mode == "best_effort" && !e.Enforceable && e.Source == "conservative_estimate") || e.EstimatorVersion == "" || e.ContractVersion == "" {
		return 0, errors.New("budget_estimate_unknown")
	}
	var input, output int64
	if e.InputUpperTokens > 0 {
		if len(e.InputCategories) == 0 {
			return 0, errors.New("budget_price_unknown")
		}
		for _, k := range e.InputCategories {
			valid := false
			for _, key := range budgetPriceKeys {
				if k == key && k != "outputUsdPerMillion" {
					valid = true
				}
			}
			if !valid {
				return 0, errors.New("invalid_budget_category")
			}
			rate, ok := p[k]
			if !ok {
				return 0, errors.New("budget_price_unknown")
			}
			v, err := exactBudgetSubtotal([]budgetBucket{{e.InputUpperTokens, rate}})
			if err != nil {
				return 0, err
			}
			if v > input {
				input = v
			}
		}
	}
	if e.OutputUpperTokens > 0 {
		rate, ok := p["outputUsdPerMillion"]
		if !ok {
			return 0, errors.New("budget_price_unknown")
		}
		var err error
		output, err = exactBudgetSubtotal([]budgetBucket{{e.OutputUpperTokens, rate}})
		if err != nil {
			return 0, err
		}
	}
	return budgetSum(input, output)
}
func budgetCount(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= float64(maxBudgetInteger) && math.Trunc(x) == x {
			return int64(x), true
		}
	case int64:
		return x, budgetInteger(x) == nil
	case int:
		return int64(x), budgetInteger(int64(x)) == nil
	}
	return 0, false
}
func budgetUsage(input map[string]any) (map[string]any, string) {
	bad := func() (map[string]any, string) {
		return map[string]any{"inputTokens": float64(0), "outputTokens": float64(0), "known": false}, `{"inputTokens":0,"outputTokens":0,"known":false}`
	}
	u := map[string]any{}
	for _, k := range []string{"known", "cacheKnown"} {
		if v, ok := input[k]; ok {
			if _, ok := v.(bool); !ok {
				return bad()
			}
			u[k] = v
		}
	}
	for _, k := range budgetTokenKeys {
		v, ok := input[k]
		if !ok {
			if k == "inputTokens" || k == "outputTokens" {
				return bad()
			}
			continue
		}
		n, valid := budgetCount(v)
		if !valid {
			return bad()
		}
		u[k] = float64(n)
	}
	if !validBudgetUsage(u) {
		return bad()
	}
	o := new(jsjson.Object)
	for _, k := range append(append([]string{}, budgetTokenKeys...), "known", "cacheKnown") {
		if v, ok := u[k]; ok {
			_ = o.Set(k, v)
		}
	}
	s, _ := jsjson.Stringify(o)
	return u, s
}
func usageN(u map[string]any, k string) int64 { n, _ := budgetCount(u[k]); return n }
func budgetPartition(u map[string]any) (int64, bool) {
	if u["known"] == false || u["cacheKnown"] != true {
		return 0, false
	}
	for _, k := range []string{"inputTokens", "outputTokens", "cacheReadInputTokens", "cacheWriteInputTokens"} {
		if _, ok := budgetCount(u[k]); !ok {
			return 0, false
		}
	}
	ttl := int64(0)
	for _, k := range budgetTokenKeys[5:] {
		ttl += usageN(u, k)
	}
	ordinary := usageN(u, "inputTokens") - usageN(u, "cacheReadInputTokens") - usageN(u, "cacheWriteInputTokens")
	if ttl > maxBudgetInteger || ttl > usageN(u, "cacheWriteInputTokens") || ordinary < 0 {
		return 0, false
	}
	if v, ok := u["ordinaryInputTokens"]; ok {
		n, valid := budgetCount(v)
		if !valid || n != ordinary {
			return 0, false
		}
	}
	return ordinary, true
}
func validBudgetUsage(u map[string]any) bool {
	if u["known"] == false {
		return false
	}
	for _, k := range budgetTokenKeys {
		v, ok := u[k]
		if !ok {
			if k == "inputTokens" || k == "outputTokens" {
				return false
			}
			continue
		}
		if _, valid := budgetCount(v); !valid {
			return false
		}
	}
	read, write, input := usageN(u, "cacheReadInputTokens"), usageN(u, "cacheWriteInputTokens"), usageN(u, "inputTokens")
	if read+write > input || usageN(u, "ordinaryInputTokens")+read+write > input {
		return false
	}
	ttl := int64(0)
	for _, k := range budgetTokenKeys[5:] {
		ttl += usageN(u, k)
	}
	limit := input
	if _, ok := u["cacheWriteInputTokens"]; ok {
		limit = write
	}
	if ttl > maxBudgetInteger || ttl > limit {
		return false
	}
	if u["cacheKnown"] == true {
		_, ok := budgetPartition(u)
		return ok
	}
	return true
}

// Preserve model-cost's useful-component and all-or-nothing cache write rules.
func budgetActual(u map[string]any, p map[string]float64) (known int64, complete, useful bool, err error) {
	if !validBudgetUsage(u) {
		return
	}
	ordinary, partition := budgetPartition(u)
	buckets := []budgetBucket{}
	component := func(n int64, key string) (budgetBucket, bool) {
		r, ok := p[key]
		if n == 0 {
			return budgetBucket{0, 0}, true
		}
		return budgetBucket{n, r}, ok && r >= 0 && !math.IsNaN(r) && !math.IsInf(r, 0) && !math.IsInf(float64(n)*r/1e6, 0)
	}
	add := func(b budgetBucket, ok bool) {
		if ok {
			buckets = append(buckets, b)
			if b.tokens > 0 {
				useful = true
			}
		}
	}
	a, oa := component(ordinary, "inputUsdPerMillion")
	oa = oa && partition
	add(a, oa)
	b, ob := component(usageN(u, "outputTokens"), "outputUsdPerMillion")
	add(b, ob)
	c, oc := component(usageN(u, "cacheReadInputTokens"), "cacheReadUsdPerMillion")
	_, present := u["cacheReadInputTokens"]
	oc = oc && present
	add(c, oc)
	ttl := int64(0)
	for _, k := range budgetTokenKeys[5:] {
		ttl += usageN(u, k)
	}
	w := []budgetBucket{}
	_, ow := u["cacheWriteInputTokens"]
	d, od := component(usageN(u, "cacheWriteInputTokens")-ttl, "cacheWriteUsdPerMillion")
	ow = ow && od
	w = append(w, d)
	for i, k := range budgetTokenKeys[5:] {
		v, ok := component(usageN(u, k), budgetPriceKeys[4+i])
		ow = ow && ok
		w = append(w, v)
	}
	if ow {
		for _, v := range w {
			add(v, true)
		}
	}
	complete = partition && oa && ob && oc && ow
	useful = useful || complete
	if !useful {
		return 0, false, false, nil
	}
	var total float64
	for _, v := range buckets {
		total += float64(v.tokens) * v.rate / 1e6
	}
	if math.IsInf(total, 0) || math.IsNaN(total) {
		return 0, false, false, nil
	}
	known, err = exactBudgetSubtotal(buckets)
	return
}
