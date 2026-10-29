import { isDeepStrictEqual } from 'node:util'
import type { MemoryContext, MemoryRecord } from '../domain/contracts.ts'
import type {
  RelationContext,
  RelationRecord,
  RelationAssertionReference,
} from '../domain/relations.ts'
import { ModelMemoryExposure, type ModelMemoryContext } from './model-exposure.ts'
export interface RelationEvidence {
  id: string
  version: number
  predicate: 'participates_in'
  modality: RelationRecord['modality']
  temporalScope: RelationRecord['temporalScope']
  personId: string
  target: { entityId: string; kind: 'plan' | 'event'; title: string }
  polarity: 'positive' | 'negative'
  conditions: string[]
  status: RelationRecord['status']
  admission: RelationRecord['admission']
  occurredAt?: string
  validFrom?: string
  validUntil?: string
  epistemicKind?: MemoryRecord['epistemicKind']
  applicability: 'current' | 'future' | 'expired' | 'past' | 'unspecified'
  requiredMemoryIds: string[]
  sources: MemoryRecord['sources']
}
export type RelationMemoryRecord = MemoryRecord & {
  relationEvidence: RelationEvidence[]
  relationCoverage: 'complete'
}
interface RelationMemoryPort {
  list(ctx: MemoryContext): MemoryRecord[]
  relations: {
    list(
      ctx: RelationContext,
      options?: { personId?: string; limit?: number },
    ): { items: RelationRecord[]; coverage: 'complete' | 'limited' }
  }
}
interface PendingRelation {
  prepared: MemoryRecord
  body: RelationMemoryRecord
  relations: RelationRecord[]
  personId?: string
}
const freeze = <T>(value: T): T => {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze)
    Object.freeze(value)
  }
  return value
}
const relationContext = (ctx: ModelMemoryContext): RelationContext => ({
  sceneId: ctx.scene.id,
  simulation: ctx.mode === 'simulation',
  provenance: 'contextual',
})
const memoryContext = (ctx: ModelMemoryContext): MemoryContext => ({
  sceneId: ctx.scene.id,
  simulation: ctx.mode === 'simulation',
})
const applicability = (edge: RelationRecord, now: number): RelationEvidence['applicability'] =>
  edge.temporalScope === 'past'
    ? 'past'
    : edge.temporalScope === 'future'
      ? 'future'
      : edge.validFrom && Date.parse(edge.validFrom) > now
        ? 'future'
        : edge.validUntil && Date.parse(edge.validUntil) <= now
          ? 'expired'
          : edge.temporalScope === 'current'
            ? 'current'
            : 'unspecified'
/** Only selected untouched delivery objects commit dependencies. Candidate edges are qualified evidence, never truth. */
export class RelationExposure {
  private readonly pending = new WeakMap<ModelMemoryContext, WeakMap<object, PendingRelation>>()
  private readonly exposed = new WeakMap<
    ModelMemoryContext,
    Map<string, RelationAssertionReference>
  >()
  private readonly entityChecks = new WeakMap<ModelMemoryContext, Map<string, () => boolean>>()
  private readonly delivered = new WeakMap<ModelMemoryContext, PendingRelation[]>()
  constructor(
    private readonly memory: RelationMemoryPort,
    private readonly memoryExposure: ModelMemoryExposure,
    private readonly enabled: (ctx: ModelMemoryContext) => boolean,
    private readonly now: () => number = Date.now,
  ) {}
  query(ctx: ModelMemoryContext, personId?: string): RelationMemoryRecord[] {
    if (ctx.signal.aborted || ctx.mode === 'simulation' || !this.enabled(ctx)) return []
    const result = this.memory.relations.list(relationContext(ctx), { personId, limit: 40 })
    if (result.coverage !== 'complete') throw new Error('relation_query_limited')
    const relations = result.items.filter((item) => item.status !== 'rejected')
    const ids = new Set(relations.flatMap((item) => [item.backing.id, item.target.backing.id]))
    if (ids.size > 12) throw new Error('relation_query_limited')
    const visible = new Map(
      this.memory.list(memoryContext(ctx)).map((record) => [record.id, record]),
    )
    const records = [...ids].map((id) => visible.get(id))
    if (records.some((record) => !record)) throw new Error('relation_snapshot_unavailable')
    const prepared = this.memoryExposure.prepare(ctx, records as MemoryRecord[])
    if (prepared.length !== ids.size) throw new Error('relation_snapshot_unavailable')
    const pending = this.pending.get(ctx) ?? new WeakMap<object, PendingRelation>()
    this.pending.set(ctx, pending)
    return prepared.map((record) => {
      const linked = relations.filter(
        (item) => item.backing.id === record.id || item.target.backing.id === record.id,
      )
      const evidence = linked.map((item): RelationEvidence => ({
        id: item.id,
        version: item.version,
        predicate: item.predicate,
        modality: item.modality,
        temporalScope: item.temporalScope,
        personId: item.personId,
        target: { entityId: item.target.id, kind: item.target.kind, title: item.target.title },
        polarity: item.polarity,
        conditions: [...item.conditions],
        status: item.status,
        admission: item.admission,
        ...(item.occurredAt ? { occurredAt: item.occurredAt } : {}),
        ...(item.validFrom ? { validFrom: item.validFrom } : {}),
        ...(item.validUntil ? { validUntil: item.validUntil } : {}),
        ...(item.epistemicKind ? { epistemicKind: item.epistemicKind } : {}),
        applicability: applicability(item, this.now()),
        requiredMemoryIds: [...new Set([item.backing.id, item.target.backing.id])],
        sources: structuredClone(item.proofs.flatMap((proof) => proof.sources)),
      }))
      const body = freeze({
        ...structuredClone(record),
        relationEvidence: evidence,
        relationCoverage: 'complete' as const,
      })
      pending.set(body, { prepared: record, body, relations: structuredClone(linked), personId })
      return body
    })
  }
  deliver(ctx: ModelMemoryContext, value: readonly unknown[]): RelationMemoryRecord[] {
    if (ctx.signal.aborted || ctx.mode === 'simulation' || !this.enabled(ctx)) return []
    const pending = this.pending.get(ctx)
    if (!pending) return []
    const selected: PendingRelation[] = []
    for (const valueItem of value) {
      if (!valueItem || typeof valueItem !== 'object') return []
      const item = pending.get(valueItem)
      if (!item || !isDeepStrictEqual(valueItem, item.body)) return []
      selected.push(item)
    }
    const ids = new Set(selected.map((item) => item.body.id))
    if (
      ids.size !== selected.length ||
      selected.some((item) =>
        item.body.relationEvidence.some((edge) =>
          edge.requiredMemoryIds.some((id) => !ids.has(id)),
        ),
      )
    )
      return []
    if (!this.valid(ctx, selected)) return []
    const exposed = this.exposed.get(ctx) ?? new Map<string, RelationAssertionReference>()
    const checks = this.entityChecks.get(ctx) ?? new Map<string, () => boolean>()
    for (const item of selected)
      for (const edge of item.relations) {
        const previous = exposed.get(edge.target.id)
        if (
          previous &&
          (!isDeepStrictEqual(previous, edge.target.backing) || !checks.get(edge.target.id)?.())
        ) {
          ctx.validations.push(() => false)
          return []
        }
      }
    const committed = this.memoryExposure.commit(
      ctx,
      selected.map((item) => item.prepared),
    )
    if (committed.length !== selected.length) return []
    for (const item of selected)
      for (const edge of item.relations)
        if (!exposed.has(edge.target.id))
          exposed.set(edge.target.id, freeze(structuredClone(edge.target.backing)))
    for (const item of selected)
      for (const edge of item.relations)
        if (!checks.has(edge.target.id)) checks.set(edge.target.id, () => this.valid(ctx, selected))
    this.entityChecks.set(ctx, checks)
    this.delivered.set(ctx, [...(this.delivered.get(ctx) ?? []), ...selected])
    this.exposed.set(ctx, exposed)
    ctx.validations.push(() => this.valid(ctx, selected))
    return selected.map((item) => item.body)
  }
  /** Trusted queue capability; detached from normal reply-turn cancellation, never model supplied. */
  captureLearningGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    const selected = this.delivered.get(ctx)
    if (!selected?.length || !this.exposed.get(ctx)?.size) return undefined
    const detached: ModelMemoryContext = {
      scene: { id: ctx.scene.id },
      mode: ctx.mode,
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    const snapshots = structuredClone(selected)
    freeze(snapshots)
    return () => this.valid(detached, snapshots)
  }
  readExposedEntities(ctx: ModelMemoryContext) {
    if (ctx.signal.aborted || !this.enabled(ctx)) return []
    if ([...(this.entityChecks.get(ctx)?.values() ?? [])].some((check) => !check())) return []
    return [...(this.exposed.get(ctx)?.entries() ?? [])].map(([entityId, backingRef]) => ({
      entityId,
      backingRef: { ...backingRef },
    }))
  }
  private valid(ctx: ModelMemoryContext, selected: readonly PendingRelation[]): boolean {
    if (ctx.signal.aborted || !this.enabled(ctx)) return false
    const current = new Map<string, RelationRecord>()
    for (const personId of new Set(selected.map((item) => item.personId))) {
      const result = this.memory.relations.list(relationContext(ctx), { personId, limit: 40 })
      if (result.coverage !== 'complete') return false
      for (const item of result.items) current.set(item.id, item)
    }
    const targets = new Set(
      selected.flatMap((item) => item.relations.map((edge) => edge.target.id)),
    )
    const captured = new Set(selected.flatMap((item) => item.relations.map((edge) => edge.id)))
    if (
      [...current.values()].some(
        (edge) =>
          edge.status !== 'rejected' && targets.has(edge.target.id) && !captured.has(edge.id),
      )
    )
      return false
    return selected.every((item) =>
      item.relations.every(
        (edge) =>
          isDeepStrictEqual(current.get(edge.id), edge) &&
          item.body.relationEvidence.find((value) => value.id === edge.id)?.applicability ===
            applicability(edge, this.now()),
      ),
    )
  }
}
