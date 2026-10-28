/** Prices are USD per million tokens, snapshotted at request start by the router. */
export interface ModelCostPrices {
  inputUsdPerMillion?: number
  outputUsdPerMillion?: number
  cacheReadUsdPerMillion?: number
  cacheWriteUsdPerMillion?: number
  cacheWrite5mUsdPerMillion?: number
  cacheWrite1hUsdPerMillion?: number
  cacheWrite30mUsdPerMillion?: number
  cacheWrite12hUsdPerMillion?: number
}
export interface CostUsage {
  known?: boolean
  cacheKnown?: boolean
  inputTokens: number
  outputTokens: number
  ordinaryInputTokens?: number
  cacheReadInputTokens?: number
  cacheWriteInputTokens?: number
  cacheWrite5mInputTokens?: number
  cacheWrite1hInputTokens?: number
  cacheWrite30mInputTokens?: number
  cacheWrite12hInputTokens?: number
}
export const validTokenCount = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0

/** Cache-write TTL counters are disjoint subsets of aggregate writes, never extra input. */
export function cachePartition(usage: CostUsage) {
  if (
    usage.known === false ||
    usage.cacheKnown !== true ||
    !validTokenCount(usage.inputTokens) ||
    !validTokenCount(usage.outputTokens) ||
    !validTokenCount(usage.cacheReadInputTokens) ||
    !validTokenCount(usage.cacheWriteInputTokens)
  )
    return undefined
  const ttl = [
    usage.cacheWrite5mInputTokens ?? 0,
    usage.cacheWrite1hInputTokens ?? 0,
    usage.cacheWrite30mInputTokens ?? 0,
    usage.cacheWrite12hInputTokens ?? 0,
  ]
  if (!ttl.every(validTokenCount)) return undefined
  const ttlTotal = ttl.reduce((sum, count) => sum + count, 0)
  const ordinary = usage.inputTokens - usage.cacheReadInputTokens - usage.cacheWriteInputTokens
  if (
    !Number.isSafeInteger(ttlTotal) ||
    ttlTotal > usage.cacheWriteInputTokens ||
    !validTokenCount(ordinary) ||
    (usage.ordinaryInputTokens !== undefined && usage.ordinaryInputTokens !== ordinary)
  )
    return undefined
  return {
    ordinaryInputTokens: ordinary,
    cacheReadInputTokens: usage.cacheReadInputTokens,
    cacheWriteInputTokens: usage.cacheWriteInputTokens,
    cacheWrite5mInputTokens: ttl[0]!,
    cacheWrite1hInputTokens: ttl[1]!,
    cacheWrite30mInputTokens: ttl[2]!,
    cacheWrite12hInputTokens: ttl[3]!,
    unclassifiedWriteTokens: usage.cacheWriteInputTokens - ttlTotal,
  }
}
const component = (count: number, price: number | undefined): number | undefined => {
  if (count === 0) return 0
  if (price === undefined || !Number.isFinite(price) || price < 0) return undefined
  const cost = (count * price) / 1_000_000
  return Number.isFinite(cost) ? cost : undefined
}
/** Reject contradictory counters as a unit; missing counters remain unknown. */
export function validCostUsage(usage: CostUsage): boolean {
  if (
    usage.known === false ||
    !validTokenCount(usage.inputTokens) ||
    !validTokenCount(usage.outputTokens)
  )
    return false
  const supplied = [
    usage.ordinaryInputTokens,
    usage.cacheReadInputTokens,
    usage.cacheWriteInputTokens,
    usage.cacheWrite5mInputTokens,
    usage.cacheWrite1hInputTokens,
    usage.cacheWrite30mInputTokens,
    usage.cacheWrite12hInputTokens,
  ]
  if (supplied.some((value) => value !== undefined && !validTokenCount(value))) return false
  const read = usage.cacheReadInputTokens,
    write = usage.cacheWriteInputTokens
  if (
    (read ?? 0) + (write ?? 0) > usage.inputTokens ||
    (usage.ordinaryInputTokens ?? 0) + (read ?? 0) + (write ?? 0) > usage.inputTokens
  )
    return false
  const ttl =
    (usage.cacheWrite5mInputTokens ?? 0) +
    (usage.cacheWrite1hInputTokens ?? 0) +
    (usage.cacheWrite30mInputTokens ?? 0) +
    (usage.cacheWrite12hInputTokens ?? 0)
  if (!Number.isSafeInteger(ttl) || ttl > (write ?? usage.inputTokens)) return false
  if (usage.cacheKnown === true && !cachePartition(usage)) return false
  return true
}
/** Known component subtotal, never an invented price for missing cache metadata. */
export function calculateModelCost(usage: CostUsage, prices: ModelCostPrices) {
  if (!validCostUsage(usage)) return undefined
  const partition = cachePartition(usage)
  const ordinaryInputUsd = partition
    ? component(partition.ordinaryInputTokens, prices.inputUsdPerMillion)
    : undefined
  const outputUsd = component(usage.outputTokens, prices.outputUsdPerMillion)
  const cacheReadUsd =
    usage.cacheReadInputTokens !== undefined
      ? component(usage.cacheReadInputTokens, prices.cacheReadUsdPerMillion)
      : undefined
  const ttl = [
    usage.cacheWrite5mInputTokens ?? 0,
    usage.cacheWrite1hInputTokens ?? 0,
    usage.cacheWrite30mInputTokens ?? 0,
    usage.cacheWrite12hInputTokens ?? 0,
  ]
  const writes =
    usage.cacheWriteInputTokens !== undefined
      ? [
          component(
            usage.cacheWriteInputTokens - ttl.reduce((sum, count) => sum + count, 0),
            prices.cacheWriteUsdPerMillion,
          ),
          component(ttl[0]!, prices.cacheWrite5mUsdPerMillion),
          component(ttl[1]!, prices.cacheWrite1hUsdPerMillion),
          component(ttl[2]!, prices.cacheWrite30mUsdPerMillion),
          component(ttl[3]!, prices.cacheWrite12hUsdPerMillion),
        ]
      : undefined
  const cacheWriteUsd =
    writes && writes.every((value) => value !== undefined)
      ? (writes as number[]).reduce((sum, value) => sum + value, 0)
      : undefined
  const fullyPriced =
    !!partition &&
    [ordinaryInputUsd, outputUsd, cacheReadUsd, cacheWriteUsd].every((value) => value !== undefined)
  // Known zero-sized buckets alone cannot turn an otherwise unknown bill into a free call.
  const useful =
    fullyPriced ||
    (ordinaryInputUsd !== undefined && (partition?.ordinaryInputTokens ?? 0) > 0) ||
    (outputUsd !== undefined && usage.outputTokens > 0) ||
    (cacheReadUsd !== undefined && (usage.cacheReadInputTokens ?? 0) > 0) ||
    (cacheWriteUsd !== undefined && (usage.cacheWriteInputTokens ?? 0) > 0)
  if (!useful) return undefined
  const estimatedUsd =
    (ordinaryInputUsd ?? 0) + (outputUsd ?? 0) + (cacheReadUsd ?? 0) + (cacheWriteUsd ?? 0)
  if (!Number.isFinite(estimatedUsd)) return undefined
  return { ordinaryInputUsd, outputUsd, cacheReadUsd, cacheWriteUsd, estimatedUsd, fullyPriced }
}
