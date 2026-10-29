import { AsyncLocalStorage } from 'node:async_hooks'

interface Job<T> {
  priority: number
  enqueuedAt: number
  sequence: number
  signal?: AbortSignal
  task: () => Promise<T>
  resolve: (value: T) => void
  reject: (error: Error) => void
  cancel: () => void
}
export interface WorkSchedulerOptions {
  reserveForeground?: number
  backgroundAgingMs?: number
  now?: () => number
}
export class WorkScheduler {
  private queue: Job<unknown>[] = []
  private running = 0
  private runningNonForeground = 0
  private sequence = 0
  private readonly reserveForeground: number
  private readonly backgroundAgingMs?: number
  private readonly now: () => number
  constructor(
    private concurrency = 4,
    private readonly maxQueued = 256,
    options: WorkSchedulerOptions = {},
  ) {
    this.reserveForeground = options.reserveForeground ?? 0
    this.backgroundAgingMs = options.backgroundAgingMs
    this.now = options.now ?? Date.now
    if (
      !Number.isSafeInteger(this.reserveForeground) ||
      this.reserveForeground < 0 ||
      (this.backgroundAgingMs !== undefined &&
        (!Number.isSafeInteger(this.backgroundAgingMs) || this.backgroundAgingMs < 0))
    )
      throw new Error('invalid_scheduler_options')
  }
  configure(concurrency: number) {
    if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 32)
      throw new Error('invalid_concurrency')
    this.concurrency = concurrency
    this.drain()
  }
  get status() {
    return { running: this.running, queued: this.queue.length, concurrency: this.concurrency }
  }
  run<T>(priority: number, signal: AbortSignal | undefined, task: () => Promise<T>): Promise<T> {
    if (signal?.aborted) return Promise.reject(new Error('request_cancelled'))
    if (this.queue.length >= this.maxQueued) return Promise.reject(new Error('work_queue_full'))
    const inSubmissionContext = AsyncLocalStorage.snapshot()
    return new Promise<T>((resolve, reject) => {
      const job: Job<T> = {
        priority,
        enqueuedAt: this.now(),
        sequence: this.sequence++,
        signal,
        task: () => inSubmissionContext(task),
        resolve,
        reject,
        cancel: () => {
          const index = this.queue.indexOf(job as Job<unknown>)
          if (index >= 0) {
            this.queue.splice(index, 1)
            reject(new Error('request_cancelled'))
            this.drain()
          }
        },
      }
      signal?.addEventListener('abort', job.cancel, { once: true })
      this.queue.push(job as Job<unknown>)
      this.drain()
    })
  }
  private effectivePriority(job: Job<unknown>): number {
    return job.priority === 2 &&
      this.backgroundAgingMs !== undefined &&
      this.now() - job.enqueuedAt >= this.backgroundAgingMs
      ? 1
      : job.priority
  }
  private drain() {
    while (this.running < this.concurrency && this.queue.length) {
      this.queue.sort(
        (a, b) => this.effectivePriority(a) - this.effectivePriority(b) || a.sequence - b.sequence,
      )
      // With one slot there is no simultaneous reservation or preemption guarantee.
      const nonForegroundLimit =
        this.concurrency === 1 ? 1 : Math.max(1, this.concurrency - this.reserveForeground)
      const index = this.queue.findIndex(
        (job) => job.priority <= 0 || this.runningNonForeground < nonForegroundLimit,
      )
      if (index < 0) return
      const job = this.queue.splice(index, 1)[0]!
      job.signal?.removeEventListener('abort', job.cancel)
      if (job.signal?.aborted) {
        job.reject(new Error('request_cancelled'))
        continue
      }
      const nonForeground = job.priority > 0
      this.running++
      if (nonForeground) this.runningNonForeground++
      Promise.resolve()
        .then(job.task)
        .then(job.resolve, (error) =>
          job.reject(error instanceof Error ? error : new Error('work_failed')),
        )
        .finally(() => {
          this.running--
          if (nonForeground) this.runningNonForeground--
          this.drain()
        })
    }
  }
}

const modelPriority = new AsyncLocalStorage<1 | 2>()
/** A background scope can lower scheduling urgency, never elevate it to foreground. */
export function withModelPriority<T>(priority: 1 | 2, task: () => T): T {
  if (priority !== 1 && priority !== 2) throw new Error('invalid_model_priority')
  const inherited = modelPriority.getStore()
  return modelPriority.run(Math.max(priority, inherited ?? priority) as 1 | 2, task)
}
export function effectiveModelPriority(basePriority: number): number {
  return Math.max(basePriority, modelPriority.getStore() ?? basePriority)
}

export const modelScheduler = new WorkScheduler(4, 256, {
  reserveForeground: 1,
  backgroundAgingMs: 5 * 60_000,
})
