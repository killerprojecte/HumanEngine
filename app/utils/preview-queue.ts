/** Reconcile viewport additions without cancelling work already in progress. */
export class PreviewQueue {
  private desired = new Set<string>()
  private pending: string[] = []
  private started = new Set<string>()
  private completed = new Set<string>()
  private controller = new AbortController()
  private active = 0
  private disposed = false

  constructor(
    private readonly load: (id: string, signal: AbortSignal) => Promise<void>,
    private readonly concurrency = 3,
  ) {}

  update(ids: string[]) {
    if (this.disposed) return
    const next = new Set(ids)
    // A page/filter replacement retires old results; mere viewport additions do not.
    if ([...this.desired].some((id) => !next.has(id))) {
      this.controller.abort()
      this.controller = new AbortController()
      this.completed = new Set([...this.completed].filter((id) => next.has(id)))
      this.started = new Set(this.completed)
      this.pending = []
    }
    this.desired = next
    for (const id of ids)
      if (!this.started.has(id)) {
        this.started.add(id)
        this.pending.push(id)
      }
    this.pump()
  }

  dispose() {
    this.disposed = true
    this.controller.abort()
    this.pending = []
  }

  private pump() {
    while (!this.disposed && this.active < this.concurrency && this.pending.length) {
      const id = this.pending.shift()!
      const signal = this.controller.signal
      this.active++
      void Promise.resolve()
        .then(() => {
          if (!signal.aborted) return this.load(id, signal)
        })
        .then(() => {
          if (!signal.aborted) this.completed.add(id)
        })
        .catch(() => {})
        .finally(() => {
          this.active--
          this.pump()
        })
    }
  }
}
