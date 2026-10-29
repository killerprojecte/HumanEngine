import type { Store } from '../../store.ts'
import type { CommitmentRef } from '../domain/commitments.ts'
import type { MemoryContext, MemoryRecord, MemoryScope } from '../domain/contracts.ts'
import { canonicalProofKey, type SupportProof as Support } from '../domain/proofs.ts'
import { scopeKey, uniqueSources } from '../domain/record-values.ts'
import type { RelationAssertionReference, RelationContext } from '../domain/relations.ts'
import type { AssertionRepository } from '../storage/assertion-repository.ts'
import type { MemoryRevisionCoordinator } from '../write/revision-coordinator.ts'
import { earliestObservation } from '../write/subject-attribution.ts'
import { isCurrentlyApplicable } from './applicability.ts'
import { createParentTraversal, inspectBoundParents } from './bound-parents.ts'
import { domainSourceCurrent } from './domain-source-events.ts'
import { hasProvenance, ProofTraversalLimit, type ProvenanceBudget } from './provenance.ts'
/** Privacy support and current applicability are deliberately separate operations. */
export class AssertionAuthority {
  constructor(
    private readonly store: Pick<
      Store,
      'assertRecoveryReady' | 'recalled' | 'sourceEvents' | 'scene'
    >,
    private readonly repository: AssertionRepository,
    private readonly revisions: MemoryRevisionCoordinator,
  ) {}
  historicalAssertion(ctx: Pick<MemoryContext, 'sceneId' | 'simulation'>, ref: CommitmentRef) {
    this.store.assertRecoveryReady()
    if (ctx.simulation) return undefined
    const current = this.repository.get(ref.id)
    const state = this.revisions.history.state(ref.id)
    if (
      !current ||
      current.scope.kind !== 'scene' ||
      current.scope.sceneId !== ctx.sceneId ||
      state.deleted ||
      state.deletionEpoch !== ref.deletionEpoch ||
      !this.supported(current)
    )
      return undefined
    const revision = this.revisions.history.revision(ref.id, ref.version, scopeKey(current.scope))
    if (
      !revision ||
      revision.record.scope.kind !== 'scene' ||
      revision.record.scope.sceneId !== ctx.sceneId ||
      !this.parentScopeAllows(current.scope, revision.record.scope)
    )
      return undefined
    const proofs = this.filterHistorySupports(revision.record.scope, revision.supports)
    if (revision.record.lineageStatus === 'verified' && !proofs.length) return undefined
    const record = this.withObservation(
      { ...revision.record, sources: uniqueSources(proofs.flatMap((proof) => proof.sources)) },
      proofs,
    )
    return {
      record,
      proofs: proofs.map((proof) => ({
        ...structuredClone(proof),
        proofId: canonicalProofKey(proof),
      })),
    }
  }

  relationAuthority(ctx: RelationContext, ref: RelationAssertionReference) {
    this.store.assertRecoveryReady()
    if (ctx.simulation) return undefined
    const record = this.repository.get(ref.id)
    const state = this.revisions.history.state(ref.id)
    if (
      !record ||
      record.scope.kind !== 'scene' ||
      record.scope.sceneId !== ctx.sceneId ||
      state.deleted ||
      state.version !== ref.version ||
      state.deletionEpoch !== ref.deletionEpoch ||
      !this.supported(record) ||
      !this.currentApplicable(record)
    )
      return undefined
    const proofs = this.historySupports(ref.id).filter(
      (proof) =>
        this.proofParentsAvailable(record.scope, proof, 'current') &&
        proof.sources.every((source) => domainSourceCurrent(this.store, source)),
    )
    return {
      record: this.sanitize(record),
      proofs: proofs.map((proof) => ({
        ...structuredClone(proof),
        proofId: canonicalProofKey(proof),
      })),
    }
  }

  filterHistorySupports(
    scope: MemoryScope,
    proofs: Support[],
    traversal = createParentTraversal(),
  ): Support[] {
    return proofs.filter((proof) => {
      traversal.remaining -= 1 + proof.sources.length
      if (traversal.remaining < 0) throw new ProofTraversalLimit()
      return (
        !proof.invalidatedBy &&
        proof.sources.length > 0 &&
        proof.sources.every((source) => !this.store.recalled(source.sceneId, source.messageId)) &&
        this.proofParentsAvailable(scope, proof, 'history', traversal)
      )
    })
  }

  proofParentsAvailable(
    scope: MemoryScope,
    proof: Support,
    mode: 'current' | 'history',
    traversal = createParentTraversal(),
  ): boolean {
    return inspectBoundParents(
      scope,
      proof,
      mode,
      {
        current: (child, id, cache, budget) => {
          const record = this.repository.get(id)
          return record && this.validParent(child, id, cache, budget)
            ? { record, state: this.revisions.history.state(id) }
            : undefined
        },
        revision: (reference, current) =>
          this.revisions.history.revision(reference.id, reference.version, scopeKey(current.scope)),
        allows: (child, parent) => this.parentScopeAllows(child, parent),
        recalled: (source) => this.store.recalled(source.sceneId, source.messageId),
      },
      new Set(),
      traversal,
    )
  }

  parentScopeAllows(child: MemoryScope, parent: MemoryScope) {
    return (
      parent.kind === 'sharedPublicKnowledge' ||
      (child.kind === 'scene' &&
        parent.sceneId === child.sceneId &&
        (parent.personId === undefined || parent.personId === child.personId))
    )
  }

  historySupports(id: string): Support[] {
    const record = this.repository.get(id)
    return record ? this.filterHistorySupports(record.scope, this.repository.supports(id)) : []
  }

  validParent(
    scope: MemoryScope,
    id: string,
    cache = new Map<string, boolean>(),
    budget: ProvenanceBudget = { remaining: 4096 },
  ): boolean {
    const parent = this.repository.get(id)
    if (!parent || !this.supported(parent, new Set(), cache, budget)) return false
    if (parent.scope.kind === 'sharedPublicKnowledge') return true
    return (
      scope.kind === 'scene' &&
      parent.scope.sceneId === scope.sceneId &&
      (parent.scope.personId === undefined || parent.scope.personId === scope.personId)
    )
  }
  currentApplicable(
    memory: MemoryRecord,
    path = new Set<string>(),
    cache = new Map<string, boolean>(),
  ) {
    return isCurrentlyApplicable(
      memory,
      {
        record: (id) => this.repository.get(id),
        proofs: (id) => this.repository.supports(id),
        sourceCurrent: (source) => domainSourceCurrent(this.store, source),
        recalled: (source) => this.store.recalled(source.sceneId, source.messageId),
        supported: (record) => this.supported(record),
        parentBindings: (record, proof) =>
          this.proofParentsAvailable(record.scope, proof, 'current') &&
          proof.sources.every((source) => domainSourceCurrent(this.store, source)),
      },
      Date.now(),
      path,
      cache,
    )
  }

  supported(
    memory: MemoryRecord,
    path = new Set<string>(),
    cache = new Map<string, boolean>(),
    budget: ProvenanceBudget = { remaining: 4096 },
  ): boolean {
    return hasProvenance(
      memory,
      {
        record: (id) => this.repository.get(id),
        proofs: (id) => this.repository.supports(id),
        recalled: (source) => this.store.recalled(source.sceneId, source.messageId),
        parentAllowed: (child, parent, proof) => {
          if (!this.parentScopeAllows(child.scope, parent.scope)) return false
          const reference = proof.parentReferences?.find((value) => value.id === parent.id)
          if (!reference) return parent.status !== 'superseded'
          const state = this.revisions.history.state(parent.id)
          if (state.deleted || state.deletionEpoch !== reference.deletionEpoch) return false
          const revision = this.revisions.history.revision(
            parent.id,
            reference.version,
            scopeKey(parent.scope),
          )
          return (
            !!revision &&
            this.parentScopeAllows(child.scope, revision.record.scope) &&
            revision.record.lineageStatus !== 'quarantined' &&
            revision.record.status !== 'invalidated' &&
            !revision.record.sources.some((source) =>
              this.store.recalled(source.sceneId, source.messageId),
            )
          )
        },
      },
      path,
      cache,
      budget,
    )
  }
  /** Re-evaluate provenance atomically; temporal/version drift is a separate read gate. */
  sanitize(memory: MemoryRecord): MemoryRecord {
    if (memory.scope.kind === 'sharedPublicKnowledge') {
      const { observedAt: _old, ...record } = memory
      return { ...record, sources: [], supersedesId: undefined }
    }
    const proofs = this.historySupports(memory.id).filter(
      (proof) =>
        this.proofParentsAvailable(memory.scope, proof, 'current') &&
        proof.sources.every((source) => domainSourceCurrent(this.store, source)),
    )
    const sources =
      memory.lineageStatus === 'verified'
        ? uniqueSources(proofs.flatMap((proof) => proof.sources))
        : memory.sources.filter((source) => domainSourceCurrent(this.store, source))
    return this.withObservation({ ...memory, sources }, proofs)
  }
  withObservation(memory: MemoryRecord, proofs: Support[]): MemoryRecord {
    const { observedAt: _old, ...record } = memory
    const observedAt = earliestObservation(
      proofs.flatMap((proof) => proof.sourceObservations ?? []),
    )
    return { ...record, ...(observedAt ? { observedAt } : {}) }
  }
}
