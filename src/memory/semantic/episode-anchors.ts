import { isDeepStrictEqual } from 'node:util'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { KnowledgeService } from '../knowledge/service.ts'
import type {
  KnowledgeContext,
  KnowledgeEndpoint,
  KnowledgeEdgeView,
  KnowledgeEntity,
  KnowledgeQueryResult,
} from '../knowledge/contracts.ts'
import {
  semanticHash,
  SemanticAnchorQualificationSchema,
  type SemanticAnchorQualification,
  type SemanticAssertion,
  type SemanticAnchorDependency,
  type SemanticContext,
  type SemanticRef,
  type SemanticSelection,
} from './contracts.ts'
export interface SemanticEpisodeGroup {
  anchor: { id: string; version: number }
  anchorMembershipKey: string
  parents: SemanticSelection[]
  anchorDependencies: SemanticAnchorDependency[]
  anchorQualifications: SemanticAnchorQualification[]
  /** Qualification only; do not serialize extra graph prose without the original parent closure. */
  edges: KnowledgeEdgeView[]
  interpretation: 'grouped_declarations_not_observed_event'
}
export interface SemanticEpisodeResult {
  groups: SemanticEpisodeGroup[]
  coverage: 'complete' | 'limited'
  reason?: 'knowledge_limited' | 'declaration_unavailable' | 'group_limit'
}
export interface SemanticEpisodePorts {
  knowledge: Pick<KnowledgeService, 'query'>
  current(ctx: SemanticContext, ref: SemanticRef): SemanticAssertion | undefined
  anchorCurrent(
    ctx: SemanticContext,
    ref: SemanticRef,
  ): { record: MemoryRecord; proofs: KnowledgeEdgeView['proofs'] } | undefined
  /** Exact authorized trigger only; may be an internal marker, never becomes a model declaration. */
  triggerRecord?(ctx: SemanticContext, ref: SemanticRef): MemoryRecord | undefined
}
const same = (a: SemanticRef, b: SemanticRef) => isDeepStrictEqual(a, b)
const uniqueRefs = (refs: readonly SemanticRef[]) => [
  ...new Map(refs.map((ref) => [JSON.stringify(ref), ref])).values(),
]
/** Trusted bounded graph discovery. Identity comes from an authorized exact entity, never its title. */
export class SemanticEpisodeAnchors {
  constructor(private readonly ports: SemanticEpisodePorts) {}
  private queryContext(ctx: SemanticContext): KnowledgeContext {
    return { ...ctx, provenance: 'contextual' }
  }
  discover(ctx: SemanticContext, trigger: SemanticRef): SemanticEpisodeResult {
    const empty = (reason?: SemanticEpisodeResult['reason']): SemanticEpisodeResult => ({
      groups: [],
      coverage: reason ? 'limited' : 'complete',
      ...(reason ? { reason } : {}),
    })
    if (ctx.simulation) return empty()
    const current = this.ports.current(ctx, trigger)
    const record =
      current?.record ??
      this.ports.anchorCurrent(ctx, trigger)?.record ??
      this.ports.triggerRecord?.(ctx, trigger)
    if (
      !record ||
      record.id !== trigger.id ||
      record.scope.kind !== 'scene' ||
      record.scope.sceneId !== ctx.sceneId ||
      !record.scope.personId
    )
      return empty()
    const first = this.ports.knowledge.query(this.queryContext(ctx), {
      anchor: { kind: 'person', personId: record.scope.personId },
      predicates: ['participates_in'],
      depth: 1,
    })
    if (first.coverage !== 'complete') return empty('knowledge_limited')
    const entities = new Map<string, KnowledgeEntity>()
    for (const edge of first.edges) {
      if (
        edge.predicate !== 'participates_in' ||
        edge.subject.kind !== 'person' ||
        edge.subject.personId !== record.scope.personId ||
        edge.object.kind !== 'entity' ||
        edge.object.entity.kind !== 'event'
      )
        continue
      const entity = edge.object.entity,
        previous = entities.get(entity.id)
      if (previous && !isDeepStrictEqual(previous, entity)) return empty('knowledge_limited')
      entities.set(entity.id, entity)
    }
    if (entities.size > 4) return empty('group_limit')
    const groups: SemanticEpisodeGroup[] = []
    for (const entity of entities.values()) {
      const page = this.ports.knowledge.query(
        {
          ...this.queryContext(ctx),
          exposedEntities: [{ entityId: entity.id, backingRef: entity.backing }],
        },
        {
          anchor: { kind: 'entity', entityId: entity.id, backingRef: entity.backing },
          predicates: ['participates_in'],
          depth: 1,
        },
      )
      if (page.coverage !== 'complete') return empty('knowledge_limited')
      const group = this.group(ctx, entity, page)
      if (group === 'limited') return empty('declaration_unavailable')
      if (
        group &&
        (group.parents.some((p) => same(p.ref, trigger)) ||
          (record.internalUse && group.edges.some((edge) => same(edge.backing, trigger))))
      )
        groups.push(group)
    }
    return { groups, coverage: 'complete' }
  }
  private group(
    ctx: SemanticContext,
    entity: KnowledgeEntity,
    page: KnowledgeQueryResult,
  ): SemanticEpisodeGroup | 'limited' | undefined {
    const declarations = page.edges.filter(
      (edge) =>
        edge.predicate === 'participates_in' &&
        edge.object.kind === 'entity' &&
        isDeepStrictEqual(edge.object.entity, entity),
    )
    if (declarations.length < 2) return
    const all = new Map(
      [...page.edges, ...page.supportClosure.edges].map((edge) => [edge.id, edge]),
    )
    const origin = all.get(entity.originEdgeId)
    if (!origin || !same(origin.backing, entity.backing)) return 'limited'
    const closure = new Map<string, KnowledgeEdgeView>()
    const pending = [...declarations]
    while (pending.length) {
      const edge = pending.pop()!
      if (closure.has(edge.id)) continue
      if (closure.size >= 40) return 'limited'
      closure.set(edge.id, edge)
      for (const id of [
        ...edge.dependencies.map((d) => d.entity.originEdgeId),
        ...edge.readDependencies.flatMap((d) => (d.edgeId ? [d.edgeId] : [])),
      ]) {
        const required = all.get(id)
        if (!required) return 'limited'
        pending.push(required)
      }
    }
    const refs = uniqueRefs(
      [...closure.values()].flatMap((edge) => [
        edge.backing,
        ...edge.dependencies.map((d) => d.entity.backing),
        ...edge.readDependencies.map((d) => d.backing),
      ]),
    )
    const records = new Map<
      string,
      { selection: SemanticSelection; assertion: SemanticAssertion }
    >()
    for (const ref of refs) {
      const value = this.ports.current(ctx, ref)
      if (!value) {
        const record =
          this.ports.anchorCurrent(ctx, ref)?.record ?? this.ports.triggerRecord?.(ctx, ref)
        if (record?.id === ref.id && record.internalUse) continue
        return 'limited'
      }
      if (value.record.internalUse) continue
      if (value.derived) return 'limited'
      if (
        value.record.id !== ref.id ||
        value.record.scope.kind !== 'scene' ||
        value.record.scope.sceneId !== ctx.sceneId ||
        value.rootCoverage !== 'complete' ||
        !value.proofs.length
      )
        return 'limited'
      const old = records.get(ref.id)
      if (old && !same(old.selection.ref, ref)) return 'limited'
      records.set(ref.id, {
        selection: { ref: structuredClone(ref), dependencyKey: value.dependencyKey },
        assertion: value,
      })
    }
    if (records.size < 2 || records.size > 12) return 'limited'
    // A marker alone is not an original participant declaration. Each edge must retain one.
    for (const edge of declarations) {
      if (edge.subject.kind !== 'person') return 'limited'
      const personId = edge.subject.personId
      const direct = uniqueRefs([
        edge.backing,
        ...edge.dependencies.map((d) => d.entity.backing),
        ...edge.readDependencies.map((d) => d.backing),
      ])
      if (
        !direct.some((ref) => {
          const value = records.get(ref.id)
          return (
            value &&
            same(value.selection.ref, ref) &&
            value.assertion.record.scope.kind === 'scene' &&
            value.assertion.record.scope.personId === personId
          )
        })
      )
        return 'limited'
    }
    const dependencyMap = new Map<string, SemanticAnchorDependency>()
    for (const edge of closure.values()) {
      const required: SemanticAnchorDependency[] = [
        { ref: edge.backing, fingerprint: edge.backingFingerprint, proofs: edge.proofs },
        ...edge.dependencies.map((d) => ({
          ref: d.entity.backing,
          fingerprint: d.backingFingerprint,
          proofs: d.proofs,
        })),
        ...edge.readDependencies.map((d) => ({
          ref: d.backing,
          fingerprint: d.backingFingerprint,
          proofs: d.proofs,
        })),
      ]
      for (const dependency of required) {
        const live = this.ports.anchorCurrent(ctx, dependency.ref)
        if (
          !live ||
          live.record.id !== dependency.ref.id ||
          live.record.scope.kind !== 'scene' ||
          live.record.scope.sceneId !== ctx.sceneId ||
          dependency.proofs.some((p) => !live.proofs.some((q) => isDeepStrictEqual(p, q)))
        )
          return 'limited'
        dependencyMap.set(semanticHash(dependency), structuredClone(dependency))
        if (dependencyMap.size > 128) return 'limited'
      }
    }
    const anchorDependencies = [...dependencyMap.entries()]
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([, v]) => v)
    const parents = [...records.values()]
      .map((v) => v.selection)
      .sort((a, b) => (a.ref.id < b.ref.id ? -1 : a.ref.id > b.ref.id ? 1 : 0))
    const edges = [...closure.values()].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
    const endpoint = (e: KnowledgeEndpoint) =>
      e.kind === 'entity'
        ? {
            kind: 'entity' as const,
            entityId: e.entity.id,
            entityKind: e.entity.kind,
            backing: e.entity.backing,
            originEdgeId: e.entity.originEdgeId,
          }
        : structuredClone(e)
    const anchorQualifications = edges.map((edge) =>
      SemanticAnchorQualificationSchema.parse({
        edgeId: edge.id,
        edgeVersion: edge.version,
        registryVersion: edge.registryVersion,
        predicate: edge.predicate,
        ...(edge.property ? { property: edge.property } : {}),
        subject: endpoint(edge.subject),
        object: endpoint(edge.object),
        polarity: edge.polarity,
        modality: edge.modality,
        temporalScope: edge.temporalScope,
        conditions: edge.conditions,
        occurredAt: this.ports.anchorCurrent(ctx, edge.backing)?.record.occurredAt ?? null,
        validFrom: edge.validFrom ?? null,
        validUntil: edge.validUntil ?? null,
        status: edge.status,
        admission: edge.admission,
        interpretation: 'attributed_proposed_association_not_observed_event',
      }),
    )
    const anchor = { id: entity.id, version: origin.version }
    return {
      anchor,
      anchorMembershipKey: semanticHash([anchor, parents, edges]),
      parents,
      anchorDependencies,
      anchorQualifications,
      edges: structuredClone(edges),
      interpretation: 'grouped_declarations_not_observed_event',
    }
  }
  valid(
    ctx: SemanticContext,
    anchor: { id: string; version: number },
    parents: readonly SemanticRef[],
    membershipKey?: string,
  ): boolean {
    if (parents.length < 2 || parents.length > 12) return false
    let group: SemanticEpisodeGroup | undefined
    for (const ref of parents) {
      const page = this.discover(ctx, ref)
      if (page.coverage !== 'complete') return false
      group = page.groups.find(
        (g) => g.anchor.id === anchor.id && g.anchor.version === anchor.version,
      )
      if (group) break
    }
    if (!group || (membershipKey !== undefined && group.anchorMembershipKey !== membershipKey))
      return false
    const wanted = uniqueRefs(parents).sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
    return (
      wanted.length === parents.length &&
      isDeepStrictEqual(
        wanted,
        group.parents.map((p) => p.ref),
      )
    )
  }
}
