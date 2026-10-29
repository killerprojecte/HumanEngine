import { AsyncLocalStorage } from 'node:async_hooks'
import type { TokenizerTarget, RequestMeasurement, RequestMeasurementSummary } from './contracts.ts'
import type { TargetTokenizerRegistry } from './registry.ts'
const qualification = {
  providerTokenCount: 'unknown',
  providerFraming: 'uncalibrated',
  hardTokenCap: false,
} as const
/** Caller sends this exact already-serialized string; no body is retained by this module. */
export function measureSerializedRequest(
  serialized: string,
  target: TokenizerTarget,
  registry?: TargetTokenizerRegistry,
): RequestMeasurement {
  const base = { ...qualification, bodyBytes: Buffer.byteLength(serialized, 'utf8') }
  const tokenizer = registry?.lookup(target)
  if (!tokenizer) return { ...base, reason: 'unregistered_tokenizer' }
  try {
    const count = tokenizer.count(serialized)
    if (!Number.isSafeInteger(count) || count < 0) return { ...base, reason: 'invalid_counter' }
    return {
      ...base,
      textTokens: count,
      tokenizerContract: `${tokenizer.id}@${tokenizer.revision}`,
      reason: 'registered_text_counter',
    }
  } catch {
    return { ...base, reason: 'counter_failed' }
  }
}
const scope = new AsyncLocalStorage<Map<string, RequestMeasurementSummary>>()
const key = (providerId: string, modelId: string) => JSON.stringify([providerId, modelId])
export const withRequestMeasurements = <T>(run: () => T): T => scope.run(new Map(), run)
/** No external callbacks here: safe between final budget start guard and native fetch. */
export function recordRequestMeasurement(
  providerId: string,
  modelId: string,
  measurement: RequestMeasurement,
): void {
  const rows = scope.getStore()
  if (!rows) return
  const k = key(providerId, modelId)
  const row: RequestMeasurementSummary = rows.get(k) ?? {
    ...qualification,
    attempts: 0,
    measuredBodyAttempts: 0,
    bodyUtf8Bytes: 0,
    bytesCoverage: 'unknown' as const,
    textCountedAttempts: 0,
    serializedTextTokens: 0,
    textTokenCoverage: 'unknown' as const,
    tokenizerContracts: [],
    contractCoverage: 'complete' as const,
  }
  row.attempts++
  if (measurement.bodyBytes !== undefined) {
    row.measuredBodyAttempts++
    row.bodyUtf8Bytes =
      row.bodyUtf8Bytes !== null && Number.isSafeInteger(row.bodyUtf8Bytes + measurement.bodyBytes)
        ? row.bodyUtf8Bytes + measurement.bodyBytes
        : null
  }
  if (measurement.textTokens !== undefined) {
    row.textCountedAttempts++
    row.serializedTextTokens =
      row.serializedTextTokens !== null &&
      Number.isSafeInteger(row.serializedTextTokens + measurement.textTokens)
        ? row.serializedTextTokens + measurement.textTokens
        : null
  }
  if (
    measurement.tokenizerContract &&
    !row.tokenizerContracts.includes(measurement.tokenizerContract)
  ) {
    if (row.tokenizerContracts.length < 16)
      row.tokenizerContracts.push(measurement.tokenizerContract)
    else row.contractCoverage = 'limited'
  }
  row.bytesCoverage =
    row.bodyUtf8Bytes === null
      ? 'unknown'
      : row.measuredBodyAttempts === row.attempts
        ? 'complete'
        : row.measuredBodyAttempts
          ? 'partial'
          : 'unknown'
  row.textTokenCoverage =
    row.serializedTextTokens === null
      ? 'unknown'
      : row.textCountedAttempts === row.attempts
        ? 'complete'
        : row.textCountedAttempts
          ? 'partial'
          : 'unknown'
  rows.set(k, row)
}
export function unknownCustomMeasurement(): RequestMeasurement {
  return { ...qualification, reason: 'custom_wire_unknown' }
}
export function requestMeasurementSummary(
  providerId: string,
  modelId: string,
): RequestMeasurementSummary | undefined {
  const value = scope.getStore()?.get(key(providerId, modelId))
  return value ? structuredClone(value) : undefined
}

/** Each model log owns only attempts since its preceding log (embedding batches must not double-count). */
export function takeRequestMeasurementSummary(
  providerId: string,
  modelId: string,
): RequestMeasurementSummary | undefined {
  const value = requestMeasurementSummary(providerId, modelId)
  scope.getStore()?.delete(key(providerId, modelId))
  return value
}
