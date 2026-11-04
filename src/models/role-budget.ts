import { progressDeadline } from './stream-progress.ts'
/** Bound retries/queueing until first native streamed content; synchronous/custom calls retain the whole-operation bound. */
export async function withRoleBudget<T>(
  timeoutMs: number | undefined,
  parent: AbortSignal | undefined,
  task: (signal: AbortSignal | undefined) => Promise<T>,
  timeoutError: (reason: unknown, budgetMs: number) => Error,
): Promise<T> {
  if (timeoutMs === undefined) return task(parent)
  if (!Number.isFinite(timeoutMs) || timeoutMs < 1) throw new Error('invalid_role_budget')
  const deadline = progressDeadline(parent, timeoutMs, () => new Error('model_stage_timeout'))
  let rejectTimeout: (() => void) | undefined
  const expired = new Promise<never>((_, reject) => {
    rejectTimeout = () => {
      if (!parent?.aborted) reject(timeoutError(deadline.signal.reason, timeoutMs))
    }
    deadline.signal.addEventListener('abort', rejectTimeout, { once: true })
    if (deadline.signal.aborted) rejectTimeout()
  })
  try {
    return await Promise.race([task(deadline.signal), expired])
  } finally {
    deadline.dispose()
    if (rejectTimeout) deadline.signal.removeEventListener('abort', rejectTimeout)
  }
}
