/** Topic-local admission state. It never starts timers, models, or transport operations. */
export interface TurnWaitConfig {
  quietMs: number
  maxWaitMs: number
  addressedQuietMs: number
  maxModelWaitMs: number
  maxPending: number
}
export interface TurnWaitEvent {
  id: string
  /** Caller-owned cheap relevance signal, not a claim that the message is true. */
  meaningful: boolean
  addressed?: boolean
}
export interface TurnWake {
  kind: 'run' | 'skip' | 'recheck'
  generation: number
  eventIds: string[]
  reason: 'quiet' | 'max_wait' | 'model_wait' | 'no_new_signal'
}
interface PendingEvent extends TurnWaitEvent {
  at: number
}

export class TurnWaitCoordinator {
  private generation = 0
  private pending = new Map<string, PendingEvent>()
  private seen = new Set<string>()
  private active?: TurnWake
  private waiting?: { at: number; generation: number; eventIds: string[] }
  private lastWaitGeneration = -1

  constructor(
    private config: Readonly<TurnWaitConfig>,
    private readonly now: () => number,
  ) {
    this.configure(config)
  }

  /** Update admission timing without discarding buffered inputs or active-attempt identity. */
  configure(config: Readonly<TurnWaitConfig>): void {
    for (const value of Object.values(config))
      if (!Number.isSafeInteger(value) || value < 0) throw new Error('invalid_turn_wait_config')
    if (config.maxPending < 1 || config.maxWaitMs < config.quietMs)
      throw new Error('invalid_turn_wait_config')
    this.config = { ...config }
  }

  observe(event: TurnWaitEvent): 'accepted' | 'duplicate' | 'full' {
    if (!event.id) throw new Error('invalid_turn_wait_event')
    if (this.seen.has(event.id)) return 'duplicate'
    if (this.pending.size >= this.config.maxPending) return 'full'
    this.pending.set(event.id, { ...event, at: this.now() })
    this.seen.add(event.id)
    // Runtime ingress remains responsible for long-lived platform replay deduplication.
    if (this.seen.size > this.config.maxPending * 4)
      this.seen.delete(this.seen.values().next().value!)
    this.generation++
    return 'accepted'
  }

  recall(id: string): void {
    const removed = this.pending.delete(id)
    const affectsWait = this.waiting?.eventIds.includes(id)
    if (removed || affectsWait || this.active?.eventIds.includes(id)) this.generation++
    if (affectsWait) this.waiting = undefined
  }

  /** Trusted edit only: retire the old snapshot, then let the caller observe its replacement.
   * This grants no transport retry and does not release an active attempt before finish().
   */
  replace(id: string): void {
    if (!id) throw new Error('invalid_turn_wait_event')
    this.pending.delete(id)
    this.seen.delete(id)
    if (this.waiting?.eventIds.includes(id)) this.waiting = undefined
    this.generation++
  }

  /** Read-only eviction guard; grants no wake, retry or generation transition. */
  retention(): Readonly<{ active: boolean; pending: boolean; waiting: boolean }> {
    return { active: !!this.active, pending: this.pending.size > 0, waiting: !!this.waiting }
  }

  isCurrent(wake: TurnWake): boolean {
    return wake === this.active && wake.generation === this.generation
  }

  nextWakeAt(): number | undefined {
    if (this.active) return undefined
    const pendingAt = this.pendingDeadline()
    if (pendingAt === undefined) return this.waiting?.at
    return this.waiting ? Math.min(pendingAt, this.waiting.at) : pendingAt
  }

  /** Consume one cheap admission decision. A timer expiry is not itself model permission. */
  takeReady(): TurnWake | undefined {
    if (this.active) return undefined
    const now = this.now()
    const deadline = this.pendingDeadline()
    // An existing model WAIT cannot force an unready fresh batch into another model call.
    if (deadline === undefined || deadline > now) {
      if (!this.waiting || this.waiting.at > now) return undefined
      const generation = this.waiting.generation
      this.waiting = undefined
      return { kind: 'recheck', generation, eventIds: [], reason: 'model_wait' }
    }
    const events = [...this.pending.values()]
    const meaningful = events.some((event) => event.meaningful || event.addressed)
    const first = events[0]!.at
    const wake: TurnWake = {
      kind: meaningful ? 'run' : 'skip',
      generation: this.generation,
      eventIds: events.map((event) => event.id),
      reason: !meaningful
        ? 'no_new_signal'
        : now >= first + this.config.maxWaitMs
          ? 'max_wait'
          : 'quiet',
    }
    this.pending.clear()
    this.waiting = undefined
    if (wake.kind === 'run') this.active = wake
    return wake
  }

  /** Finish exactly this attempt, never requeue its inputs or infer a send outcome. */
  finish(wake: TurnWake, result: { waitUntil?: number } = {}): boolean {
    if (this.active !== wake) return false
    this.active = undefined
    if (
      result.waitUntil !== undefined &&
      wake.generation === this.generation &&
      this.lastWaitGeneration !== wake.generation &&
      Number.isFinite(result.waitUntil)
    ) {
      const now = this.now()
      // A zero/past WAIT is terminal for this generation, not a zero-delay model loop.
      if (result.waitUntil > now && this.config.maxModelWaitMs > 0) {
        this.waiting = {
          at: Math.min(result.waitUntil, now + this.config.maxModelWaitMs),
          generation: wake.generation,
          eventIds: [...wake.eventIds],
        }
        this.lastWaitGeneration = wake.generation
      }
    }
    return true
  }

  /** Disable/dispose clears the buffer; enabling later must observe fresh ingress only. */
  cancel(): void {
    this.generation++
    this.pending.clear()
    this.active = undefined
    this.waiting = undefined
  }

  private pendingDeadline(): number | undefined {
    const events = [...this.pending.values()]
    if (!events.length) return undefined
    const first = events[0]!.at
    const last = events.at(-1)!.at
    const quiet = events.some((event) => event.addressed)
      ? Math.min(this.config.quietMs, this.config.addressedQuietMs)
      : this.config.quietMs
    return Math.min(last + quiet, first + this.config.maxWaitMs)
  }
}
