import { isDeepStrictEqual } from 'node:util'
import { canonicalProofKey } from '../domain/proofs.ts'
import type { RelationProof } from '../domain/relations.ts'
import {
  SemanticCandidateSchema,
  semanticFingerprint,
  semanticHash,
  semanticDefinitionHash,
  type SemanticQualification,
  type SemanticAuthority,
  type SemanticCandidate,
  type SemanticContext,
  type SemanticParent,
  type SemanticAnchorDependency,
  type SemanticRef,
} from './contracts.ts'
import type { SemanticStorage } from './storage.ts'
const same = (a: SemanticRef, b: SemanticRef) => isDeepStrictEqual(a, b)
const intersection = (stored: RelationProof[], live: RelationProof[]) =>
  stored.filter((p) =>
    live.some(
      (q) =>
        p.proofId === q.proofId &&
        canonicalProofKey(p) === canonicalProofKey(q) &&
        isDeepStrictEqual(p, q),
    ),
  )
/** Only trusted privacy lifecycle calls these operations. Current drift never authorizes deletion. */
export class SemanticLifecycle {
  constructor(
    private readonly storage: SemanticStorage,
    private readonly authority: SemanticAuthority,
  ) {}
  private transform(
    sceneId: string | undefined,
    map: (
      parent: SemanticParent | SemanticAnchorDependency,
    ) => SemanticParent | SemanticAnchorDependency | undefined,
    qualify?: (before: SemanticCandidate, after: SemanticCandidate) => SemanticCandidate,
  ) {
    this.storage.requireTransaction()
    const effects: { before: SemanticCandidate; after: SemanticCandidate | undefined }[] = []
    for (const before of this.storage.rows(sceneId)) {
      const parents = before.parents.map(map)
      const anchorDependencies = before.anchorDependencies.map(map)
      let after =
        parents.some((p) => !p) || anchorDependencies.some((p) => !p)
          ? undefined
          : {
              ...before,
              parents: parents as SemanticParent[],
              anchorDependencies: anchorDependencies as SemanticAnchorDependency[],
            }
      if (after && qualify) after = qualify(before, after)
      if (after && isDeepStrictEqual(after, before)) continue
      if (after) {
        after.version++
        SemanticCandidateSchema.parse(after)
      }
      effects.push({ before, after })
    }
    for (const { before, after } of effects) this.storage.write(after, before, 'privacy')
  }
  erase(ctx: SemanticContext, id: string) {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    const candidate = this.storage.get(id)
    if (candidate && candidate.sceneId === ctx.sceneId)
      this.storage.write(undefined, candidate, 'privacy')
  }
  purgeSource(sceneId: string, messageId: string) {
    this.storage.requireTransaction()
    const root = semanticHash([sceneId, messageId])
    for (const attempt of this.storage.attempts())
      if (attempt.sceneId === sceneId && attempt.roots.includes(root))
        this.storage.writeAttempt(
          { ...attempt, parents: [], roots: [], status: 'abandoned', version: attempt.version + 1 },
          attempt,
        )
    this.transform(undefined, (parent) => {
      const proofs = parent.proofs.filter(
        (p) => !p.sources.some((s) => s.sceneId === sceneId && s.messageId === messageId),
      )
      return proofs.length ? { ...parent, proofs } : undefined
    })
  }
  purgeAssertion(id: string) {
    this.storage.requireTransaction()
    for (const attempt of this.storage.attempts())
      if (attempt.parents.some((p) => p.ref.id === id))
        this.storage.writeAttempt(
          { ...attempt, parents: [], roots: [], status: 'abandoned', version: attempt.version + 1 },
          attempt,
        )
    this.transform(undefined, (parent) => {
      if (parent.ref.id === id) return
      const proofs = parent.proofs.filter((p) => !p.parents.includes(id))
      return proofs.length ? { ...parent, proofs } : undefined
    })
  }
  pruneUnavailableProof(
    ctx: SemanticContext,
    available: (ref: SemanticRef, proof: RelationProof) => boolean,
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    this.transform(ctx.sceneId, (parent) => {
      const proofs = parent.proofs.filter((p) => available(parent.ref, p))
      return proofs.length ? { ...parent, proofs } : undefined
    })
  }
  requalify(
    ctx: SemanticContext,
    changes: readonly { before: SemanticRef; after: SemanticRef }[],
    qualifiedBefore: readonly SemanticQualification[] = [],
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    this.transform(
      ctx.sceneId,
      (parent) => {
        const change = changes.find((c) => same(c.before, parent.ref))
        if (!change) return parent
        if (change.before.id !== change.after.id)
          throw new Error('semantic_requalification_invalid')
        const originalCurrent =
          'roots' in parent ? this.authority.current(ctx, change.after) : undefined
        const current =
          'roots' in parent ? originalCurrent : this.authority.anchorCurrent(ctx, change.after)
        if (
          !current ||
          ('rootCoverage' in current && current.rootCoverage !== 'complete') ||
          ('derived' in current && current.derived) ||
          semanticFingerprint(current.record) !== parent.fingerprint
        )
          return parent
        const proofs = intersection(parent.proofs, current.proofs)
        if (!proofs.length) return parent
        if ('roots' in parent && originalCurrent)
          return {
            ...parent,
            ref: change.after,
            dependencyKey: originalCurrent.dependencyKey,
            proofs,
            roots: parent.roots.filter((r) => originalCurrent.roots.includes(r)),
          }
        return { ...parent, ref: change.after, proofs }
      },
      (before, after) => {
        if (before.partitionKey === null) return after
        const captured = qualifiedBefore.find(
          (q) =>
            q.id === before.id &&
            q.inputKey === before.inputKey &&
            q.partitionKey === before.partitionKey &&
            q.definitionHash === semanticDefinitionHash(before),
        )
        if (
          !captured ||
          !isDeepStrictEqual(
            captured.parents,
            before.parents.map((p) => ({ ref: p.ref, dependencyKey: p.dependencyKey })),
          )
        )
          return after
        if (!after.parents.some((p, i) => !same(p.ref, before.parents[i]!.ref))) return after
        for (const parent of after.parents) {
          const current = this.authority.current(ctx, parent.ref)
          if (
            !current ||
            current.dependencyKey !== parent.dependencyKey ||
            current.rootCoverage !== 'complete' ||
            current.derived ||
            semanticFingerprint(current.record) !== parent.fingerprint ||
            !intersection(parent.proofs, current.proofs).length
          )
            return after
        }
        const partitionKey = this.authority.partitionKey(ctx, before.personId)
        return partitionKey && /^[a-f0-9]{64}$/.test(partitionKey)
          ? { ...after, partitionKey }
          : after
      },
    )
  }
}
