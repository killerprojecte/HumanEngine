/** Private error/outcome metadata, never attached to serialized model or tool payloads. */
export interface ErrorContext {
  operation?: string
  stage?: string
  role?: string
  modelId?: string
  providerId?: string
  apiModel?: string
  stageBudgetMs?: number
  maxAttempts?: number
  attempt?: number
  timeoutMs?: number
  remainingCandidates?: number
  retryable?: boolean
}
const contexts = new WeakMap<object, ErrorContext>()
const outcomes = new WeakMap<object, unknown>()
export function safeErrorContext(value: ErrorContext): ErrorContext {
  const result: ErrorContext = {}
  for (const key of ['operation', 'stage', 'role', 'modelId', 'providerId', 'apiModel'] as const) {
    const field = value[key]
    if (
      typeof field === 'string' &&
      /^[A-Za-z0-9_.:@/ -]{1,128}$/.test(field) &&
      !field.includes('://')
    )
      result[key] = field
  }
  for (const key of [
    'attempt',
    'maxAttempts',
    'stageBudgetMs',
    'timeoutMs',
    'remainingCandidates',
  ] as const) {
    const field = value[key]
    if (typeof field === 'number' && Number.isFinite(field) && field >= 0) result[key] = field
  }
  if (typeof value.retryable === 'boolean') result.retryable = value.retryable
  return result
}
/** Inner, more specific metadata takes precedence over outer wrappers. */
export function preserveErrorContext<T>(error: T, value: ErrorContext): T {
  if (error && typeof error === 'object')
    contexts.set(error, { ...safeErrorContext(value), ...contexts.get(error) })
  return error
}
export function errorContext(error: object): ErrorContext | undefined {
  return contexts.get(error)
}
export function preserveOutcomeError<T extends object>(
  outcome: T,
  error: unknown,
  value: ErrorContext = {},
): T {
  outcomes.set(outcome, preserveErrorContext(error, value))
  return outcome
}
export function outcomeError(outcome: object): unknown {
  return outcomes.get(outcome)
}
export function failureFromOutcomes(values: object[]): unknown {
  const errors = values.map(outcomeError).filter((value) => value !== undefined)
  return errors.length
    ? new AggregateError(errors, 'operation_outcomes_failed', { cause: errors.at(-1) })
    : undefined
}

/** Capture only the currently selected attempt when a shared stage deadline fires. */
export function bindAbortErrorContext(
  signal: AbortSignal | undefined,
  value: ErrorContext,
): () => void {
  if (!signal) return () => {}
  const apply = () => {
    const reason: unknown = signal.reason
    if (reason && typeof reason === 'object')
      contexts.set(reason, { ...contexts.get(reason), ...safeErrorContext(value) })
  }
  if (signal.aborted) apply()
  else signal.addEventListener('abort', apply, { once: true })
  return () => signal.removeEventListener('abort', apply)
}
