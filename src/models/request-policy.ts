const handledFailures = new WeakSet<object>()
export function markProviderAttemptsHandled<T>(error: T): T {
  if (error && typeof error === 'object') handledFailures.add(error)
  return error
}
export function providerAttemptsHandled(error: unknown): boolean {
  return !!error && typeof error === 'object' && handledFailures.has(error)
}

export interface ProviderRequestPolicyInput {
  retryCount?: number
  timeoutMs?: number
}
export interface ProviderRequestPolicy {
  retryCount: number
  maxAttempts: number
  timeoutMs: number
}
export interface ProviderAttemptMetadata {
  /** One-based current attempt, including the initial request. */
  attempt: number
  maxAttempts: number
  timeoutMs: number
}
export interface ProviderRetryMetadata extends ProviderAttemptMetadata {
  /** Delay after this failed attempt, before the next attempt. */
  delayMs: number
}
export interface ProviderAttemptOptions {
  classify(error: unknown): { retryable: boolean; retryAfterMs?: number }
  onAttempt?(metadata: ProviderAttemptMetadata): void
  onRetry?(metadata: ProviderRetryMetadata): void
  sleep?(delayMs: number, signal?: AbortSignal): Promise<void>
}
const MAX_RETRY_DELAY_MS = 10000
const cancelled = () => new Error('request_cancelled')
const checkCancelled = (signal?: AbortSignal) => {
  if (signal?.aborted) throw cancelled()
}

export function providerPolicy(provider: ProviderRequestPolicyInput = {}): ProviderRequestPolicy {
  if (!provider || typeof provider !== 'object' || Array.isArray(provider))
    throw new Error('invalid_provider_request_policy')
  const retryCount = provider.retryCount === undefined ? 2 : provider.retryCount
  const timeoutMs = provider.timeoutMs === undefined ? 30000 : provider.timeoutMs
  if (
    !Number.isInteger(retryCount) ||
    retryCount < 0 ||
    retryCount > 5 ||
    !Number.isInteger(timeoutMs) ||
    timeoutMs < 100 ||
    timeoutMs > 300000
  )
    throw new Error('invalid_provider_request_policy')
  return { retryCount, maxAttempts: retryCount + 1, timeoutMs }
}

/** Parse a delta in seconds or an HTTP date; never wait more than ten seconds. */
export function parseRetryAfter(value: string | null, now = Date.now()): number | undefined {
  if (value === null || !value.trim()) return undefined
  const text = value.trim()
  const duration = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/.test(text)
    ? Number(text) * 1000
    : Date.parse(text) - now
  if (Number.isNaN(duration)) return undefined
  return Math.min(MAX_RETRY_DELAY_MS, Math.max(0, duration))
}

/** Attempt one waits 250ms, attempt two 500ms; Retry-After may extend the bounded delay. */
export function retryDelay(attempt: number, retryAfterMs?: number): number {
  const exponent = Number.isFinite(attempt) ? Math.min(6, Math.max(0, Math.floor(attempt) - 1)) : 6
  const base = Math.min(MAX_RETRY_DELAY_MS, 250 * 2 ** exponent)
  const requested =
    typeof retryAfterMs === 'number' && !Number.isNaN(retryAfterMs)
      ? Math.min(MAX_RETRY_DELAY_MS, Math.max(0, retryAfterMs))
      : 0
  return Math.max(base, requested)
}

export function waitRetry(delayMs: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return Promise.reject(cancelled())
  const delay = Number.isFinite(delayMs)
    ? Math.min(MAX_RETRY_DELAY_MS, Math.max(0, delayMs))
    : MAX_RETRY_DELAY_MS
  return new Promise<void>((resolve, reject) => {
    const cleanup = () => {
      clearTimeout(timer)
      signal?.removeEventListener('abort', onAbort)
    }
    const onAbort = () => {
      cleanup()
      reject(cancelled())
    }
    const timer = setTimeout(() => {
      cleanup()
      resolve()
    }, delay)
    signal?.addEventListener('abort', onAbort, { once: true })
    // Also cover an abort observed while a custom signal listener was being installed.
    if (signal?.aborted) onAbort()
  })
}

export async function runProviderAttempts<T>(
  provider: ProviderRequestPolicyInput,
  signal: AbortSignal | undefined,
  task: (attempt: number, timeoutMs: number) => Promise<T>,
  options: ProviderAttemptOptions,
): Promise<T> {
  const policy = providerPolicy(provider)
  const sleep = options.sleep ?? waitRetry
  for (let attempt = 1; attempt <= policy.maxAttempts; attempt++) {
    checkCancelled(signal)
    const metadata = { attempt, maxAttempts: policy.maxAttempts, timeoutMs: policy.timeoutMs }
    options.onAttempt?.(metadata)
    try {
      checkCancelled(signal)
      const result = await task(attempt, policy.timeoutMs)
      checkCancelled(signal)
      return result
    } catch (error) {
      checkCancelled(signal)
      if (attempt === policy.maxAttempts) throw markProviderAttemptsHandled(error)
      const decision = options.classify(error)
      checkCancelled(signal)
      if (!decision.retryable) throw markProviderAttemptsHandled(error)
      const delayMs = retryDelay(attempt, decision.retryAfterMs)
      options.onRetry?.({ ...metadata, delayMs })
      checkCancelled(signal)
      await sleep(delayMs, signal)
      checkCancelled(signal)
    }
  }
  // Validated policies always execute at least one attempt.
  throw new Error('invalid_provider_request_policy')
}

/** Upper bound used by probes: each attempt timeout plus each possible capped backoff. */
export function providerOperationBudget(provider: ProviderRequestPolicyInput): number {
  const policy = providerPolicy(provider)
  return policy.maxAttempts * policy.timeoutMs + policy.retryCount * MAX_RETRY_DELAY_MS
}
