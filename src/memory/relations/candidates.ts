import { isDeepStrictEqual } from 'node:util'
import { canonicalProofKey } from '../domain/proofs.ts'
import { createHash, randomUUID } from 'node:crypto'
import {
  RelationProposalSchema,
  type RelationAuthority,
  type RelationAssertionReference,
  type RelationContext,
  type RelationRecord,
  type RelationEntity,
} from '../domain/relations.ts'
import { RelationStorage, type RelationDatabase } from './storage.ts'
import { autoEligible, personProofs, sameReference, validBacking } from './validation.ts'
export class RelationCandidates {
  readonly storage: RelationStorage
  constructor(
    db: RelationDatabase,
    private readonly authority: RelationAuthority,
    private readonly options: { now?: () => number; id?: () => string } = {},
  ) {
    this.storage = new RelationStorage(db)
  }
  private now() {
    return (this.options.now ?? Date.now)()
  }
  private id() {
    return (this.options.id ?? randomUUID)()
  }
  private currentProjection(
    ctx: RelationContext,
    record: RelationRecord,
  ): RelationRecord | undefined {
    if (ctx.simulation || record.sceneId !== ctx.sceneId || !this.authority.enabled(ctx))
      return undefined
    const backing = this.authority.current(ctx, record.backing)
    if (
      !validBacking(ctx, backing, record.personId) ||
      !validBacking(ctx, this.authority.current(ctx, record.target.backing))
    )
      return undefined
    const liveProofs = personProofs(backing, record.personId)
    const proofs = record.proofs.filter((proof) =>
      liveProofs.some(
        (live) =>
          live.proofId === proof.proofId &&
          canonicalProofKey(live) === canonicalProofKey(proof) &&
          isDeepStrictEqual(live, proof),
      ),
    )
    if (!proofs.length) return undefined
    // Intersection only: a newly introduced proof must not silently authorize an old edge.
    return structuredClone({ ...record, proofs })
  }
  private eligible(ctx: RelationContext, record: RelationRecord) {
    return this.currentProjection(ctx, record) !== undefined
  }
  admit(ctx: RelationContext, backing: RelationAssertionReference, proposals: readonly unknown[]) {
    this.storage.requireTransaction()
    if (
      !backing.id ||
      !Number.isSafeInteger(backing.version) ||
      backing.version < 1 ||
      !Number.isSafeInteger(backing.deletionEpoch) ||
      backing.deletionEpoch < 0
    )
      throw new Error('invalid_relation_reference')
    const created: RelationRecord[] = [],
      rejected: { index: number; reason: string }[] = []
    if (ctx.simulation || !this.authority.enabled(ctx) || !this.authority.learningEnabled(ctx))
      return { created, rejected: [{ index: -1, reason: 'relation_admission_disabled' }] }
    if (proposals.length > 3)
      return { created, rejected: [{ index: -1, reason: 'relation_candidate_limit' }] }
    for (const [index, input] of proposals.entries()) {
      const parsed = RelationProposalSchema.safeParse(input)
      if (!parsed.success) {
        rejected.push({ index, reason: 'invalid_relation_proposal' })
        continue
      }
      const proposal = parsed.data,
        view = this.authority.current(ctx, backing)
      if (!validBacking(ctx, view, proposal.personId)) {
        rejected.push({ index, reason: 'relation_backing_unavailable' })
        continue
      }
      const key = createHash('sha256')
        .update(JSON.stringify([ctx.sceneId, backing, proposal]))
        .digest('hex')
      let target: RelationEntity | undefined, newEntity: RelationEntity | undefined
      if (proposal.target.kind === 'new') {
        target = {
          id: this.id(),
          sceneId: ctx.sceneId,
          kind: proposal.target.entityKind,
          title: proposal.target.title,
          backing: structuredClone(backing),
        }
        newEntity = target
      } else {
        const entityId = proposal.target.entityId
        const exposed = ctx.exposedEntities?.find((item) => item.entityId === entityId)
        target = this.storage.entity(proposal.target.entityId)
        if (
          ctx.provenance !== 'contextual' ||
          !target ||
          target.sceneId !== ctx.sceneId ||
          !exposed ||
          !sameReference(exposed.backingRef, target.backing) ||
          !validBacking(ctx, this.authority.current(ctx, target.backing))
        ) {
          rejected.push({ index, reason: 'relation_target_not_exposed' })
          continue
        }
      }
      const existing = this.storage.byKey(key)
      if (existing) {
        const projected = this.currentProjection(ctx, existing)
        if (projected) created.push(projected)
        else rejected.push({ index, reason: 'relation_backing_unavailable' })
        continue
      }
      const record: RelationRecord = {
        id: this.id(),
        version: 1,
        sceneId: ctx.sceneId,
        predicate: proposal.predicate,
        modality: proposal.modality,
        temporalScope: proposal.temporalScope,
        personId: proposal.personId,
        target,
        polarity: proposal.polarity,
        conditions: structuredClone(proposal.conditions),
        ...(proposal.occurredAt ? { occurredAt: proposal.occurredAt } : {}),
        ...(proposal.validFrom ? { validFrom: proposal.validFrom } : {}),
        ...(proposal.validUntil ? { validUntil: proposal.validUntil } : {}),
        ...(view.record.epistemicKind ? { epistemicKind: view.record.epistemicKind } : {}),
        backing: structuredClone(backing),
        proofs: structuredClone(personProofs(view, proposal.personId)),
        status: 'candidate',
        admission: 'candidate',
        createdAt: new Date(this.now()).toISOString(),
      }
      const rows = this.storage.rows(ctx.sceneId)
      const conflict =
        rows.length > 200 ||
        rows.some(
          (row) =>
            row.status !== 'rejected' &&
            row.personId === record.personId &&
            row.target.id === target.id &&
            (row.polarity !== record.polarity ||
              row.modality !== 'asserted' ||
              row.temporalScope !== 'current' ||
              row.conditions.length > 0) &&
            this.eligible(ctx, row),
        )
      const originClear =
        proposal.target.kind === 'new' ||
        rows.some(
          (row) =>
            row.target.id === target.id &&
            sameReference(row.backing, target.backing) &&
            row.status === 'accepted' &&
            row.modality === 'asserted' &&
            row.temporalScope === 'current' &&
            row.polarity === 'positive' &&
            row.conditions.length === 0 &&
            this.eligible(ctx, row),
        )
      const targetView = this.authority.current(ctx, target.backing)
      const targetClear =
        validBacking(ctx, targetView) &&
        targetView.record.status === 'active' &&
        targetView.record.epistemicKind === 'explicit' &&
        targetView.record.occurredAt === undefined &&
        (!targetView.record.validFrom || Date.parse(targetView.record.validFrom) <= this.now()) &&
        (!targetView.record.validUntil || Date.parse(targetView.record.validUntil) > this.now())
      if (
        this.authority.autoAdmission(ctx) &&
        targetClear &&
        originClear &&
        autoEligible(record, view, this.now(), conflict)
      ) {
        record.status = 'accepted'
        record.admission = 'automatic'
        record.autoReason = 'self_explicit_no_visible_conflict'
      }
      this.storage.put(record, key, newEntity)
      created.push(structuredClone(record))
    }
    return { created, rejected }
  }
  review(
    ctx: RelationContext,
    id: string,
    expectedVersion: number,
    status: 'accepted' | 'rejected',
    actorId?: string,
  ) {
    this.storage.requireTransaction()
    if (
      !Number.isSafeInteger(expectedVersion) ||
      expectedVersion < 1 ||
      !['accepted', 'rejected'].includes(status)
    )
      throw new Error('invalid_relation_review')
    const record = this.storage.get(id)
    if (!record || !this.eligible(ctx, record)) throw new Error('relation_unavailable')
    if (record.version !== expectedVersion) throw new Error('relation_version_conflict')
    const next: RelationRecord = {
      ...record,
      version: record.version + 1,
      status,
      admission: 'reviewed',
      reviewedAt: new Date(this.now()).toISOString(),
      ...(actorId ? { actorId } : {}),
    }
    delete next.autoReason
    this.storage.update(next, expectedVersion)
    return this.currentProjection(ctx, next)!
  }
  list(
    ctx: RelationContext,
    options: { personId?: string; status?: RelationRecord['status']; limit?: number } = {},
  ) {
    const limit = options.limit ?? 40
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 40)
      throw new Error('relation_read_limit')
    if (ctx.simulation || !this.authority.enabled(ctx))
      return { items: [] as RelationRecord[], coverage: 'complete' as const }
    const rows = this.storage.rows(ctx.sceneId),
      eligible = rows
        .slice(0, 200)
        .filter(
          (row) =>
            (!options.personId || row.personId === options.personId) &&
            (!options.status || row.status === options.status) &&
            this.eligible(ctx, row),
        )
        .map((row) => this.currentProjection(ctx, row))
        .filter((row): row is RelationRecord => row !== undefined)
    return {
      items: eligible.slice(0, limit),
      coverage:
        rows.length > 200 || eligible.length > limit ? ('limited' as const) : ('complete' as const),
    }
  }
  purgeAssertion(id: string) {
    return this.storage.purgeAssertion(id)
  }
  assertionChanged(id: string) {
    return this.storage.purgeAssertion(id)
  }
}
