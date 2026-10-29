import { ModelReadPolicy } from './memory/read/model-read-policy.ts'
import { withBudgetContext } from './budget/native-attempt.ts'
import { withModelPriority } from './scheduler.ts'
import { startFlow } from './diagnostics/flow.ts'
import { createHash, randomUUID } from 'node:crypto'
import { ModelError, type Router, type RouterConfig } from './models.ts'
import type { MemoryRecord, MemoryService } from './memory.ts'
import type { Store } from './store.ts'

const hash = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const contentHash = (memory: MemoryRecord) => hash([memory.id, memory.content])
export interface IndexGeneration {
  generationId: string
  encoderRevision: string
  modelId: string
  dimensions: number
  spaceId: string
}
export interface RebuildJob {
  id: string
  encoderRevision: string
  modelId: string
  state: 'running' | 'paused' | 'failed' | 'cancelled' | 'completed'
  processed: number
  total: number
  dimensions?: number
  spaceId?: string
  error?: string
  requiresRetryConfirmation: boolean
  expectedActive?: string
  /** Fences late responses from earlier process/worker attempts. */
  attemptId?: string
  ownerId?: string
  leaseUntil?: number
}
export interface EmbeddingIndexStatus {
  state: 'unconfigured' | 'needs_rebuild' | 'ready'
  active?: IndexGeneration
  selected?: { modelId: string; encoderRevision: string; requestedDimensions?: number }
  job?: RebuildJob
  coverage: { eligible: number; indexed: number; pending: number }
  retained: { generations: number; vectors: number }
}

/** Identity deliberately excludes credentials, credential names, batch size and timeouts. */
function encoder(config: RouterConfig) {
  const route = config.routes.find((r) => r.role === 'embedding')
  const model = config.models.find((m) => m.id === route?.orderedModelIds[0])
  const provider = config.providers.find((p) => p.id === model?.providerId)
  if (!model?.capabilities.includes('embedding') || !provider) return undefined
  const url = new URL(provider.baseUrl)
  // Credentials in an endpoint URL must not be persisted, even as a hash.
  if (url.username || url.password || url.search || url.hash)
    throw new Error('embedding_endpoint_must_not_contain_credentials_or_query')
  const revision = hash([
    provider.id,
    provider.protocol,
    url.origin + url.pathname,
    model.id,
    model.apiModelName,
    model.embeddingDimensions ?? null,
    'text-v1',
    false,
  ])
  return { model, provider, revision }
}

/** Derived index work only: checkpoints contain IDs/hashes, never raw chats or credentials. */
export class EmbeddingIndex {
  private controller?: AbortController
  private work?: Promise<void>
  private bootstrap = false
  private readonly ownerId = randomUUID()
  private readonly leaseMs = 30_000
  private heartbeat?: ReturnType<typeof setInterval>
  private ownedAttempt?: string
  constructor(
    private readonly store: Store,
    private readonly memory: MemoryService,
    private readonly router: (config: RouterConfig, admit?: () => void) => Router,
    private readonly options: { now?: () => number } = {},
  ) {
    store.db.exec(`CREATE TABLE IF NOT EXISTS embedding_rebuild_items (
      job_id TEXT NOT NULL, memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
      content_hash TEXT NOT NULL, PRIMARY KEY(job_id,memory_id)
    );`)
    this.bootstrap =
      this.memory.adminList().length === 0 &&
      !this.store.db.prepare('SELECT 1 FROM vector_spaces LIMIT 1').get() &&
      !this.active() &&
      !this.job()
    this.recoverExpired()
  }
  private now() {
    return this.options.now?.() ?? Date.now()
  }
  private recoverExpired() {
    this.store.db
      .transaction(() => {
        const job = this.job()
        if (job?.state === 'running' && (job.leaseUntil ?? 0) <= this.now()) {
          job.state = 'paused'
          job.error = job.requiresRetryConfirmation
            ? 'lease_expired_uncertain_request'
            : 'lease_expired'
          this.save(job)
        }
      })
      .immediate()
  }
  private owned(job: RebuildJob, stored = this.job()) {
    return (
      stored?.id === job.id &&
      stored.state === 'running' &&
      stored.attemptId === job.attemptId &&
      stored.ownerId === this.ownerId &&
      (stored.leaseUntil ?? 0) > this.now()
    )
  }
  private saveOwned(job: RebuildJob) {
    return this.store.db
      .transaction(() => {
        if (!this.owned(job)) return false
        job.leaseUntil = this.now() + this.leaseMs
        this.save(job)
        return true
      })
      .immediate()
  }

  private job(): RebuildJob | undefined {
    return this.store.get('embedding.rebuild', undefined)
  }
  private active(): IndexGeneration | undefined {
    return this.store.get('embedding.active', undefined)
  }
  private save(job: RebuildJob) {
    this.store.set('embedding.rebuild', job)
  }
  status(): EmbeddingIndexStatus {
    this.recoverExpired()
    let selected: ReturnType<typeof encoder>
    try {
      selected = encoder(this.store.models)
    } catch {
      selected = undefined
    }
    const active = this.active(),
      job = this.job()
    const eligible = this.memory.indexable()
    const compatible =
      active !== undefined &&
      selected?.revision === active.encoderRevision &&
      this.store.get('embedding.incompatible', '') !== active.generationId
    const indexedIds = new Set(
      compatible && active
        ? (
            this.store.db
              .prepare('SELECT memory_id FROM memory_vectors WHERE space_id=?')
              .all(active.spaceId) as { memory_id: string }[]
          ).map((r) => r.memory_id)
        : [],
    )
    const indexed = eligible.filter((m) => indexedIds.has(m.id)).length
    return {
      state: !selected ? 'unconfigured' : compatible ? 'ready' : 'needs_rebuild',
      ...(active ? { active } : {}),
      ...(selected
        ? {
            selected: {
              modelId: selected.model.id,
              encoderRevision: selected.revision,
              ...(selected.model.embeddingDimensions === undefined
                ? {}
                : { requestedDimensions: selected.model.embeddingDimensions }),
            },
          }
        : {}),
      ...(job ? { job } : {}),
      coverage: { eligible: eligible.length, indexed, pending: eligible.length - indexed },
      retained: {
        generations: (
          this.store.db
            .prepare(
              "SELECT count(*) n FROM vector_spaces WHERE json_extract(value,'$.generation') IS NOT NULL",
            )
            .get() as { n: number }
        ).n,
        vectors: (
          this.store.db
            .prepare(
              "SELECT count(*) n FROM memory_vectors v JOIN vector_spaces s ON s.id=v.space_id WHERE json_extract(s.value,'$.generation') IS NOT NULL",
            )
            .get() as { n: number }
        ).n,
      },
    }
  }
  private assertExpectedJob(expected: string | null | undefined) {
    if (expected !== undefined && (this.job()?.id ?? null) !== expected)
      throw new Error('embedding_job_changed')
  }
  start(expectedJobId?: string | null, expectedEncoderRevision?: string): RebuildJob {
    if (this.work) throw new Error('embedding_rebuild_busy')
    const job = this.store.db
      .transaction(() => {
        this.assertExpectedJob(expectedJobId)
        this.recoverExpired()
        if (this.job()?.state === 'running') throw new Error('embedding_rebuild_busy')
        const selected = encoder(this.store.models)
        if (!selected) throw new Error('embedding_model_not_configured')
        if (expectedEncoderRevision !== undefined && selected.revision !== expectedEncoderRevision)
          throw new Error('embedding_config_changed')
        this.bootstrap = false
        const next: RebuildJob = {
          id: randomUUID(),
          encoderRevision: selected.revision,
          modelId: selected.model.id,
          state: 'running',
          processed: 0,
          total: this.memory.indexable().length,
          requiresRetryConfirmation: false,
          expectedActive: this.active()?.generationId,
          attemptId: randomUUID(),
          ownerId: this.ownerId,
          leaseUntil: this.now() + this.leaseMs,
        }
        this.save(next)
        this.pruneRetained()
        return next
      })
      .immediate()
    this.launch(job)
    return job
  }
  resume(confirmUnknownRequest = false, expectedJobId?: string | null): RebuildJob {
    if (this.work) throw new Error('embedding_rebuild_busy')
    const job = this.store.db
      .transaction(() => {
        this.assertExpectedJob(expectedJobId)
        this.recoverExpired()
        const next = this.job(),
          selected = encoder(this.store.models)
        if (!next || !['paused', 'failed'].includes(next.state))
          throw new Error('embedding_rebuild_not_resumable')
        if (!selected || next.encoderRevision !== selected.revision)
          throw new Error('embedding_config_changed')
        if (next.requiresRetryConfirmation && !confirmUnknownRequest)
          throw new Error('embedding_retry_confirmation_required')
        next.state = 'running'
        delete next.error
        next.requiresRetryConfirmation = false
        next.attemptId = randomUUID()
        next.ownerId = this.ownerId
        next.leaseUntil = this.now() + this.leaseMs
        this.save(next)
        return next
      })
      .immediate()
    this.launch(job)
    return job
  }
  pause(expectedJobId?: string | null): RebuildJob {
    return this.stop('paused', expectedJobId)
  }
  cancel(expectedJobId?: string | null): RebuildJob {
    return this.stop('cancelled', expectedJobId)
  }
  private stop(state: 'paused' | 'cancelled', expectedJobId?: string | null): RebuildJob {
    const job = this.store.db
      .transaction(() => {
        this.assertExpectedJob(expectedJobId)
        const current = this.job()
        if (!current || !['running', 'paused', 'failed'].includes(current.state))
          throw new Error('embedding_rebuild_not_running')
        current.state = state
        this.save(current)
        return current
      })
      .immediate()
    if (job.ownerId === this.ownerId && job.attemptId === this.ownedAttempt)
      this.controller?.abort()
    return job
  }
  async dispose() {
    this.controller?.abort()
    try {
      if (this.ownedAttempt)
        this.store.db
          .transaction(() => {
            const job = this.job()
            if (
              job?.state === 'running' &&
              job.ownerId === this.ownerId &&
              job.attemptId === this.ownedAttempt
            ) {
              job.state = 'paused'
              this.save(job)
            }
          })
          .immediate()
    } catch (error) {
      // Shutdown must stop work without mutating a database behind the authority head.
      if (!(error instanceof Error) || error.message !== 'recovery_authority_head_changed')
        throw error
    } finally {
      await this.work
    }
  }
  async settled() {
    await this.work
  }
  private launch(job: RebuildJob) {
    const controller = new AbortController()
    this.controller = controller
    this.ownedAttempt = job.attemptId
    const config = this.store.models
    const heartbeat = setInterval(() => {
      try {
        const renewed = this.store.db
          .transaction(() => {
            const current = this.job()
            if (!this.owned(job, current)) return false
            current!.leaseUntil = this.now() + this.leaseMs
            this.save(current!)
            return true
          })
          .immediate()
        if (!renewed) controller.abort()
      } catch {
        controller.abort()
      }
    }, this.leaseMs / 3)
    heartbeat.unref()
    this.heartbeat = heartbeat
    this.work = withBudgetContext(
      { turnId: `embedding-rebuild:${job.id}`, lane: 'maintenance' },
      () => withModelPriority(2, () => this.run(job, config, controller.signal)),
    ).finally(() => {
      clearInterval(heartbeat)
      if (this.controller === controller) {
        this.controller = undefined
        this.work = undefined
        this.ownedAttempt = undefined
        this.heartbeat = undefined
      }
    })
  }
  private current(job: RebuildJob, signal: AbortSignal) {
    if (signal.aborted || !this.owned(job)) return false
    if (encoder(this.store.models)?.revision !== job.encoderRevision) {
      job.state = 'paused'
      job.error = 'embedding_config_changed'
      this.saveOwned(job)
      return false
    }
    return true
  }
  private modelReadGuard(records: readonly MemoryRecord[]) {
    const policy = new ModelReadPolicy(this.store)
    const tickets = new Map<string, ReturnType<ModelReadPolicy['require']>>()
    for (const record of records) {
      if (record.scope.kind !== 'scene') continue
      const sceneId = record.scope.sceneId
      const ticket = tickets.get(sceneId) ?? policy.require(sceneId)
      tickets.set(sceneId, ticket)
      if (
        !ticket.personalizationEnabled &&
        (record.scope.personId || ['experience', 'summary'].includes(record.kind))
      )
        throw new Error('personalization_disabled')
    }
    return () => {
      for (const ticket of tickets.values()) policy.assert(ticket)
    }
  }
  private pending(job: RebuildJob) {
    const eligible = this.memory.indexable()
    const done = new Map(
      (
        this.store.db
          .prepare('SELECT memory_id,content_hash FROM embedding_rebuild_items WHERE job_id=?')
          .all(job.id) as { memory_id: string; content_hash: string }[]
      ).map((r) => [r.memory_id, r.content_hash]),
    )
    const pending = eligible.filter(
      (m) =>
        done.get(m.id) !== contentHash(m) ||
        !job.spaceId ||
        !this.store.db
          .prepare('SELECT 1 FROM memory_vectors WHERE memory_id=? AND space_id=?')
          .get(m.id, job.spaceId),
    )
    job.total = eligible.length
    job.processed = eligible.length - pending.length
    return pending
  }
  private async run(job: RebuildJob, config: RouterConfig, signal: AbortSignal) {
    try {
      const selected = encoder(config)!
      while (this.current(job, signal)) {
        const pending = this.pending(job)
        if (!pending.length) {
          // SQLite is synchronous here: no mutation can interleave validation and pointer CAS.
          this.store.db.transaction(() => {
            if (!this.current(job, signal) || this.pending(job).length) return
            if (this.active()?.generationId !== job.expectedActive)
              throw new Error('embedding_active_generation_changed')
            if (!job.spaceId || !job.dimensions) {
              // Empty databases need no paid probe. Wait for content or explicit configured width.
              if (!selected.model.embeddingDimensions) {
                job.state = 'paused'
                job.error = 'embedding_empty_native_dimension_unknown'
                this.saveOwned(job)
                return
              }
              this.space(job, config, selected.model.embeddingDimensions)
            }
            if (!this.current(job, signal)) return
            this.store.set('embedding.previousSpace', this.active()?.spaceId ?? '')
            this.store.set('embedding.active', {
              generationId: job.id,
              encoderRevision: job.encoderRevision,
              modelId: job.modelId,
              dimensions: job.dimensions!,
              spaceId: job.spaceId!,
            } satisfies IndexGeneration)
            this.store.set('embedding.incompatible', '')
            job.state = 'completed'
            job.requiresRetryConfirmation = false
            if (!this.saveOwned(job)) throw new Error('embedding_lease_lost')
            this.pruneRetained()
          })()
          return
        }
        const batch = pending.slice(0, selected.model.embeddingBatchSize ?? 4)
        const admit = this.modelReadGuard(batch)
        job.requiresRetryConfirmation = true
        if (!this.saveOwned(job)) return
        admit()
        const result = await this.router(config, admit).embeddings(
          job.modelId,
          batch.map((m) => m.content),
          signal,
        )
        admit()
        if (!this.current(job, signal)) return
        // The router validates finite/nonzero/consistent dimensions and explicit dimension requests.
        const dimensions = result.vectors[0]?.length
        if (!dimensions || result.vectors.length !== batch.length)
          throw new Error('invalid_embedding_response')
        if (job.dimensions !== undefined && job.dimensions !== dimensions)
          throw new Error('embedding_dimensions_changed')
        this.store.db.transaction(() => {
          if (!this.current(job, signal)) return
          this.space(job, config, dimensions)
          const eligible = new Map(this.memory.indexable().map((m) => [m.id, m]))
          batch.forEach((before, i) => {
            const now = eligible.get(before.id)
            if (!now || contentHash(now) !== contentHash(before)) return
            this.memory.index(now.id, job.spaceId!, result.vectors[i]!)
            this.store.db
              .prepare(
                `INSERT INTO embedding_rebuild_items VALUES(?,?,?)
              ON CONFLICT(job_id,memory_id) DO UPDATE SET content_hash=excluded.content_hash`,
              )
              .run(job.id, now.id, contentHash(now))
          })
          job.requiresRetryConfirmation = false
          this.pending(job)
          if (!this.saveOwned(job)) throw new Error('embedding_lease_lost')
        })()
        // Yield between bounded batches; no timer or retry loops survive a process restart.
        await new Promise<void>((resolve) => setTimeout(resolve, 0))
      }
    } catch (error) {
      // A failed batch transaction may have rolled back its newly registered space.
      if (
        job.spaceId &&
        !this.store.db.prepare('SELECT 1 FROM vector_spaces WHERE id=?').get(job.spaceId)
      )
        delete job.spaceId
      const current = this.job()
      if (
        current?.id !== job.id ||
        current.attemptId !== job.attemptId ||
        current.ownerId !== this.ownerId ||
        current.state !== 'running'
      )
        return
      job.state = 'failed'
      job.error =
        error instanceof ModelError
          ? error.code
          : error instanceof Error &&
              [
                'memory_read_disabled',
                'memory_read_revoked',
                'personalization_disabled',
                'embedding_dimensions_changed',
                'embedding_active_generation_changed',
                'embedding_lease_lost',
                'invalid_embedding_response',
              ].includes(error.message)
            ? error.message
            : 'embedding_rebuild_failed'
      if (error instanceof ModelError && error.status !== undefined)
        job.requiresRetryConfirmation = false
      this.saveOwned(job)
    }
  }
  private space(job: RebuildJob, config: RouterConfig, dimensions: number) {
    if (job.spaceId) return
    const selected = encoder(config)!
    job.dimensions = dimensions
    job.spaceId = this.memory.registerSpace({
      providerId: selected.provider.id,
      modelId: selected.model.id,
      apiModelName: selected.model.apiModelName,
      deployment: selected.provider.baseUrl,
      dimensions,
      preprocessing: 'text-v1',
      normalized: false,
      generation: job.id,
      encoderRevision: job.encoderRevision,
    })
  }
  /** Keep current active, one prior published generation and the current resumable job. */
  private pruneRetained() {
    const keep = new Set(
      [
        this.active()?.spaceId,
        this.store.get('embedding.previousSpace', ''),
        this.job()?.spaceId,
      ].filter(Boolean),
    )
    const spaces = this.store.db
      .prepare(
        "SELECT id,value FROM vector_spaces WHERE json_extract(value,'$.generation') IS NOT NULL",
      )
      .all() as { id: string; value: string }[]
    for (const space of spaces) {
      if (keep.has(space.id)) continue
      const generation = (JSON.parse(space.value) as { generation: string }).generation
      this.store.db.prepare('DELETE FROM vector_spaces WHERE id=?').run(space.id)
      this.store.db.prepare('DELETE FROM embedding_rebuild_items WHERE job_id=?').run(generation)
    }
  }
  private markIncompatible(generationId: string | undefined) {
    if (!generationId) return
    this.store.db
      .transaction(() => {
        if (this.active()?.generationId === generationId)
          this.store.set('embedding.incompatible', generationId)
      })
      .immediate()
  }
  private usable(active: IndexGeneration) {
    return (
      this.active()?.generationId === active.generationId &&
      this.store.get('embedding.incompatible', '') !== active.generationId &&
      encoder(this.store.models)?.revision === active.encoderRevision
    )
  }
  async search(ctx: { sceneId: string; simulation: boolean }, query: string, signal?: AbortSignal) {
    const span = startFlow(this.store, 'memory.search', {
      sceneId: ctx.sceneId,
      mode: ctx.simulation ? 'simulation' : 'real',
    })
    return span.run(async () => {
      const cancelled = () => {
        span.finish('cancelled', { reason: 'request_cancelled', nextStep: 'stop' })
        return []
      }
      const lexical = (reason: string) => {
        const results = this.memory.search(ctx, query)
        span.finish('completed', {
          action: 'lexical',
          reason,
          counts: { resultCount: results.length },
          nextStep: 'use_memory_results',
        })
        return results
      }
      let active: IndexGeneration | undefined
      try {
        if (signal?.aborted) return cancelled()
        if (ctx.simulation) return lexical('simulation_lexical_only')
        active = this.active()
        const config = this.store.models,
          selected = encoder(config)
        if (!active) return lexical('no_active_vector_index')
        if (selected?.revision !== active.encoderRevision) return lexical('vector_revision_changed')
        if (this.store.get('embedding.incompatible', '') === active.generationId)
          return lexical('vector_index_incompatible')
        const result = await this.router(config).embeddings(active.modelId, [query], signal)
        if (signal?.aborted) return cancelled()
        const generation = active
        return this.store.db
          .transaction(() => {
            if (!this.usable(generation)) return lexical('vector_generation_changed')
            if (result.vectors[0]?.length !== generation.dimensions) {
              this.markIncompatible(generation.generationId)
              return lexical('embedding_dimensions_mismatch')
            }
            const results = this.memory.search(ctx, query, undefined, {
              spaceId: generation.spaceId,
              vector: result.vectors[0]!,
            })
            span.finish('completed', {
              action: 'vector',
              counts: { resultCount: results.length },
              nextStep: 'use_memory_results',
            })
            return results
          })
          .immediate()
      } catch (error) {
        if (error instanceof ModelError && error.code === 'embedding_dimensions_mismatch')
          this.markIncompatible(active?.generationId)
        if (signal?.aborted) return cancelled()
        this.store.log('warn', 'memory', 'query_embedding_unavailable')
        try {
          return lexical('query_embedding_unavailable')
        } catch (fallbackError) {
          span.finish(
            'failed',
            { reason: 'memory_retrieval_failed', nextStep: 'stop' },
            fallbackError,
          )
          throw fallbackError
        }
      } finally {
        if (!span.ended)
          span.finish(signal?.aborted ? 'cancelled' : 'failed', {
            reason: signal?.aborted ? 'request_cancelled' : 'memory_retrieval_failed',
            nextStep: 'stop',
          })
      }
    })
  }
  async index(id: string, content: string, signal?: AbortSignal) {
    if (signal?.aborted) return
    let active = this.active()
    let attemptedRevision: string | undefined
    try {
      const config = this.store.models,
        selected = encoder(config)
      attemptedRevision = selected?.revision
      if (
        !selected ||
        (!active && !this.bootstrap) ||
        (active &&
          (active.encoderRevision !== selected.revision ||
            this.store.get('embedding.incompatible', '') === active.generationId))
      )
        return
      const before = this.memory.indexable().find((m) => m.id === id && m.content === content)
      if (!before) return
      const admit = this.modelReadGuard([before])
      admit()
      const result = await this.router(config, admit).embeddings(
        selected.model.id,
        [content],
        signal,
      )
      admit()
      if (signal?.aborted || encoder(this.store.models)?.revision !== selected.revision) return
      this.store.db.transaction(() => {
        const now = this.memory.indexable().find((m) => m.id === id)
        if (!now || contentHash(now) !== contentHash(before)) return
        const current = this.active()
        if (!active) {
          // Bootstrap only a database empty at construction; never silently adopt legacy vectors.
          if (!this.bootstrap || this.job()) return
          if (current) active = current
          else {
            const dimensions = result.vectors[0]?.length
            if (!dimensions) return
            const job: RebuildJob = {
              id: randomUUID(),
              encoderRevision: selected.revision,
              modelId: selected.model.id,
              state: 'completed',
              processed: 1,
              total: 1,
              requiresRetryConfirmation: false,
            }
            this.space(job, config, dimensions)
            active = {
              generationId: job.id,
              encoderRevision: selected.revision,
              modelId: selected.model.id,
              dimensions,
              spaceId: job.spaceId!,
            }
            this.store.set('embedding.active', active)
          }
        } else if (current?.generationId !== active.generationId) return
        if (!this.usable(active)) return
        if (result.vectors[0]?.length !== active.dimensions) {
          this.markIncompatible(active.generationId)
          return
        }
        this.memory.index(id, active.spaceId, result.vectors[0]!)
      })()
    } catch (error) {
      if (error instanceof ModelError && error.code === 'embedding_dimensions_mismatch') {
        const current = this.active()
        this.markIncompatible(
          active?.generationId ??
            (current?.encoderRevision === attemptedRevision ? current?.generationId : undefined),
        )
      }
      this.store.log('warn', 'memory', 'embedding_unavailable')
    }
  }
}
