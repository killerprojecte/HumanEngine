import type { RelationProof } from '../domain/relations.ts'
import { isDeepStrictEqual } from 'node:util'
import {
  KnowledgeRefSchema,
  type KnowledgeAuthority,
  type KnowledgeContext,
  type KnowledgeRef,
  type KnowledgeEdge,
  type KnowledgeEndpoint,
} from './contracts.ts'
import { KnowledgeStorage, KnowledgeEdgeSchema } from './storage.ts'
import { knowledgeBackingFingerprint, sameRef, sameSource, intersect } from './qualification.ts'
/** Caller-owned transaction and historical privacy maintenance; no model or admission authority. */
export class KnowledgeLifecycle {
  constructor(
    private readonly storage: Pick<
      KnowledgeStorage,
      'requireTransaction' | 'rows' | 'remove' | 'replace'
    >,
    private readonly current: (
      ctx: KnowledgeContext,
      ref: KnowledgeRef,
    ) => ReturnType<KnowledgeAuthority['current']>,
  ) {}
  /** Trusted recall lifecycle only: exact pre/post refs from this mutation, never latest rebinding. */
  requalify(
    ctx: KnowledgeContext,
    changes: readonly { before: KnowledgeRef; after: KnowledgeRef }[],
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    const resolve = (reference: KnowledgeRef, fingerprint: string, proofs: RelationProof[]) => {
      const change = changes.find((c) => sameRef(c.before, reference))
      if (!change) return undefined
      if (
        change.before.id !== change.after.id ||
        !KnowledgeRefSchema.safeParse(change.after).success
      )
        throw new Error('knowledge_requalification_invalid')
      const view = this.current(ctx, change.after)
      if (!view || knowledgeBackingFingerprint(view.record) !== fingerprint) return undefined
      const survivors = intersect(proofs, view.proofs)
      return survivors.length ? { ref: change.after, proofs: survivors } : undefined
    }
    const rows = this.storage.rows(ctx.sceneId),
      updates = new Map<string, KnowledgeEdge>()
    for (const edge of rows) {
      const own = resolve(edge.backing, edge.backingFingerprint, edge.proofs)
      const dependencies = edge.dependencies.map((dep) => {
        const next = resolve(dep.entity.backing, dep.backingFingerprint, dep.proofs)
        return next
          ? { ...dep, entity: { ...dep.entity, backing: next.ref }, proofs: next.proofs }
          : dep
      })
      const readDependencies = edge.readDependencies.map((dep) => {
        const next = resolve(dep.backing, dep.backingFingerprint, dep.proofs)
        return next ? { ...dep, backing: next.ref, proofs: next.proofs } : dep
      })
      const endpoint = (point: KnowledgeEndpoint): KnowledgeEndpoint => {
        if (point.kind !== 'entity') return point
        if (point.entity.originEdgeId === edge.id && own)
          return { ...point, entity: { ...point.entity, backing: own.ref } }
        const dep = dependencies.find((d) => d.entity.id === point.entity.id)
        return dep ? { kind: 'entity', entity: dep.entity } : point
      }
      const proofs = own?.proofs ?? edge.proofs
      const next = {
        ...edge,
        backing: own?.ref ?? edge.backing,
        proofs,
        dependencies,
        readDependencies,
        subject: endpoint(edge.subject),
        object: endpoint(edge.object),
        spans: edge.spans.filter((span) =>
          proofs.some((p) => p.sources.some((source) => sameSource(source, span.source))),
        ),
      }
      if (!isDeepStrictEqual(next, edge))
        updates.set(edge.id, { ...next, version: edge.version + 1 })
    }
    this.applyMaintenance(rows, updates, new Set())
  }
  /** Propagate only exact versions changed by this privacy operation, not arbitrary latest edges. */
  private applyMaintenance(
    rows: KnowledgeEdge[],
    updates: Map<string, KnowledgeEdge>,
    removed: Set<string>,
  ) {
    const originals = new Map(rows.map((edge) => [edge.id, edge]))
    let settled = false
    for (let round = 0; round <= rows.length; round++) {
      let changed = false
      for (const original of rows) {
        if (removed.has(original.id)) continue
        const edge = updates.get(original.id) ?? original
        if (
          edge.dependencies.some((d) => removed.has(d.entity.originEdgeId)) ||
          edge.readDependencies.some((d) => d.edgeId !== undefined && removed.has(d.edgeId))
        ) {
          removed.add(edge.id)
          changed = true
          continue
        }
        let invalid = false
        const readDependencies = edge.readDependencies.map((dep, index) => {
          if (dep.edgeId === undefined) return dep
          const before = originals.get(dep.edgeId),
            after = updates.get(dep.edgeId),
            captured = original.readDependencies[index]
          if (
            !before ||
            !after ||
            !captured ||
            captured.edgeVersion !== before.version ||
            !sameRef(captured.backing, before.backing) ||
            captured.backingFingerprint !== before.backingFingerprint
          )
            return dep
          const proofs = intersect(dep.proofs, after.proofs)
          if (!proofs.length) {
            invalid = true
            return dep
          }
          return { ...dep, edgeVersion: after.version, backing: after.backing, proofs }
        })
        if (invalid) {
          removed.add(edge.id)
          changed = true
          continue
        }
        if (!isDeepStrictEqual(readDependencies, edge.readDependencies)) {
          updates.set(edge.id, { ...edge, readDependencies, version: original.version + 1 })
          changed = true
        }
      }
      if (!changed) {
        settled = true
        break
      }
    }
    if (!settled) throw new Error('knowledge_maintenance_cycle')
    for (const [id, edge] of updates) if (!removed.has(id)) KnowledgeEdgeSchema.parse(edge)
    for (const id of removed) this.storage.remove(id)
    for (const [id, edge] of updates) if (!removed.has(id)) this.storage.replace(edge)
  }
  /** Privacy lifecycle only. The caller supplies historical provenance, not current applicability. */
  pruneUnavailableProof(
    ctx: KnowledgeContext,
    available: (backing: KnowledgeRef, proof: RelationProof) => boolean,
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    this.purge((proof, backing) => !available(backing, proof), undefined, ctx.sceneId)
  }
  private purge(
    matches: (proof: RelationProof, backing: KnowledgeRef) => boolean,
    assertionId?: string,
    sceneId?: string,
  ) {
    this.storage.requireTransaction()
    const rows = this.storage.rows(sceneId),
      updates = new Map<string, KnowledgeEdge>(),
      removed = new Set<string>()
    for (const edge of rows) {
      const proofs = edge.proofs.filter((p) => !matches(p, edge.backing))
      const dependencies = edge.dependencies.map((d) => ({
        ...d,
        proofs: d.proofs.filter((p) => !matches(p, d.entity.backing)),
      }))
      const readDependencies = edge.readDependencies.map((d) => ({
        ...d,
        proofs: d.proofs.filter((p) => !matches(p, d.backing)),
      }))
      if (
        edge.backing.id === assertionId ||
        !proofs.length ||
        dependencies.some((d) => d.entity.backing.id === assertionId || !d.proofs.length) ||
        readDependencies.some((d) => d.backing.id === assertionId || !d.proofs.length)
      ) {
        removed.add(edge.id)
        continue
      }
      const spans = edge.spans.filter((span) =>
        proofs.some((p) => p.sources.some((source) => sameSource(source, span.source))),
      )
      if (!spans.length) {
        removed.add(edge.id)
        continue
      }
      const next = { ...edge, proofs, dependencies, readDependencies, spans }
      if (!isDeepStrictEqual(next, edge))
        updates.set(edge.id, { ...next, version: edge.version + 1 })
    }
    this.applyMaintenance(rows, updates, removed)
  }
  purgeSource(sceneId: string, messageId: string) {
    this.purge((p) => p.sources.some((s) => s.sceneId === sceneId && s.messageId === messageId))
  }
  purgeAssertion(id: string) {
    this.purge((p) => p.parents.includes(id), id)
  }
}
