interface Job<T> {
  priority: number
  signal?: AbortSignal
  task: () => Promise<T>
  resolve: (value: T) => void
  reject: (error: Error) => void
  cancel: () => void
}
export class WorkScheduler {
  private queue: Job<unknown>[] = []
  private running = 0
  constructor(
    private concurrency = 4,
    private readonly maxQueued = 256,
  ) {}
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
    return new Promise<T>((resolve, reject) => {
      const job: Job<T> = {
        priority,
        signal,
        task,
        resolve,
        reject,
        cancel: () => {
          const index = this.queue.indexOf(job as Job<unknown>)
          if (index >= 0) {
            this.queue.splice(index, 1)
            reject(new Error('request_cancelled'))
          }
        },
      }
      signal?.addEventListener('abort', job.cancel, { once: true })
      this.queue.push(job as Job<unknown>)
      this.queue.sort((a, b) => a.priority - b.priority)
      this.drain()
    })
  }
  private drain() {
    while (this.running < this.concurrency && this.queue.length) {
      const job = this.queue.shift()!
      job.signal?.removeEventListener('abort', job.cancel)
      if (job.signal?.aborted) {
        job.reject(new Error('request_cancelled'))
        continue
      }
      this.running++
      Promise.resolve()
        .then(job.task)
        .then(job.resolve, (error) =>
          job.reject(error instanceof Error ? error : new Error('work_failed')),
        )
        .finally(() => {
          this.running--
          this.drain()
        })
    }
  }
}
export const modelScheduler = new WorkScheduler()
