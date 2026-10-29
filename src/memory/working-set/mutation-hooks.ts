import { randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'
import {
  WorkingSetReferenceSchema,
  WorkingSetSnapshotSchema,
  type WorkingSetContext,
  type WorkingSetReference,
  type WorkingSetSnapshot,
} from './contracts.ts'
import type { WorkingSetStorage } from './storage.ts'
import { entityKey, referenceKey, sameSelection } from './policy.ts'

type EntityReference = Pick<WorkingSetReference, 'kind' | 'id'>
export interface WorkingSetMutationPorts {
  assertReady(): void
  enabled(): boolean
  /** Reader enrollment in this same transaction; no model/network/async work. */
  beforeWrite(): void
  storage: Pick<WorkingSetStorage, 'ensure' | 'item' | 'job' | 'putJob'>
  /** ONE exact batch, with no Memory.list/capture/refresh or recursive revision writes.
   * dependencyKey includes all source heads and relevant plan mutations/protection. */
  resolveCommitted(
    ctx: WorkingSetContext,
    refs: readonly WorkingSetReference[],
  ): readonly WorkingSetSnapshot[]
  /** Existing core invalidation retains pin policy; these operations must be synchronous. */
  invalidate(sceneId: string, refs: readonly EntityReference[]): void
  terminalInvalidate(sceneId: string, refs: readonly EntityReference[]): void
  /** Indexed metadata pages across pins/items/jobs/receipts, sorted unique scene IDs > afterScene.
   * No read-page prefix limit: terminal cleanup continues until an empty/short page. */
  affectedAssertionScenes(
    assertionId: string,
    afterScene: string | undefined,
    limit: number,
  ): readonly string[]
  now?(): number
}
export interface WorkingSetMutationResult {
  queued: number
  unavailable: number
  coverage: 'complete' | 'limited' | 'disabled'
}
const MAX_BATCH = 40
const sync = <T>(value: T): T => {
  if (value && typeof value === 'object' && 'then' in value)
    throw new Error('working_set_mutation_async_denied')
  return value
}

/**
 * Trusted authoritative lifecycle adapter, never a model tool or another authority.
 * Caller supplies the existing SQLite transaction; no new transaction, drain, lease
 * completion, source replay or body persistence is introduced here.
 */
export class WorkingSetMutationHooks {
  constructor(
    private readonly db: Database.Database,
    private readonly ports: WorkingSetMutationPorts,
  ) {}

  private ready(): void {
    if (!this.db.inTransaction) throw new Error('working_set_mutation_transaction_required')
    sync(this.ports.assertReady())
  }
  private references(input: readonly WorkingSetReference[]): WorkingSetReference[] {
    if (!Array.isArray(input) || input.length > MAX_BATCH)
      throw new Error('working_set_mutation_limit')
    const refs = new Map<string, WorkingSetReference>()
    for (const raw of input) {
      const ref = WorkingSetReferenceSchema.parse(raw),
        key = entityKey(ref),
        prior = refs.get(key)
      if (prior && referenceKey(prior) !== referenceKey(ref))
        throw new Error('working_set_mutation_conflict')
      refs.set(key, ref)
    }
    return [...refs.values()]
  }
  private enqueue(
    ctx: WorkingSetContext,
    refs: readonly WorkingSetReference[],
  ): WorkingSetMutationResult {
    if (ctx.simulation || !sync(this.ports.enabled()))
      return { queued: 0, unavailable: 0, coverage: 'disabled' }
    if (!refs.length) return { queued: 0, unavailable: 0, coverage: 'complete' }
    const wanted = new Set(refs.map(referenceKey))
    const resolved = sync(this.ports.resolveCommitted(ctx, refs))
    if (!Array.isArray(resolved) || resolved.length > MAX_BATCH)
      throw new Error('working_set_mutation_authority_invalid')
    const snapshots = new Map<string, WorkingSetSnapshot>()
    for (const raw of resolved) {
      const snapshot = WorkingSetSnapshotSchema.parse(raw),
        key = referenceKey(snapshot.ref)
      if (snapshot.sceneId !== ctx.sceneId || !wanted.has(key) || snapshots.has(key))
        throw new Error('working_set_mutation_authority_invalid')
      snapshots.set(key, snapshot)
    }
    const now = (this.ports.now ?? Date.now)()
    if (!Number.isSafeInteger(now) || now < 0) throw new Error('working_set_mutation_clock_invalid')
    let queued = 0
    for (const snapshot of snapshots.values()) {
      const item = this.ports.storage.item(ctx.sceneId, snapshot.ref)
      if (item && sameSelection(item.snapshot, snapshot)) continue
      const job = this.ports.storage.job(ctx.sceneId, snapshot.ref)
      if (job && sameSelection(job.snapshot, snapshot)) continue
      sync(this.ports.beforeWrite())
      this.ports.storage.ensure()
      this.ports.storage.putJob({
        schemaVersion: 1,
        snapshot,
        generation: randomUUID(),
        enqueuedAt: now,
        lease: null,
      })
      queued++
    }
    sync(this.ports.assertReady())
    return {
      queued,
      unavailable: wanted.size - snapshots.size,
      coverage: snapshots.size < wanted.size ? 'limited' : 'complete',
    }
  }
  /** Final committed ref/proof callback, before the caller commits its enclosing transaction. */
  committed(
    ctx: WorkingSetContext,
    input: readonly WorkingSetReference[],
  ): WorkingSetMutationResult {
    this.ready()
    if (!ctx.sceneId || typeof ctx.simulation !== 'boolean')
      throw new Error('working_set_mutation_context_invalid')
    return this.enqueue(ctx, this.references(input))
  }
  /** Source head changed even if assertion version did not. Invalidate even while OFF;
   * then enqueue only exact eligible survivors. Ordinary invalidation never clears pin intent. */
  sourceEdited(ctx: WorkingSetContext, input: readonly WorkingSetReference[]) {
    this.ready()
    if (!ctx.sceneId || typeof ctx.simulation !== 'boolean')
      throw new Error('working_set_mutation_context_invalid')
    const refs = this.references(input)
    if (ctx.simulation)
      return { queued: 0, unavailable: 0, coverage: 'disabled' as const, invalidatedTargets: 0 }
    if (refs.length) sync(this.ports.invalidate(ctx.sceneId, refs))
    return { ...this.enqueue(ctx, refs), invalidatedTargets: refs.length }
  }
  /** Terminal authority only: clear pin policy in EVERY metadata scene, not one visible page. */
  terminalAssertionDeleted(assertionId: string): { scenes: number } {
    return this.invalidateAcrossScenes(assertionId, true)
  }
  /** Privacy/source invalidation removes derived metadata, never the independent pin intent. */
  invalidateAssertion(assertionId: string): { scenes: number } {
    return this.invalidateAcrossScenes(assertionId, false)
  }
  private invalidateAcrossScenes(assertionId: string, terminal: boolean): { scenes: number } {
    this.ready()
    if (!assertionId || assertionId.length > 512)
      throw new Error('working_set_mutation_assertion_invalid')
    let after: string | undefined,
      scenes = 0
    for (;;) {
      const page = sync(this.ports.affectedAssertionScenes(assertionId, after, MAX_BATCH))
      if (
        !Array.isArray(page) ||
        page.length > MAX_BATCH ||
        page.some(
          (scene, index) =>
            typeof scene !== 'string' ||
            !scene ||
            scene.length > 512 ||
            (index
              ? Buffer.compare(Buffer.from(scene), Buffer.from(page[index - 1]!)) <= 0
              : after !== undefined && Buffer.compare(Buffer.from(scene), Buffer.from(after)) <= 0),
        )
      )
        throw new Error('working_set_mutation_scene_page_invalid')
      for (const scene of page) {
        const refs: EntityReference[] = [{ kind: 'assertion', id: assertionId }]
        if (terminal) sync(this.ports.terminalInvalidate(scene, refs))
        else sync(this.ports.invalidate(scene, refs))
        scenes++
      }
      if (page.length < MAX_BATCH) break
      after = page.at(-1)!
    }
    sync(this.ports.assertReady())
    return { scenes }
  }
}
