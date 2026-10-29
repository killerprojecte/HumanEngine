export type ContextualLearningJob = {
  sceneId: string
  /** Caller-derived candidate and provenance identity, never just the whole message ID. */
  key: string
  sourceIds: readonly string[]
  validate: () => boolean
  /** Must revalidate signal and authority at the actual guarded commit boundary. */
  run: (signal: AbortSignal) => Promise<unknown>
  /** Trusted receipt classification, not model arguments or a guarantee of continued validity. */
  committed?: (result: unknown) => boolean
}
export type ContextualLearningEvent = {
  jobId: string
  committed?: true
  status: 'queued' | 'running' | 'completed' | 'failed' | 'discarded' | 'cancelled'
}
export type ContextualLearningQueueOptions = {
  foregroundBusy?: () => boolean
  maxQueued?: number
  maxRemembered?: number
  pollMs?: number
  onEvent?: (event: ContextualLearningEvent) => void
}
export type ContextualLearningEnqueueResult =
  | { status: 'queued'; jobId: string }
  | { status: 'duplicate'; jobId: string }
  | { status: 'rejected'; reason: 'disposed' | 'invalid' | 'capacity' }

type Entry = {
  identity: string
  jobId: string
  job: ContextualLearningJob
  controller: AbortController
  cancelled: boolean
}

/**
 * Ephemeral candidate dispatch, not a durable command log. Queued never means saved.
 * A fresh signal decouples work from normal turn completion; source/scene/epoch
 * authority remains a caller-owned validate function. Failures never auto-retry.
 * Post-run validation discards the result, but cannot roll back an already committed write.
 */
export class ContextualLearningQueue {
  private readonly options: Required<Omit<ContextualLearningQueueOptions, 'onEvent'>> &
    Pick<ContextualLearningQueueOptions, 'onEvent'>
  private readonly pending: Entry[] = []
  private readonly identities = new Map<string, string>()
  private readonly remembered = new Map<string, string>()
  private active?: Entry
  private timer?: ReturnType<typeof setTimeout>
  private scheduled = false
  private disposed = false
  private sequence = 0
  private readonly idleWaiters = new Set<() => void>()
  private readonly totals = { accepted: 0, completed: 0, failed: 0, discarded: 0, cancelled: 0 }

  constructor(options: ContextualLearningQueueOptions = {}) {
    this.options = {
      foregroundBusy: options.foregroundBusy ?? (() => false),
      maxQueued: options.maxQueued ?? 128,
      maxRemembered: options.maxRemembered ?? 512,
      pollMs: options.pollMs ?? 100,
      onEvent: options.onEvent,
    }
    for (const key of ['maxQueued', 'maxRemembered', 'pollMs'] as const) {
      if (!Number.isSafeInteger(this.options[key]) || this.options[key] < 1) {
        throw new Error(`Invalid contextual learning queue option: ${key}`)
      }
    }
  }

  enqueue(job: ContextualLearningJob): ContextualLearningEnqueueResult {
    if (this.disposed) return { status: 'rejected', reason: 'disposed' }
    if (
      !job.sceneId ||
      !job.key ||
      !job.sourceIds.length ||
      job.sourceIds.some((id) => !id) ||
      !this.valid(job)
    ) {
      return { status: 'rejected', reason: 'invalid' }
    }
    const sourceIds = [...new Set(job.sourceIds)].sort()
    const identity = JSON.stringify([job.sceneId, job.key, sourceIds])
    const previous = this.identities.get(identity) ?? this.remembered.get(identity)
    if (previous) return { status: 'duplicate', jobId: previous }
    if (this.pending.length + Number(Boolean(this.active)) >= this.options.maxQueued) {
      return { status: 'rejected', reason: 'capacity' }
    }
    const jobId = `contextual-learning-${++this.sequence}`
    const entry: Entry = {
      identity,
      jobId,
      job: { ...job, sourceIds },
      controller: new AbortController(),
      cancelled: false,
    }
    this.identities.set(identity, jobId)
    this.pending.push(entry)
    this.totals.accepted++
    this.emit(entry, 'queued')
    this.resume()
    return { status: 'queued', jobId }
  }

  /** Safe to call on foreground completion; at most one polling timer exists otherwise. */
  resume(): void {
    if (this.disposed || this.active || this.scheduled) return
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
    this.scheduled = true
    queueMicrotask(() => {
      this.scheduled = false
      this.pump()
    })
  }

  cancelScene(sceneId: string): void {
    this.cancelWhere((entry) => entry.job.sceneId === sceneId)
  }

  recall(sceneId: string, sourceId: string): void {
    this.cancelWhere(
      (entry) => entry.job.sceneId === sceneId && entry.job.sourceIds.includes(sourceId),
    )
  }

  dispose(): void {
    this.disposed = true
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
    this.cancelWhere(() => true)
  }

  stats() {
    return {
      ...this.totals,
      queued: this.pending.length,
      running: Number(Boolean(this.active)),
      disposed: this.disposed,
    }
  }

  /** Waits for actual running promises to settle, including an aborted task that ignores its signal. */
  whenIdle(): Promise<void> {
    if (!this.active && !this.pending.length) return Promise.resolve()
    return new Promise((resolve) => this.idleWaiters.add(resolve))
  }

  private pump(): void {
    if (this.disposed || this.active || !this.pending.length) {
      this.notifyIdle()
      return
    }
    let busy = true
    try {
      busy = this.options.foregroundBusy()
    } catch {
      /* Fail closed on scheduler uncertainty. */
    }
    if (busy) {
      if (!this.timer)
        this.timer = setTimeout(() => {
          this.timer = undefined
          this.resume()
        }, this.options.pollMs)
      return
    }
    const entry = this.pending.shift()!
    this.active = entry
    void this.execute(entry)
  }

  private async execute(entry: Entry): Promise<void> {
    try {
      if (!this.valid(entry.job)) {
        this.terminal(entry, 'discarded')
        return
      }
      this.emit(entry, 'running')
      if (entry.controller.signal.aborted) {
        this.terminal(entry, 'cancelled')
        return
      }
      const result = await entry.job.run(entry.controller.signal)
      let committed = false
      try {
        committed = entry.job.committed?.(result) === true
      } catch {
        /* Unknown commit classification is never guessed. */
      }
      this.terminal(
        entry,
        entry.cancelled
          ? 'cancelled'
          : committed || this.valid(entry.job)
            ? 'completed'
            : 'discarded',
        committed,
      )
    } catch {
      // Arbitrary model errors may contain user data; emit only bounded status codes.
      this.terminal(entry, entry.cancelled ? 'cancelled' : 'failed')
    } finally {
      this.active = undefined
      this.notifyIdle()
      this.resume()
    }
  }

  private cancelWhere(matches: (entry: Entry) => boolean): void {
    for (let index = this.pending.length - 1; index >= 0; index--) {
      const entry = this.pending[index]!
      if (!matches(entry)) continue
      this.pending.splice(index, 1)
      entry.cancelled = true
      entry.controller.abort()
      this.terminal(entry, 'cancelled')
    }
    if (this.active && matches(this.active)) {
      this.active.cancelled = true
      this.active.controller.abort()
    }
    if (!this.pending.length && this.timer) {
      clearTimeout(this.timer)
      this.timer = undefined
    }
    this.notifyIdle()
  }

  private terminal(
    entry: Entry,
    status: 'completed' | 'failed' | 'discarded' | 'cancelled',
    committed = false,
  ): void {
    this.identities.delete(entry.identity)
    this.remembered.set(entry.identity, entry.jobId)
    while (this.remembered.size > this.options.maxRemembered) {
      this.remembered.delete(this.remembered.keys().next().value!)
    }
    this.totals[status]++
    this.emit(entry, status, committed)
  }

  private valid(job: ContextualLearningJob): boolean {
    try {
      return job.validate() === true
    } catch {
      return false
    }
  }

  private emit(entry: Entry, status: ContextualLearningEvent['status'], committed = false): void {
    try {
      this.options.onEvent?.({
        jobId: entry.jobId,
        status,
        ...(committed ? { committed: true as const } : {}),
      })
    } catch {
      /* Observability cannot change admission. */
    }
  }

  private notifyIdle(): void {
    if (this.active || this.pending.length) return
    for (const resolve of this.idleWaiters) resolve()
    this.idleWaiters.clear()
  }
}
