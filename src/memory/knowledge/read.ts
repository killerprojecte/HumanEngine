import type { MemoryRecord } from '../domain/contracts.ts'
import { isDeepStrictEqual } from 'node:util'
import {
  KnowledgeRefSchema,
  type KnowledgeAuthority,
  type KnowledgeContext,
  type KnowledgeRef,
  type KnowledgeEdge,
  type KnowledgeEdgeView,
  type KnowledgeDependency,
  type KnowledgeReadDependency,
  type KnowledgeQuery,
  type KnowledgeQueryResult,
} from './contracts.ts'
import { conflictKey, knowledgeNodeKey, KNOWLEDGE_PREDICATES } from './registry.ts'
import { KnowledgeStorage } from './storage.ts'
import { knowledgeBackingFingerprint, sameRef, sameSource, intersect } from './qualification.ts'
/** Per-invocation authorized reads; no mutation port or cross-stage result cache. */
export class KnowledgeReader {
  constructor(
    private readonly storage: Pick<KnowledgeStorage, 'rows' | 'get' | 'entity'>,
    private readonly authority: Pick<KnowledgeAuthority, 'current' | 'enabled' | 'assertReady'>,
    private readonly now: () => number,
  ) {}
  current(ctx: KnowledgeContext, ref: KnowledgeRef) {
    const view = this.authority.current(ctx, ref)
    return view &&
      view.record.id === ref.id &&
      view.record.scope.kind === 'scene' &&
      view.record.scope.sceneId === ctx.sceneId &&
      view.proofs.length
      ? view
      : undefined
  }
  project(
    ctx: KnowledgeContext,
    edge: KnowledgeEdge,
    visiting = new Set<string>(),
    stage = { remaining: 512, memo: new Map<string, KnowledgeEdgeView>() },
  ): KnowledgeEdgeView | undefined {
    if (
      ctx.simulation ||
      edge.sceneId !== ctx.sceneId ||
      !this.authority.enabled(ctx) ||
      visiting.has(edge.id)
    )
      return
    if (visiting.size >= 64) throw new Error('knowledge_dependency_limit')
    if (--stage.remaining < 0) throw new Error('knowledge_dependency_limit')
    const backing = this.current(ctx, edge.backing)
    if (!backing || knowledgeBackingFingerprint(backing.record) !== edge.backingFingerprint) return
    const proofs = intersect(edge.proofs, backing.proofs)
    if (!proofs.length) return
    const cached = stage.memo.get(edge.id)
    if (cached) return cached
    const personal = (record: MemoryRecord) =>
      (record.scope.kind === 'scene' && !!record.scope.personId) ||
      !['fact', 'claim', 'term'].includes(record.kind)
    let requiresPersonalization =
      personal(backing.record) || edge.subject.kind === 'person' || edge.object.kind === 'person'
    const path = new Set(visiting)
    path.add(edge.id)
    const dependencies: KnowledgeDependency[] = []
    const dependencyStatuses: KnowledgeEdgeView['dependencyStatuses'] = []
    for (const dep of edge.dependencies) {
      const entity = this.storage.entity(dep.entity.id, ctx.sceneId),
        view = this.current(ctx, dep.entity.backing)
      if (
        !entity ||
        !isDeepStrictEqual(entity, dep.entity) ||
        !view ||
        knowledgeBackingFingerprint(view.record) !== dep.backingFingerprint
      )
        return
      const kept = intersect(dep.proofs, view.proofs),
        origin = this.storage.get(entity.originEdgeId)
      if (!kept.length || !origin) return
      const originView = this.project(ctx, origin, path, stage)
      if (!originView) return
      requiresPersonalization ||= personal(view.record) || originView.requiresPersonalization
      dependencies.push({ ...dep, proofs: kept })
      dependencyStatuses.push({ entityId: dep.entity.id, status: view.record.status })
    }
    const readDependencies: KnowledgeReadDependency[] = []
    for (const dep of edge.readDependencies) {
      if (--stage.remaining < 0) throw new Error('knowledge_dependency_limit')
      const view = this.current(ctx, dep.backing)
      if (!view || knowledgeBackingFingerprint(view.record) !== dep.backingFingerprint) return
      requiresPersonalization ||= personal(view.record)
      let currentProofs = view.proofs
      if (dep.edgeId !== undefined) {
        const origin = this.storage.get(dep.edgeId)
        if (
          !origin ||
          origin.version !== dep.edgeVersion ||
          !sameRef(origin.backing, dep.backing) ||
          origin.backingFingerprint !== dep.backingFingerprint
        )
          return
        const projected = this.project(ctx, origin, path, stage)
        if (!projected) return
        requiresPersonalization ||= projected.requiresPersonalization
        currentProofs = projected.proofs
      }
      const kept = intersect(dep.proofs, currentProofs)
      if (!kept.length) return
      readDependencies.push({ ...dep, proofs: kept })
    }
    const spans = edge.spans.filter((span) =>
      proofs.some((p) => p.sources.some((s) => sameSource(s, span.source))),
    )
    if (!spans.length) return
    const applicability: KnowledgeEdgeView['applicability'] =
      edge.validUntil && this.now() >= Date.parse(edge.validUntil)
        ? 'expired'
        : edge.validFrom && this.now() < Date.parse(edge.validFrom)
          ? 'future'
          : edge.temporalScope === 'past'
            ? 'past'
            : edge.temporalScope === 'future'
              ? 'future'
              : edge.temporalScope === 'unspecified'
                ? 'unspecified'
                : 'current'
    const result = structuredClone({
      ...edge,
      proofs,
      spans,
      dependencies,
      readDependencies,
      applicability,
      backingStatus: backing.record.status,
      requiresPersonalization,
      dependencyStatuses,
    })
    stage.memo.set(edge.id, result)
    return result
  }
  query(ctx: KnowledgeContext, input: KnowledgeQuery): KnowledgeQueryResult {
    this.authority.assertReady()
    const anchor = input.anchor
    if (
      anchor.kind === 'entity' &&
      !ctx.exposedEntities?.some(
        (e) => e.entityId === anchor.entityId && sameRef(e.backingRef, anchor.backingRef),
      )
    )
      throw new Error('knowledge_anchor_not_exposed')
    return this.queryFromRoots(ctx, input, (live) => {
      if (anchor.kind === 'person') return ['person:' + anchor.personId]
      const found = live.some((edge) =>
        [edge.subject, edge.object].some(
          (point) =>
            point.kind === 'entity' &&
            point.entity.id === anchor.entityId &&
            point.entity.originEdgeId === edge.id &&
            sameRef(point.entity.backing, anchor.backingRef),
        ),
      )
      if (!found) throw new Error('knowledge_anchor_unavailable')
      return ['entity:' + anchor.entityId]
    })
  }
  /** First discovery from a trusted actual assertion exposure, not from model-guessed entity IDs. */
  discover(
    ctx: KnowledgeContext,
    exactBacking: KnowledgeRef,
    input: Pick<KnowledgeQuery, 'predicates' | 'depth'>,
  ): KnowledgeQueryResult {
    this.authority.assertReady()
    if (!KnowledgeRefSchema.safeParse(exactBacking).success)
      throw new Error('knowledge_reference_invalid')
    if (!ctx.simulation && this.authority.enabled(ctx) && !this.current(ctx, exactBacking))
      throw new Error('knowledge_backing_unavailable')
    return this.queryFromRoots(ctx, input, (live) =>
      live
        .filter((edge) => sameRef(edge.backing, exactBacking))
        .flatMap((edge) =>
          [edge.subject, edge.object].flatMap((point) =>
            point.kind === 'entity' &&
            point.entity.originEdgeId === edge.id &&
            sameRef(point.entity.backing, exactBacking)
              ? ['entity:' + point.entity.id]
              : [],
          ),
        ),
    )
  }
  private queryFromRoots(
    ctx: KnowledgeContext,
    input: Pick<KnowledgeQuery, 'predicates' | 'depth'>,
    resolveRoots: (live: readonly KnowledgeEdgeView[]) => string[],
  ): KnowledgeQueryResult {
    this.authority.assertReady()
    const empty = (reason?: KnowledgeQueryResult['reason']): KnowledgeQueryResult => ({
      edges: [],
      paths: [],
      supportClosure: { edges: [], records: [] },
      coverage: reason ? 'limited' : 'complete',
      ...(reason ? { reason } : {}),
      interpretation: 'attributed_claims_not_transitive_truth',
    })
    if (ctx.simulation || !this.authority.enabled(ctx)) return empty()
    if (
      ![1, 2].includes(input.depth) ||
      !input.predicates.length ||
      input.predicates.length > 7 ||
      input.predicates.some((p) => !Object.hasOwn(KNOWLEDGE_PREDICATES, p))
    )
      throw new Error('knowledge_query_invalid')
    const rows = this.storage.rows(ctx.sceneId, 201)
    if (rows.length > 200) return empty('scan_limit')
    const stage = { remaining: 512, memo: new Map<string, KnowledgeEdgeView>() }
    let live: KnowledgeEdgeView[]
    try {
      live = rows.flatMap((e) => {
        const p = this.project(ctx, e, new Set(), stage)
        return p ? [p] : []
      })
    } catch (error) {
      if (error instanceof Error && error.message === 'knowledge_dependency_limit')
        return empty('dependency_limit')
      throw error
    }
    const roots = [...new Set(resolveRoots(live))]
    if (roots.length > 12) return empty('edge_limit')
    let frontier = roots.map((node) => ({ node, path: [] as string[] }))
    const pathKeys = new Set<string>()
    const selected = new Map<string, KnowledgeEdgeView>(),
      paths: string[][] = []
    for (let hop = 0; hop < input.depth; hop++) {
      const next: typeof frontier = []
      for (const step of frontier) {
        const matches = live.filter(
          (e) =>
            input.predicates.includes(e.predicate) &&
            (knowledgeNodeKey(e.subject) === step.node || knowledgeNodeKey(e.object) === step.node),
        )
        const buckets = new Set(matches.map(conflictKey))
        const group = live.filter((e) => buckets.has(conflictKey(e)))
        if (group.length > 12) return empty('edge_limit')
        for (const edge of group) selected.set(edge.id, edge)
        for (const edge of matches) {
          if (step.path.includes(edge.id)) continue
          const path = [...step.path, edge.id]
          const pathKey = JSON.stringify(path)
          if (!pathKeys.has(pathKey)) {
            pathKeys.add(pathKey)
            paths.push(path)
          }
          for (const point of [edge.subject, edge.object])
            if (point.kind !== 'literal' && knowledgeNodeKey(point) !== step.node)
              next.push({ node: knowledgeNodeKey(point), path })
        }
        if (selected.size > 40) return empty('edge_limit')
        if (paths.length > 64) return empty('path_limit')
      }
      frontier = next
    }
    const closureEdges = new Map<string, KnowledgeEdgeView>(),
      records = new Map<string, { ref: KnowledgeRef; record: MemoryRecord }>()
    const queue = [...selected.values()],
      visited = new Set<string>()
    const retainRecord = (ref: KnowledgeRef, fingerprint: string) => {
      const key = JSON.stringify(ref)
      if (records.has(key)) return true
      const current = this.current(ctx, ref)
      if (!current || knowledgeBackingFingerprint(current.record) !== fingerprint) return false
      records.set(key, { ref: structuredClone(ref), record: structuredClone(current.record) })
      return records.size <= 128
    }
    while (queue.length) {
      const edge = queue.shift()!
      if (visited.has(edge.id)) continue
      visited.add(edge.id)
      if (visited.size > 40 || !retainRecord(edge.backing, edge.backingFingerprint))
        return empty('dependency_limit')
      const originIds: string[] = []
      for (const dep of edge.dependencies) {
        if (!retainRecord(dep.entity.backing, dep.backingFingerprint))
          return empty('dependency_limit')
        originIds.push(dep.entity.originEdgeId)
      }
      for (const dep of edge.readDependencies) {
        if (!retainRecord(dep.backing, dep.backingFingerprint)) return empty('dependency_limit')
        if (dep.edgeId !== undefined) originIds.push(dep.edgeId)
      }
      for (const id of originIds) {
        const origin = stage.memo.get(id)
        if (!origin) return empty('dependency_limit')
        if (!selected.has(id)) closureEdges.set(id, origin)
        if (selected.size + closureEdges.size > 40) return empty('dependency_limit')
        if (!visited.has(id)) queue.push(origin)
      }
    }
    return {
      edges: [...selected.values()],
      paths,
      supportClosure: { edges: [...closureEdges.values()], records: [...records.values()] },
      coverage: 'complete',
      interpretation: 'attributed_claims_not_transitive_truth',
    }
  }
}
