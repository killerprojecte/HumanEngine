import { isDeepStrictEqual } from 'node:util'
import type {
  KnowledgeContext,
  KnowledgeQuery,
  KnowledgeQueryResult,
  KnowledgeRef,
} from '../knowledge/contracts.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'

export interface KnowledgeStateEvidence {
  stateKind: 'knowledge_graph'
  page: KnowledgeQueryResult
}
export interface KnowledgeEntityExposure {
  entityId: string
  backingRef: KnowledgeRef
}
export interface KnowledgeExposurePort {
  query(ctx: KnowledgeContext, input: KnowledgeQuery): KnowledgeQueryResult
  discover?(
    ctx: KnowledgeContext,
    ref: KnowledgeRef,
    input: Pick<KnowledgeQuery, 'predicates' | 'depth'>,
  ): KnowledgeQueryResult
  policyIdentity?(ctx: ModelMemoryContext): string | number
  allowed(ctx: ModelMemoryContext): boolean
}
interface Prepared {
  exposure: StateExposure<KnowledgeStateEvidence, undefined>
  values: readonly KnowledgeStateEvidence[]
}
const freeze = <T>(value: T): T => {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze)
    Object.freeze(value)
  }
  return value
}
const same = (a: KnowledgeRef, b: KnowledgeRef) =>
  a.id === b.id && a.version === b.version && a.deletionEpoch === b.deletionEpoch
/** Whole query authority is granted only by actual delivery, never by a model-supplied entity ref. */
export class KnowledgeExposure {
  private readonly prepared = new WeakMap<ModelMemoryContext, Prepared[]>()
  constructor(private readonly port: KnowledgeExposurePort) {}
  query(ctx: ModelMemoryContext, input: KnowledgeQuery): readonly KnowledgeStateEvidence[] {
    if (ctx.mode !== 'real' || ctx.signal.aborted || !this.port.allowed(ctx))
      return Object.freeze([])
    const query = freeze(structuredClone(input))
    const prior = this.captureGuard(ctx)
    if (prior && !prior()) return Object.freeze([])
    const entities = this.readEntities(ctx)
    if (query.anchor.kind === 'entity') {
      const anchor = query.anchor
      if (
        !entities.some(
          (e) => e.entityId === anchor.entityId && same(e.backingRef, anchor.backingRef),
        )
      )
        return Object.freeze([])
    }
    return this.prepareWhole(ctx, query.depth, prior, (c) =>
      this.port.query(
        {
          sceneId: c.scene.id,
          simulation: c.mode !== 'real',
          provenance: 'contextual',
          exposedEntities: entities,
        },
        query,
      ),
    )
  }
  queryFromBacking(
    ctx: ModelMemoryContext,
    ref: KnowledgeRef,
    input: Pick<KnowledgeQuery, 'predicates' | 'depth'>,
    anchorValid: () => boolean,
  ): readonly KnowledgeStateEvidence[] {
    const valid = () => {
      try {
        return typeof anchorValid === 'function' && anchorValid() === true
      } catch {
        return false
      }
    }
    if (
      ctx.mode !== 'real' ||
      ctx.signal.aborted ||
      !this.port.allowed(ctx) ||
      !this.port.discover ||
      !valid()
    )
      return Object.freeze([])
    const prior = this.captureGuard(ctx)
    if (prior && !prior()) return Object.freeze([])
    const anchor = freeze(structuredClone(ref)),
      query = freeze(structuredClone(input))
    const entities = this.readEntities(ctx)
    return this.prepareWhole(
      ctx,
      query.depth,
      () => valid() && (!prior || prior()),
      (c) =>
        this.port.discover!(
          {
            sceneId: c.scene.id,
            simulation: c.mode !== 'real',
            provenance: 'contextual',
            exposedEntities: entities,
          },
          anchor,
          query,
        ),
    )
  }
  private prepareWhole(
    ctx: ModelMemoryContext,
    depth: KnowledgeQuery['depth'],
    prior: (() => boolean) | undefined,
    load: (ctx: ModelMemoryContext) => KnowledgeQueryResult,
  ): readonly KnowledgeStateEvidence[] {
    const snapshot = (c: ModelMemoryContext): KnowledgeStateEvidence => {
      const page = load(c)
      if (
        page.interpretation !== 'attributed_claims_not_transitive_truth' ||
        !['complete', 'limited'].includes(page.coverage) ||
        !page.supportClosure ||
        !Array.isArray(page.supportClosure.edges) ||
        !Array.isArray(page.supportClosure.records) ||
        page.edges.length + page.supportClosure.edges.length > 40 ||
        page.supportClosure.records.length > 128 ||
        page.paths.length > 64 ||
        [...page.edges, ...page.supportClosure.edges].some((e) => e.sceneId !== c.scene.id) ||
        page.supportClosure.records.some(
          (r) =>
            r.ref.id !== r.record.id ||
            r.record.scope.kind !== 'scene' ||
            r.record.scope.sceneId !== c.scene.id,
        ) ||
        page.paths.some(
          (path) => path.length > depth || path.some((id) => !page.edges.some((e) => e.id === id)),
        )
      )
        throw new Error('knowledge_exposure_page_invalid')
      return { stateKind: 'knowledge_graph', page }
    }
    const first = snapshot(ctx)
    const policyIdentity = this.port.policyIdentity?.(ctx)
    const exposure = new StateExposure<KnowledgeStateEvidence, undefined>({
      query: () => [first],
      valid: (c, values) =>
        this.port.allowed(c) &&
        this.port.policyIdentity?.(c) === policyIdentity &&
        (!prior || prior()) &&
        values.length === 1 &&
        isDeepStrictEqual(snapshot(c), values[0]),
      sourceIds: (values) => [
        ...new Set(
          values.flatMap((v) => [
            ...v.page.supportClosure.records.flatMap((item) =>
              item.record.sources.map((source) => source.messageId),
            ),
            ...[...v.page.edges, ...v.page.supportClosure.edges].flatMap((e) => [
              ...e.proofs.flatMap((p) => p.sources.map((s) => s.messageId)),
              ...e.spans.map((s) => s.source.messageId),
              ...(e.readDependencies ?? []).flatMap((d) =>
                d.proofs.flatMap((p) => p.sources.map((s) => s.messageId)),
              ),
              ...e.dependencies.flatMap((d) =>
                d.proofs.flatMap((p) => p.sources.map((s) => s.messageId)),
              ),
            ]),
          ]),
        ),
      ],
    })
    const values = exposure.query(ctx, undefined)
    if (values.length)
      this.prepared.set(ctx, [...(this.prepared.get(ctx) ?? []), { exposure, values }])
    return values
  }
  deliver(ctx: ModelMemoryContext, values: readonly unknown[]): readonly KnowledgeStateEvidence[] {
    if (!values.length) return Object.freeze([])
    const entry = this.prepared.get(ctx)?.find((p) => p.values[0] === values[0])
    return entry?.exposure.deliver(ctx, values) ?? Object.freeze([])
  }
  readDelivered(ctx: ModelMemoryContext): readonly KnowledgeStateEvidence[] {
    const entries = this.prepared.get(ctx) ?? []
    const guard = this.captureGuard(ctx)
    if (guard && !guard()) return Object.freeze([])
    return Object.freeze(entries.flatMap((p) => p.exposure.readDelivered(ctx)))
  }
  readEdges(
    ctx: ModelMemoryContext,
  ): readonly { id: string; version: number; backing: KnowledgeRef }[] {
    const edges = new Map<string, { id: string; version: number; backing: KnowledgeRef }>()
    for (const receipt of this.readDelivered(ctx))
      for (const edge of [...receipt.page.edges, ...receipt.page.supportClosure.edges]) {
        const snapshot = {
          id: edge.id,
          version: edge.version,
          backing: structuredClone(edge.backing),
        }
        const prior = edges.get(edge.id)
        if (prior && !isDeepStrictEqual(prior, snapshot)) return Object.freeze([])
        edges.set(edge.id, snapshot)
      }
    return freeze([...edges.values()])
  }
  readEntities(ctx: ModelMemoryContext): readonly KnowledgeEntityExposure[] {
    const refs = new Map<string, KnowledgeEntityExposure>()
    for (const receipt of this.readDelivered(ctx))
      for (const edge of receipt.page.edges)
        for (const endpoint of [edge.subject, edge.object]) {
          if (endpoint.kind !== 'entity') continue
          const entity = endpoint.entity
          if (entity.sceneId !== ctx.scene.id) throw new Error('knowledge_exposure_scope_invalid')
          const prior = refs.get(entity.id)
          if (prior && !same(prior.backingRef, entity.backing)) return Object.freeze([])
          if (!prior)
            refs.set(entity.id, {
              entityId: entity.id,
              backingRef: structuredClone(entity.backing),
            })
        }
    return freeze([...refs.values()])
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    const guards = (this.prepared.get(ctx) ?? []).flatMap((p) => {
      const guard = p.exposure.captureGuard(ctx)
      return guard ? [guard] : []
    })
    return guards.length ? () => guards.every((g) => g()) : undefined
  }
}
