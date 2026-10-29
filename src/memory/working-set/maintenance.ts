/** Notification-driven deterministic projection work; never a model or chat-replay scheduler. */
export class WorkingSetMaintenance {
  private timer?: ReturnType<typeof setTimeout>
  private started = false
  constructor(
    private readonly port: { enabled(): boolean; drainOneBatch(): boolean; failed?(): void },
    private readonly intervalMs = 250,
  ) {
    if (!Number.isSafeInteger(intervalMs) || intervalMs < 10)
      throw new Error('working_set_maintenance_interval')
  }
  private failed() {
    try {
      this.port.failed?.()
    } catch {
      /* Reporting cannot restart or escape a stopped maintenance tick. */
    }
  }
  start() {
    if (this.started) return
    this.started = true
    this.notify()
  }
  notify() {
    if (!this.started || this.timer) return
    try {
      if (!this.port.enabled()) return
    } catch {
      this.failed()
      return
    }
    this.timer = setTimeout(() => {
      this.timer = undefined
      if (!this.started) return
      try {
        if (this.port.enabled() && this.port.drainOneBatch()) this.notify()
      } catch {
        this.failed()
      }
    }, this.intervalMs)
    this.timer.unref?.()
  }
  configure() {
    this.cancel()
    this.notify()
  }
  cancel() {
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
  }
  dispose() {
    this.started = false
    this.cancel()
  }
}
