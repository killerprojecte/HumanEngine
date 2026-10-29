import type { Store } from '../../store.ts'
import type {
  MemoryCandidate,
  MemoryContext,
  MemoryRecord,
  MemoryScope,
} from '../domain/contracts.ts'
import { fingerprint } from '../domain/record-values.ts'
import {
  readAssertionRow as read,
  type AssertionRepository,
  type AssertionRow as Row,
} from '../storage/assertion-repository.ts'
import type { SourceAdmission } from './source-admission.ts'
/** Synchronous reconciliation decision; not a durable permission or change-set. */
export class ReconciliationPolicy {
  constructor(
    private readonly store: Pick<Store, 'db' | 'recalled'>,
    private readonly repository: AssertionRepository,
  ) {}
  inspect(
    ctx: MemoryContext,
    candidate: MemoryCandidate,
    scope: Extract<MemoryScope, { kind: 'scene' }>,
    attribution: Extract<
      ReturnType<SourceAdmission['prepare']>,
      { status: 'accepted' }
    >['attribution'],
    parents: string[],
  ) {
    const merging = candidate.mergeIntoId !== undefined
    if (merging && (!candidate.mergeIntoId || candidate.supersedesId !== undefined))
      return { status: 'rejected' as const, reason: 'invalid_merge_request' }
    const fp = fingerprint(scope, candidate.content)
    if (this.store.db.prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?').get(fp))
      return { status: 'rejected' as const, reason: 'manually_deleted' }
    const existingRow = this.store.db
      .prepare(
        merging
          ? 'SELECT * FROM memories WHERE id=?'
          : 'SELECT * FROM memories WHERE fingerprint=?',
      )
      .get(merging ? candidate.mergeIntoId : fp) as Row | undefined
    // Reported third-party testimony cannot silently strengthen or replace a self assertion.
    const protectedSubject = (id: string) => {
      const record = this.repository.get(id)
      const proofs = this.repository
        .supports(id)
        .filter(
          (proof) =>
            !proof.invalidatedBy &&
            proof.sources.every((source) => !this.store.recalled(source.sceneId, source.messageId)),
        )
      return (
        !record ||
        record.kind !== 'claim' ||
        !['reported', 'inferred', 'hypothesis'].includes(record.epistemicKind ?? '') ||
        !proofs.length ||
        proofs.some((proof) => proof.subjectWitness?.kind !== 'mentioned')
      )
    }
    if (
      attribution.witness?.kind === 'mentioned' &&
      ((existingRow && protectedSubject(existingRow.id)) ||
        (candidate.supersedesId && protectedSubject(candidate.supersedesId)))
    )
      return { status: 'rejected' as const, reason: 'reported_subject_cannot_replace_self' }
    if (existingRow) {
      const prior = read(existingRow)
      const priorProofs = this.repository
        .supports(prior.id)
        .filter(
          (proof) =>
            !proof.invalidatedBy &&
            proof.sources.every((source) => !this.store.recalled(source.sceneId, source.messageId)),
        )
      const witnessKind = attribution.witness?.kind
      if (
        priorProofs.some((proof) => proof.subjectWitness?.kind !== witnessKind) ||
        prior.epistemicKind !== candidate.epistemicKind
      )
        return { status: 'rejected' as const, reason: 'incompatible_subject_corroboration' }
    }
    if (merging) {
      const target = existingRow ? read(existingRow) : undefined
      if (
        target &&
        ctx.provenance === 'source-only' &&
        fp !== fingerprint(target.scope, target.content)
      )
        return { status: 'rejected' as const, reason: 'merge_requires_exact_content' }
      if (
        !target ||
        target.locked ||
        target.origin !== 'learned' ||
        target.status !== 'active' ||
        target.scope.kind !== 'scene' ||
        target.scope.sceneId !== ctx.sceneId ||
        target.scope.personId !== scope.personId ||
        (target.validUntil !== undefined && Date.parse(target.validUntil) <= Date.now()) ||
        target.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)) ||
        this.store.db
          .prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?')
          .get(existingRow!.fingerprint)
      )
        return { status: 'rejected' as const, reason: 'merge_not_allowed' }
    }
    let superseded: MemoryRecord | undefined
    if (candidate.supersedesId) {
      const previous = this.repository.get(candidate.supersedesId)
      const sameScene =
        previous?.scope.kind === 'scene' &&
        previous.scope.sceneId === ctx.sceneId &&
        previous.scope.personId === candidate.personId
      const owningPublicSource =
        previous?.scope.kind === 'sharedPublicKnowledge' &&
        previous.origin === 'learned' &&
        previous.status === 'active' &&
        previous.sources.length > 0 &&
        previous.sources.every((source) => source.sceneId === ctx.sceneId)
      if (
        !previous ||
        previous.locked ||
        previous.origin === 'manual' ||
        !['active', 'disputed'].includes(previous.status) ||
        (!sameScene && !owningPublicSource) ||
        existingRow?.id === previous.id
      )
        return { status: 'rejected' as const, reason: 'correction_not_allowed' }
      superseded = previous
    }
    if (superseded && parents.includes(superseded.id))
      return { status: 'rejected' as const, reason: 'correction_depends_on_superseded_memory' }
    return { status: 'accepted' as const, fp, existingRow, superseded }
  }
}
