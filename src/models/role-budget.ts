/** A retained role budget bounds the whole operation, not each retry separately. */
export async function withRoleBudget<T>(
  timeoutMs: number | undefined,
  parent: AbortSignal | undefined,
  task: (signal: AbortSignal | undefined) => Promise<T>,
  timeoutError: (reason: unknown, budgetMs: number) => Error,
): Promise<T> {
  if (timeoutMs === undefined) return task(parent)
  if (!Number.isFinite(timeoutMs) || timeoutMs < 1) throw new Error('invalid_role_budget')
  const deadline = new AbortController()
  const signal = parent ? AbortSignal.any([parent, deadline.signal]) : deadline.signal
  let timer: ReturnType<typeof setTimeout> | undefined
  const expired = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      const reason = new Error('model_stage_timeout')
      deadline.abort(reason)
      reject(timeoutError(reason, timeoutMs))
    }, timeoutMs)
  })
  try {
    return await Promise.race([task(signal), expired])
  } finally {
    if (timer) clearTimeout(timer)
  }
}
