package usageview

import (
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

const maxSafeCount = 9007199254740991

func validCount(n *float64) bool {
	return n != nil && *n >= 0 && *n <= maxSafeCount && math.Trunc(*n) == *n
}

func count(n *float64) float64 {
	if n != nil {
		return *n
	}
	return 0
}

type usageCounts struct {
	input, output, ordinary, read, write float64
	ttl                                  [4]float64
	ttlTotal                             float64
	reported, partition, unknown         bool
	readKnown, writeKnown                bool
}

func countUsage(u *modelservice.AttemptUsage) (c usageCounts) {
	if u == nil {
		c.unknown = true
		return
	}
	input, output := validCount(u.InputTokens), validCount(u.OutputTokens)
	if input {
		c.input = *u.InputTokens
	}
	if output {
		c.output = *u.OutputTokens
	}
	c.unknown = !u.Known || !input || !output
	if c.unknown {
		return
	}
	for _, n := range []*float64{u.OrdinaryInputTokens, u.CacheReadInputTokens, u.CacheWriteInputTokens} {
		if n != nil && !validCount(n) {
			return
		}
	}
	read, write := count(u.CacheReadInputTokens), count(u.CacheWriteInputTokens)
	if read+write > c.input || count(u.OrdinaryInputTokens)+read+write > c.input {
		return
	}
	var ttl [4]float64
	for i, n := range u.CacheWriteTTL {
		if n != nil && !validCount(n) {
			return
		}
		ttl[i] = count(n)
	}
	total := ttl[0] + ttl[1] + ttl[2] + ttl[3]
	ttlLimit := c.input
	if u.CacheWriteInputTokens != nil {
		ttlLimit = write
	}
	if total > ttlLimit {
		return
	}
	if u.CacheKnown {
		ordinary := c.input - read - write
		if u.CacheReadInputTokens == nil || u.CacheWriteInputTokens == nil ||
			u.OrdinaryInputTokens != nil && *u.OrdinaryInputTokens != ordinary {
			return
		}
		c.partition, c.ordinary = true, ordinary
	}
	c.reported, c.readKnown, c.writeKnown = true, u.CacheReadInputTokens != nil, u.CacheWriteInputTokens != nil
	c.read, c.write, c.ttlTotal = read, write, total
	c.ttl = ttl
	return
}

type usageCost struct {
	input, output, read, write, sum float64
	full, partial                   bool
}

func priceUsage(c usageCounts, prices map[string]float64) (cost usageCost) {
	if !c.reported {
		return
	}
	component := func(tokens float64, key string) (float64, bool) {
		if tokens == 0 {
			return 0, true
		}
		p, ok := prices[key]
		usd := tokens * p / 1_000_000
		if !ok || p < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
			return 0, false
		}
		return usd, true
	}
	var input, output, read, write bool
	if c.partition {
		cost.input, input = component(c.ordinary, "inputUsdPerMillion")
	}
	cost.output, output = component(c.output, "outputUsdPerMillion")
	if c.readKnown {
		cost.read, read = component(c.read, "cacheReadUsdPerMillion")
	}
	if c.writeKnown {
		var remainder float64
		remainder, write = component(c.write-c.ttlTotal, "cacheWriteUsdPerMillion")
		for i, key := range [...]string{"cacheWrite5mUsdPerMillion", "cacheWrite1hUsdPerMillion", "cacheWrite30mUsdPerMillion", "cacheWrite12hUsdPerMillion"} {
			usd, known := component(c.ttl[i], key)
			remainder += usd
			write = write && known
		}
		if write {
			cost.write = remainder
		}
	}
	cost.full = c.partition && input && output && read && write
	cost.partial = !cost.full && (input && c.ordinary > 0 || output && c.output > 0 || read && c.read > 0 || write && c.write > 0)
	cost.sum = cost.input + cost.output + cost.read + cost.write
	if (!cost.full && !cost.partial) || math.IsInf(cost.sum, 0) || math.IsNaN(cost.sum) {
		return usageCost{}
	}
	return
}

func counter(b bool) int {
	if b {
		return 1
	}
	return 0
}

func usageArguments(day string, e modelservice.AttemptObservation, prices map[string]float64) []any {
	c := countUsage(e.Usage)
	var cost usageCost
	if e.Completed {
		cost = priceUsage(c, prices)
	}
	var reportedInput, reportedOutput, rateInput float64
	if c.reported {
		reportedInput, reportedOutput = c.input, c.output
	}
	if c.readKnown {
		rateInput = c.input
	}
	var ttl [4]float64
	if c.partition {
		ttl = c.ttl
	}
	return []any{
		day, e.ModelID, e.ProviderID, e.APIModelName, e.Role,
		1, c.input, c.output, float64(e.Duration) / 1e6, counter(cost.full), cost.sum,
		reportedInput, reportedOutput, counter(c.reported), rateInput, c.read, counter(c.readKnown),
		c.ordinary, c.read, c.write, ttl[0], ttl[1], ttl[2], ttl[3],
		counter(c.partition), counter(c.readKnown), counter(c.writeKnown), counter(cost.partial),
		0, counter(c.unknown), cost.input, cost.output, cost.read, cost.write, 0,
	}
}
