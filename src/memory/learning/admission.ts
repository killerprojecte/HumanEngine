/** Metadata-only admission. Source authority, payloads, model scheduling and retries belong to callers. */
export type LearningLane = 'realtime' | 'background'
export type LearningAdmissionOptions = {
  now?: () => number
  quietMs?: number
  maxWaitMs?: number
  minIntervalMs?: number
  maxSourcesPerScene?: number
  maxSourcesTotal?: number
  maxScenes?: number
}
export type LearningCapacity = { foregroundBusy?: boolean; budgetAvailable?: boolean }
export type LearningBatch = {
  sceneId: string
  token: number
  lane: LearningLane
  sourceKeys: string[]
}
export type LearningAdmissionStatus =
  | { state: 'idle' | 'disabled' | 'active' }
  | { state: 'blocked'; reason: 'foreground_busy' | 'budget_unavailable' }
  | {
      state: 'waiting' | 'ready'
      dueAt: number
      firstAdmittedAt: number
      pending: number
      lane: LearningLane
    }

type Source = { key: string; at: number; lane: LearningLane }
type Scene = {
  pending: Map<string, Source>
  active?: { token: number; sources: Source[] }
  lastStartedAt?: number
  retryNotBefore?: number
}

/**
 * In-memory only: restarting or disabling never replays paused-period messages.
 * Windows are admission bounds, not periodic learning clocks. Max wait cannot
 * override foreground work, a model budget, an active batch or the hard minimum interval.
 * A lane is a caller-supplied scheduling hint, not a claim that a message contains a fact.
 */
export class LearningAdmission {
  private readonly now: () => number
  private options: Required<Omit<LearningAdmissionOptions, 'now'>>
  private readonly scenes = new Map<string, Scene>()
  private enabled = true
  private sequence = 0

  constructor(options: LearningAdmissionOptions = {}) {
    this.now = options.now ?? Date.now
    this.options = {
      quietMs: options.quietMs ?? 2_000,
      maxWaitMs: options.maxWaitMs ?? 8_000,
      minIntervalMs: options.minIntervalMs ?? 15_000,
      maxSourcesPerScene: options.maxSourcesPerScene ?? 2_048,
      maxSourcesTotal: options.maxSourcesTotal ?? 8_192,
      maxScenes: options.maxScenes ?? 256,
    }
    this.configure({})
  }

  /** Validate atomically; existing sources and active tokens survive smaller capacity limits. */
  configure(options: Omit<LearningAdmissionOptions, 'now'>): void {
    const next = { ...this.options }
    for (const key of Object.keys(next) as Array<keyof typeof next>) {
      if (options[key] !== undefined) next[key] = options[key]
    }
    for (const [key, value] of Object.entries(next)) {
      if (
        !Number.isSafeInteger(value) ||
        value < (key.startsWith('maxSources') || key === 'maxScenes' ? 1 : 0)
      ) {
        throw new Error(`Invalid learning admission option: ${key}`)
      }
    }
    if (next.maxWaitMs < next.quietMs) {
      throw new Error('Learning maximum wait must not be shorter than quiet window')
    }
    this.options = next
  }

  /** Returned active tokens must be aborted by the owner; this policy never owns model requests. */
  setEnabled(enabled: boolean): LearningBatch[] {
    this.enabled = enabled
    if (enabled) return []
    const cancelled = this.activeBatches()
    this.scenes.clear()
    return cancelled
  }

  admit(
    sceneId: string,
    sourceKey: string,
    lane: LearningLane = 'realtime',
  ): 'accepted' | 'duplicate' | 'disabled' | 'capacity' {
    if (!sceneId || !sourceKey) throw new Error('Learning admission requires scene and source keys')
    if (!this.enabled) return 'disabled'
    const now = this.now()
    this.pruneIdle(now)
    let scene = this.scenes.get(sceneId)
    if (
      scene?.pending.has(sourceKey) ||
      scene?.active?.sources.some((source) => source.key === sourceKey)
    ) {
      return 'duplicate'
    }
    if (
      (!scene && this.scenes.size >= this.options.maxScenes) ||
      (scene && this.count(scene) >= this.options.maxSourcesPerScene) ||
      [...this.scenes.values()].reduce((sum, value) => sum + this.count(value), 0) >=
        this.options.maxSourcesTotal
    ) {
      return 'capacity'
    }
    if (!scene) {
      scene = { pending: new Map() }
      this.scenes.set(sceneId, scene)
    }
    scene.pending.set(sourceKey, { key: sourceKey, at: now, lane })
    return 'accepted'
  }

  inspect(sceneId: string, capacity: LearningCapacity = {}): LearningAdmissionStatus {
    if (!this.enabled) return { state: 'disabled' }
    const scene = this.scenes.get(sceneId)
    if (scene?.active) return { state: 'active' }
    if (!scene?.pending.size) return { state: 'idle' }
    if (capacity.foregroundBusy) return { state: 'blocked', reason: 'foreground_busy' }
    if (capacity.budgetAvailable === false)
      return { state: 'blocked', reason: 'budget_unavailable' }
    const sources = [...scene.pending.values()]
    const firstAdmittedAt = Math.min(...sources.map((source) => source.at))
    const latestAt = Math.max(...sources.map((source) => source.at))
    const dueAt = Math.max(
      Math.min(latestAt + this.options.quietMs, firstAdmittedAt + this.options.maxWaitMs),
      scene.lastStartedAt === undefined
        ? -Infinity
        : scene.lastStartedAt + this.options.minIntervalMs,
      scene.retryNotBefore ?? -Infinity,
    )
    return {
      state: this.now() >= dueAt ? 'ready' : 'waiting',
      dueAt,
      firstAdmittedAt,
      pending: sources.length,
      lane: sources.some((source) => source.lane === 'realtime') ? 'realtime' : 'background',
    }
  }

  /** Ready scenes in lane/age order. No preemption or model-slot reservation is implied. */
  readyScenes(capacity: LearningCapacity = {}): string[] {
    return [...this.scenes.keys()]
      .flatMap((sceneId) => {
        const status = this.inspect(sceneId, capacity)
        return status.state === 'ready' ? [{ sceneId, ...status }] : []
      })
      .sort(
        (a, b) =>
          Number(a.lane === 'background') - Number(b.lane === 'background') ||
          a.firstAdmittedAt - b.firstAdmittedAt ||
          a.sceneId.localeCompare(b.sceneId),
      )
      .map(({ sceneId }) => sceneId)
  }

  take(sceneId: string, limit = 60, capacity: LearningCapacity = {}): LearningBatch | undefined {
    if (!Number.isSafeInteger(limit) || limit < 1) throw new Error('Invalid learning batch limit')
    const status = this.inspect(sceneId, capacity)
    if (status.state !== 'ready') return undefined
    const scene = this.scenes.get(sceneId)!
    // Oldest-first preserves backlog progress under bursts, rather than repeatedly skipping old sources.
    const sources = [...scene.pending.values()].slice(0, limit)
    for (const source of sources) scene.pending.delete(source.key)
    scene.active = { token: ++this.sequence, sources }
    scene.lastStartedAt = this.now()
    scene.retryNotBefore = undefined
    return this.batch(sceneId, scene.active)
  }

  /** Retry is explicit and owned/bounded by the caller; failures do not create timers or automatic loops. */
  finish(sceneId: string, token: number, retryAfterMs?: number): boolean {
    if (retryAfterMs !== undefined && (!Number.isSafeInteger(retryAfterMs) || retryAfterMs < 0)) {
      throw new Error('Invalid learning retry delay')
    }
    const scene = this.scenes.get(sceneId)
    if (!scene?.active || scene.active.token !== token) return false
    if (retryAfterMs !== undefined) {
      scene.pending = new Map([
        ...scene.active.sources.map((source) => [source.key, source] as const),
        ...scene.pending,
      ])
      scene.retryNotBefore = this.now() + retryAfterMs
    }
    scene.active = undefined
    return true
  }

  /**
   * Abort the returned batch. Unknown or partially committed work must not replay.
   * Only the owner, after proving that no commit began, may requeue survivors.
   */
  recall(
    sceneId: string,
    sourceKey: string,
    options: { requeueSurvivors?: boolean } = {},
  ): LearningBatch | undefined {
    const scene = this.scenes.get(sceneId)
    if (!scene) return undefined
    scene.pending.delete(sourceKey)
    if (!scene.active?.sources.some((source) => source.key === sourceKey)) return undefined
    const cancelled = this.batch(sceneId, scene.active)
    if (options.requeueSurvivors === true) {
      const survivors = scene.active.sources.filter((source) => source.key !== sourceKey)
      scene.pending = new Map([
        ...survivors.map((source) => [source.key, source] as const),
        ...scene.pending,
      ])
    }
    scene.active = undefined
    return cancelled
  }

  cancelScene(sceneId: string): LearningBatch | undefined {
    const scene = this.scenes.get(sceneId)
    this.scenes.delete(sceneId)
    return scene?.active ? this.batch(sceneId, scene.active) : undefined
  }

  private batch(sceneId: string, active: NonNullable<Scene['active']>): LearningBatch {
    return {
      sceneId,
      token: active.token,
      lane: active.sources.some((source) => source.lane === 'realtime') ? 'realtime' : 'background',
      sourceKeys: active.sources.map((source) => source.key),
    }
  }

  private activeBatches(): LearningBatch[] {
    return [...this.scenes].flatMap(([id, scene]) =>
      scene.active ? [this.batch(id, scene.active)] : [],
    )
  }

  private count(scene: Scene): number {
    return scene.pending.size + (scene.active?.sources.length ?? 0)
  }

  private pruneIdle(now: number): void {
    for (const [id, scene] of this.scenes) {
      if (
        !this.count(scene) &&
        (scene.lastStartedAt === undefined ||
          now >= scene.lastStartedAt + this.options.minIntervalMs)
      ) {
        this.scenes.delete(id)
      }
    }
  }
}
