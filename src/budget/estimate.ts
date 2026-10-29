import type { ModelCostPrices } from '../model-cost.ts'
import type { BudgetEstimate } from './contracts.ts'

export const priceKeys = [
  'inputUsdPerMillion',
  'outputUsdPerMillion',
  'cacheReadUsdPerMillion',
  'cacheWriteUsdPerMillion',
  'cacheWrite5mUsdPerMillion',
  'cacheWrite1hUsdPerMillion',
  'cacheWrite30mUsdPerMillion',
  'cacheWrite12hUsdPerMillion',
] as const
export function nonnegativeInteger(value: unknown): asserts value is number {
  if (!Number.isSafeInteger(value) || (value as number) < 0)
    throw new Error('invalid_budget_integer')
}
export function safeNumber(value: bigint): number {
  if (value < 0n || value > BigInt(Number.MAX_SAFE_INTEGER))
    throw new Error('budget_amount_overflow')
  return Number(value)
}
/** Canonical decimal number → exact ratio; no repeated binary floating-point ledger addition. */
function decimal(value: number): [bigint, bigint] {
  if (!Number.isFinite(value) || value < 0) throw new Error('invalid_budget_price')
  const [mantissa, exponent = '0'] = value.toString().toLowerCase().split('e')
  const [whole, fraction = ''] = mantissa!.split('.')
  const digits = BigInt(whole! + fraction)
  const power = Number(exponent) - fraction.length
  return power >= 0 ? [digits * 10n ** BigInt(power), 1n] : [digits, 10n ** BigInt(-power)]
}
export function roundedNanoUsd(usd: number): number {
  const [numerator, denominator] = decimal(usd)
  return safeNumber((numerator * 1_000_000_000n + denominator - 1n) / denominator)
}
function tokenCost(tokens: number, rate: number): bigint {
  const [numerator, denominator] = decimal(rate)
  // USD / million tokens → nano-USD / token.
  return (BigInt(tokens) * numerator * 1000n + denominator - 1n) / denominator
}
export function quoteEstimate(
  estimate: BudgetEstimate,
  prices: ModelCostPrices,
  mode: 'strict' | 'best_effort' = 'strict',
): number {
  nonnegativeInteger(estimate.inputUpperTokens)
  nonnegativeInteger(estimate.outputUpperTokens)
  if (!(
    (estimate.enforceable === true &&
      ['verified_contract', 'verified_tokenizer'].includes(estimate.source)) ||
    (mode === 'best_effort' &&
      estimate.enforceable === false &&
      estimate.source === 'conservative_estimate')
  ))
    throw new Error('budget_estimate_unknown')
  if (!estimate.estimatorVersion || !estimate.contractVersion)
    throw new Error('budget_estimate_unknown')
  let input = 0n
  if (estimate.inputUpperTokens > 0) {
    if (!estimate.inputCategories.length) throw new Error('budget_price_unknown')
    for (const category of estimate.inputCategories) {
      if (!priceKeys.includes(category) || category === ('outputUsdPerMillion' as string))
        throw new Error('invalid_budget_category')
      const rate = prices[category]
      if (rate === undefined) throw new Error('budget_price_unknown')
      const cost = tokenCost(estimate.inputUpperTokens, rate)
      if (cost > input) input = cost
    }
  }
  const output =
    estimate.outputUpperTokens > 0
      ? prices.outputUsdPerMillion === undefined
        ? (() => {
            throw new Error('budget_price_unknown')
          })()
        : tokenCost(estimate.outputUpperTokens, prices.outputUsdPerMillion)
      : 0n
  return safeNumber(input + output)
}

/** Round once after exact decimal summation of known disjoint buckets. */
export function exactTokenSubtotalNanoUsd(
  components: ReadonlyArray<readonly [number, number | undefined]>,
): number {
  let total = 0n,
    denominator = 1n
  for (const [tokens, rate] of components) {
    nonnegativeInteger(tokens)
    if (tokens === 0) continue
    if (rate === undefined) throw new Error('budget_price_unknown')
    const [numerator, divisor] = decimal(rate)
    const common = denominator > divisor ? denominator : divisor
    total = total * (common / denominator) + BigInt(tokens) * numerator * 1000n * (common / divisor)
    denominator = common
  }
  return safeNumber((total + denominator - 1n) / denominator)
}
