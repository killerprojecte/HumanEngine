import { CommitmentTemporalStore } from './temporal-store.ts'
import {
  commitmentTemporalQualification,
  temporalHash,
  qualifyRecordedState,
  TemporalMarkerSchema,
  type TemporalMarker,
  type CommitmentTimeline,
  type CommitmentTransition,
} from './temporal-contracts.ts'
import { readCommitmentTimeline } from './temporal-read.ts'
import type { CommitmentExposure } from '../domain/commitments.ts'
import { isDeepStrictEqual } from 'node:util'
import { isRecoveryMaintenance } from '../recovery/ledger.ts'
import { createHash, randomUUID } from 'node:crypto'
import {
  commitmentBackingFingerprint,
  CommitmentCommandSchema,
  CommitmentRefSchema,
  type CommitmentAuthority,
  type CommitmentContext,
  type CommitmentRef,
  type CommitmentRevision,
  type CommitmentHead,
  type CommitmentMutation,
  type CommitmentEvidence,
} from '../domain/commitments.ts'
import { CommitmentStorage, type CommitmentDatabase } from './storage.ts'
import { eligibleAssertion, selfPerson, canConfirm, projectCommitment } from './transitions.ts'
const same = (a: CommitmentRef, b: CommitmentRef) =>
  a.id === b.id && a.version === b.version && a.deletionEpoch === b.deletionEpoch
export class CommitmentService {
  readonly storage: CommitmentStorage
  readonly temporal: CommitmentTemporalStore
  constructor(
    db: CommitmentDatabase,
    private readonly authority: CommitmentAuthority,
    private readonly options: { now?: () => string; id?: () => string } = {},
  ) {
    this.storage = new CommitmentStorage(db)
    this.temporal = new CommitmentTemporalStore(db)
  }
  private enabled(ctx: CommitmentContext) {
    return !ctx.simulation && this.authority.enabled(ctx)
  }
  snapshot(id: string) {
    const head = this.storage.head(id)
    if (!head) return undefined
    const revisions = this.storage.revisions(id)
    const timeline = this.temporal.read(id)
    return {
      head,
      revisions,
      timeline,
      qualificationHash: commitmentTemporalQualification(head, revisions, timeline),
    }
  }
  private write(
    kind: CommitmentMutation['kind'],
    head: CommitmentHead,
    revision?: CommitmentRevision,
    nextRevisions?: CommitmentRevision[],
    preparedTimeline?: CommitmentTimeline,
  ) {
    const prior = this.storage.revisions(head.id)
    const revisions = nextRevisions ?? [
      ...prior.filter((r) => r.ref.version !== revision?.ref.version),
      ...(revision ? [revision] : []),
    ]
    const oldTimeline = this.temporal.read(head.id)
    let timeline = preparedTimeline ?? oldTimeline
    let privacyMarker: TemporalMarker | undefined
    if (head.deleted) timeline = undefined
    else if (!isRecoveryMaintenance(this.storage.db)) {
      const ctx: CommitmentContext = {
        sceneId: head.sceneId,
        simulation: false,
        provenance: 'contextual',
      }
      const states =
        kind === 'purge' || kind === 'requalify'
          ? null
          : [...new Set([head.activeVersion ?? head.latestVersion, head.latestVersion])].map(
              (version) => {
                const item = projectCommitment(
                  ctx,
                  head,
                  revisions.find((r) => r.ref.version === version),
                  this.authority,
                )
                const { terms, recordedAt, ...state } = item
                return {
                  ...state,
                  ...(terms ? { termsHash: temporalHash(terms) } : {}),
                  ...(recordedAt ? { termsRecordedAt: recordedAt } : {}),
                }
              },
            )
      const transition: CommitmentTransition = {
        schemaVersion: 1,
        id: head.id,
        sceneId: head.sceneId,
        mutationVersion: head.mutationVersion,
        recordedAt: (this.options.now ?? (() => new Date().toISOString()))(),
        reason: kind as Exclude<typeof kind, 'delete'>,
        head: structuredClone(head),
        states,
      }
      if (kind === 'purge' || kind === 'requalify')
        privacyMarker = TemporalMarkerSchema.parse({
          recordedAt: transition.recordedAt,
          head: transition.head,
          reason: kind,
        })
      timeline = this.temporal.append(timeline, transition)
    }
    if (timeline) timeline = this.temporal.normalize(timeline)
    const result: unknown = this.authority.beforeWrite?.({
      kind,
      head: structuredClone(head),
      qualificationHash: commitmentTemporalQualification(head, revisions, timeline),
      ...(timeline || oldTimeline ? { temporalVersion: 1 as const } : {}),
      ...(privacyMarker ? { privacyMarker } : {}),
    })
    if (result && typeof (result as PromiseLike<unknown>).then === 'function')
      throw Error('commitment_async_authority')
    if (nextRevisions) {
      for (const old of prior)
        if (!revisions.some((r) => r.ref.version === old.ref.version))
          this.storage.removeRevision(head.id, old.ref.version)
      for (const next of revisions) this.storage.put(head, next)
    }
    this.storage.put(head, revision)
    this.temporal.replace(head.id, timeline)
    const after: unknown = this.authority.afterWrite?.(
      structuredClone(head),
      structuredClone(revisions),
      structuredClone(prior),
    )
    if (after && typeof (after as PromiseLike<unknown>).then === 'function')
      throw Error('commitment_async_after_write')
  }
  admit(ctx: CommitmentContext, rawBacking: CommitmentRef, input: unknown) {
    this.storage.requireTransaction()
    const rejected = (reason: string) => ({ status: 'rejected' as const, reason, item: undefined })
    if (!this.enabled(ctx) || !this.authority.learningEnabled(ctx))
      return rejected('commitment_learning_disabled')
    const backing = CommitmentRefSchema.parse(rawBacking)
    const command = CommitmentCommandSchema.parse(input)
    const view = this.authority.current(ctx, backing)
    if (!eligibleAssertion(ctx, view) || view.record.id !== backing.id)
      return rejected('commitment_backing_unavailable')
    if (
      command.kind !== 'propose' &&
      (ctx.provenance !== 'contextual' ||
        !ctx.exposedRefs?.some((ref) => same(ref, command.target)))
    )
      return rejected('commitment_not_exposed')
    if (command.kind !== 'propose') {
      const delivered = ctx.exposedRefs?.find((r) => same(r, command.target))
      const head = this.storage.head(command.target.id)
      if (!delivered || !head || delivered.mutationVersion !== head.mutationVersion)
        return rejected('commitment_snapshot_changed')
    }
    const key = createHash('sha256')
      .update(JSON.stringify([ctx.sceneId, backing, command]))
      .digest('hex')
    const previous = this.storage.command(key)
    if (previous) {
      const item = this.current(ctx, {
        id: previous.id,
        version: previous.version,
        deletionEpoch: command.kind === 'propose' ? 0 : command.target.deletionEpoch,
      })
      return item ? { status: 'duplicate' as const, item } : rejected('commitment_unavailable')
    }
    let head: CommitmentHead, revision: CommitmentRevision
    const person = selfPerson(view)
    if (command.kind === 'propose' || command.kind === 'amend') {
      const people = this.authority.people(ctx, backing)
      if (command.terms.participantIds.some((p) => !people.includes(p)))
        return rejected('commitment_participant_not_witnessed')
      if (command.kind === 'propose') {
        head = {
          id: (this.options.id ?? randomUUID)(),
          sceneId: ctx.sceneId,
          latestVersion: 1,
          activeVersion: null,
          everActivatedVersion: null,
          everTerminal: null,
          deletionEpoch: 0,
          mutationVersion: 1,
          deleted: false,
        }
      } else {
        const old = this.storage.head(command.target.id)
        if (
          !old ||
          old.deleted ||
          old.sceneId !== ctx.sceneId ||
          old.deletionEpoch !== command.target.deletionEpoch ||
          old.latestVersion !== command.target.version
        )
          return rejected('commitment_version_conflict')
        if (old.latestVersion >= 64) return rejected('commitment_revision_limit')
        if (!this.current(ctx, command.target)?.terms) return rejected('commitment_unavailable')
        head = {
          ...old,
          latestVersion: old.latestVersion + 1,
          mutationVersion: old.mutationVersion + 1,
        }
      }
      const recordedAt = (this.options.now ?? (() => new Date().toISOString()))()
      if (!/^\d{4}-\d\d-\d\dT/.test(recordedAt) || !Number.isFinite(Date.parse(recordedAt)))
        throw Error('commitment_clock_invalid')
      revision = {
        ref: { id: head.id, version: head.latestVersion, deletionEpoch: head.deletionEpoch },
        sceneId: ctx.sceneId,
        terms: command.terms,
        recordedAt,
        requiredProposalGroups:
          command.kind === 'amend'
            ? (() => {
                const oldRevision = this.storage.revision(
                  command.target.id,
                  command.target.version,
                )!
                return structuredClone([
                  ...oldRevision.requiredProposalGroups,
                  oldRevision.evidence.filter((e) => e.kind === 'proposal'),
                ])
              })()
            : [],
        evidence: [
          {
            backing,
            backingFingerprint: commitmentBackingFingerprint(view.record),
            proofs: structuredClone(view.proofs),
            kind: 'proposal',
          },
        ],
      }
      if (
        revision.requiredProposalGroups.length > 64 ||
        revision.requiredProposalGroups.reduce((sum, g) => sum + g.length, 0) > 64
      )
        return rejected('commitment_dependency_limit')
      if (person && command.terms.participantIds.includes(person) && canConfirm(revision))
        revision.evidence.push({
          backing,
          backingFingerprint: commitmentBackingFingerprint(view.record),
          proofs: structuredClone(
            view.proofs.filter(
              (p) => p.subjectWitness?.kind === 'self' && p.subjectWitness.personId === person,
            ),
          ),
          personId: person,
          kind: 'confirm',
        })
    } else {
      const old = this.storage.head(command.target.id)
      const prior = this.storage.revision(command.target.id, command.target.version)
      if (
        !old ||
        old.deleted ||
        old.sceneId !== ctx.sceneId ||
        old.deletionEpoch !== command.target.deletionEpoch ||
        !prior ||
        prior.sceneId !== ctx.sceneId
      )
        return rejected('commitment_unavailable')
      if (
        command.kind === 'confirm'
          ? old.latestVersion !== command.target.version
          : old.activeVersion !== command.target.version
      )
        return rejected('commitment_version_conflict')
      if (!person || !prior.terms.participantIds.includes(person) || !canConfirm(prior))
        return rejected('commitment_self_declaration_required')
      const current = this.current(ctx, command.target)
      if (!current?.terms || (command.kind !== 'confirm' && current.confirmation !== 'confirmed'))
        return rejected('commitment_confirmation_incomplete')
      if (prior.evidence.length >= 64) return rejected('commitment_evidence_limit')
      head = { ...old, mutationVersion: old.mutationVersion + 1 }
      revision = structuredClone(prior)
      revision.evidence.push({
        backing,
        backingFingerprint: commitmentBackingFingerprint(view.record),
        proofs: structuredClone(
          view.proofs.filter(
            (p) => p.subjectWitness?.kind === 'self' && p.subjectWitness.personId === person,
          ),
        ),
        personId: person,
        kind: command.kind,
      })
    }
    if (projectCommitment(ctx, head, revision, this.authority).confirmation === 'confirmed') {
      head.activeVersion = revision.ref.version
      head.everActivatedVersion = revision.ref.version
    }
    const resultProjection = projectCommitment(ctx, head, revision, this.authority)
    if (resultProjection.lifecycle === 'done' || resultProjection.lifecycle === 'cancelled')
      head.everTerminal = { version: revision.ref.version, state: resultProjection.lifecycle }
    this.write(command.kind, head, revision)
    this.storage.remember(key, head.id, revision.ref.version)
    return {
      status: 'saved' as const,
      item: projectCommitment(ctx, head, revision, this.authority),
    }
  }
  current(ctx: CommitmentContext, ref: CommitmentRef) {
    if (!this.enabled(ctx)) return undefined
    const head = this.storage.head(ref.id)
    if (
      !head ||
      head.deleted ||
      head.sceneId !== ctx.sceneId ||
      head.deletionEpoch !== ref.deletionEpoch ||
      ![head.activeVersion, head.latestVersion].includes(ref.version)
    )
      return undefined
    return projectCommitment(ctx, head, this.storage.revision(ref.id, ref.version), this.authority)
  }
  list(
    ctx: CommitmentContext,
    options: { personId?: string; limit?: number; lifecycle?: 'open' | 'terminal' | 'all' } = {},
  ) {
    const limit = options.limit ?? 12
    const lifecycle = options.lifecycle ?? 'all'
    if (!['open', 'terminal', 'all'].includes(lifecycle)) throw Error('commitment_read_lifecycle')
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 12)
      throw Error('commitment_read_limit')
    if (!this.enabled(ctx)) return { items: [], coverage: 'complete' as const }
    const heads = this.storage.heads(ctx.sceneId)
    const items = heads
      .slice(0, 200)
      .filter((h) => !h.deleted)
      .flatMap((head) =>
        [...new Set([head.activeVersion ?? head.latestVersion, head.latestVersion])].map(
          (version) =>
            projectCommitment(ctx, head, this.storage.revision(head.id, version), this.authority),
        ),
      )
      .filter((item) => !options.personId || item.terms?.participantIds.includes(options.personId))
      .filter(
        (item) =>
          lifecycle === 'all' ||
          (lifecycle === 'open'
            ? ['pending', 'needs_review'].includes(item.lifecycle)
            : ['done', 'cancelled'].includes(item.lifecycle)),
      )
    return {
      items: items.slice(0, limit),
      coverage:
        heads.length > 200 || items.length > limit ? ('limited' as const) : ('complete' as const),
    }
  }
  history(ctx: CommitmentContext, ref: CommitmentRef) {
    if (!this.enabled(ctx) || !ctx.exposedRefs?.some((r) => same(r, ref))) return undefined
    const head = this.storage.head(ref.id)
    if (
      !head ||
      head.deleted ||
      head.sceneId !== ctx.sceneId ||
      head.deletionEpoch !== ref.deletionEpoch
    )
      return undefined
    return {
      revisions: this.storage.revisions(ref.id).map((revision) =>
        projectCommitment(ctx, head, revision, {
          ...this.authority,
          current: (c, r) => this.authority.historical?.(c, r),
        }),
      ),
      coverage: 'limited' as const,
      interpretation: 'previously_recorded_not_current_truth' as const,
    }
  }
  temporalHistory(
    ctx: CommitmentContext,
    anchor: CommitmentExposure,
    input: { asOfRecorded?: string; limit?: number } = {},
  ) {
    if (
      !this.enabled(ctx) ||
      !ctx.exposedRefs?.some((r) => same(r, anchor) && r.mutationVersion === anchor.mutationVersion)
    )
      return undefined
    const head = this.storage.head(anchor.id)
    if (
      !head ||
      head.deleted ||
      head.sceneId !== ctx.sceneId ||
      head.deletionEpoch !== anchor.deletionEpoch ||
      head.mutationVersion !== anchor.mutationVersion ||
      !this.current(ctx, anchor)
    )
      return undefined
    return readCommitmentTimeline(ctx, anchor, this.temporal.read(anchor.id), input, {
      revision: (v) => this.storage.revision(anchor.id, v),
      authority: this.authority,
    })
  }
  private transformTemporal(
    id: string,
    transform: (e: CommitmentEvidence) => CommitmentEvidence | undefined,
  ): CommitmentTimeline | undefined {
    const timeline = this.temporal.read(id)
    if (!timeline) return undefined
    return this.temporal.normalize({
      ...timeline,
      transitions: timeline.transitions.map((row) => {
        if (row.states === null) return row
        const states = row.states.map((state) => qualifyRecordedState(state, transform))
        return {
          ...row,
          states: states.some((state) => !state)
            ? null
            : (states as NonNullable<typeof row.states>),
        }
      }),
    })
  }
  purgeSource(sceneId: string, messageId: string) {
    this.purge((proof) =>
      proof.sources.some((source) => source.sceneId === sceneId && source.messageId === messageId),
    )
  }
  purgeAssertion(id: string) {
    this.purge((proof) => proof.parents.includes(id), id)
  }
  private mapEvidence(
    revision: CommitmentRevision,
    transform: (e: CommitmentEvidence) => CommitmentEvidence | undefined,
  ): CommitmentRevision {
    const group = (items: CommitmentEvidence[]) =>
      items.map(transform).filter((e): e is CommitmentEvidence => !!e)
    return {
      ...revision,
      evidence: group(revision.evidence),
      requiredProposalGroups: revision.requiredProposalGroups.map(group),
    }
  }
  private retainsTerms(revision: CommitmentRevision) {
    return (
      revision.evidence.some((e) => e.kind === 'proposal') &&
      revision.requiredProposalGroups.every((group) => group.length > 0)
    )
  }
  private purge(
    affected: (proof: CommitmentEvidence['proofs'][number]) => boolean,
    assertionId?: string,
  ) {
    this.storage.requireTransaction()
    for (const head of this.storage.allHeads()) {
      const prior = this.storage.revisions(head.id)
      const transform = (e: CommitmentEvidence) => {
        const proofs = e.backing.id === assertionId ? [] : e.proofs.filter((p) => !affected(p))
        return proofs.length ? { ...e, proofs } : undefined
      }
      const timeline = this.transformTemporal(head.id, transform)
      const next = prior
        .map((revision) => this.mapEvidence(revision, transform))
        .filter((revision) => this.retainsTerms(revision))
      if (
        JSON.stringify(prior) !== JSON.stringify(next) ||
        JSON.stringify(timeline) !== JSON.stringify(this.temporal.read(head.id))
      )
        this.write(
          'purge',
          { ...head, mutationVersion: head.mutationVersion + 1 },
          undefined,
          next,
          timeline,
        )
    }
  }
  /** Privacy-only authority predicate. Never pass current/time/version applicability here. */
  pruneUnavailableProof(
    ctx: CommitmentContext,
    allowed: (evidence: CommitmentEvidence, proof: CommitmentEvidence['proofs'][number]) => boolean,
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation) return
    for (const head of this.storage
      .allHeads()
      .filter((h) => h.sceneId === ctx.sceneId && !h.deleted)) {
      const prior = this.storage.revisions(head.id)
      const transform = (e: CommitmentEvidence) => {
        const proofs = e.proofs.filter((proof) =>
          allowed(structuredClone(e), structuredClone(proof)),
        )
        return proofs.length ? { ...e, proofs } : undefined
      }
      const timeline = this.transformTemporal(head.id, transform)
      const next = prior
        .map((revision) => this.mapEvidence(revision, transform))
        .filter((revision) => this.retainsTerms(revision))
      if (
        JSON.stringify(prior) !== JSON.stringify(next) ||
        JSON.stringify(timeline) !== JSON.stringify(this.temporal.read(head.id))
      )
        this.write(
          'purge',
          { ...head, mutationVersion: head.mutationVersion + 1 },
          undefined,
          next,
          timeline,
        )
    }
  }
  /** Only exact transitions captured by a trusted privacy lifecycle may renew unchanged OR survivors. */
  requalify(
    ctx: CommitmentContext,
    transitions: readonly { from: CommitmentRef; to: CommitmentRef }[],
  ) {
    this.storage.requireTransaction()
    if (ctx.simulation || !this.authority.surviving) return
    for (const head of this.storage
      .allHeads()
      .filter((h) => h.sceneId === ctx.sceneId && !h.deleted)) {
      const prior = this.storage.revisions(head.id)
      const transform = (e: CommitmentEvidence) => {
        const transition = transitions.find((t) => same(t.from, e.qualificationRef ?? e.backing))
        if (!transition) return e
        const candidate = this.authority.surviving!(ctx, e)
        if (
          !candidate ||
          candidate.ref.id !== e.backing.id ||
          !same(candidate.ref, transition.to) ||
          !eligibleAssertion(ctx, candidate.view) ||
          commitmentBackingFingerprint(candidate.view.record) !== e.backingFingerprint
        )
          return e
        const proofs = e.proofs.filter((p) =>
          candidate.view.proofs.some((v) => isDeepStrictEqual(v, p)),
        )
        return proofs.length
          ? { ...e, qualificationRef: CommitmentRefSchema.parse(candidate.ref), proofs }
          : e
      }
      const next = prior.map((revision) => this.mapEvidence(revision, transform))
      const timeline = this.transformTemporal(head.id, transform)
      if (
        JSON.stringify(prior) !== JSON.stringify(next) ||
        JSON.stringify(timeline) !== JSON.stringify(this.temporal.read(head.id))
      )
        this.write(
          'requalify',
          { ...head, mutationVersion: head.mutationVersion + 1 },
          undefined,
          next,
          timeline,
        )
    }
  }
  /** Replay only a journaled privacy marker; never reconstruct declaration bodies or use today's clock. */
  recoverTemporalMarker(raw: TemporalMarker) {
    if (!isRecoveryMaintenance(this.storage.db)) throw Error('commitment_maintenance_required')
    this.storage.requireTransaction()
    const marker = TemporalMarkerSchema.parse(raw),
      head = this.storage.head(marker.head.id)
    if (!head || head.sceneId !== marker.head.sceneId)
      throw Error('commitment_temporal_marker_unavailable')
    if (head.deleted) return
    if (
      head.deletionEpoch !== marker.head.deletionEpoch ||
      head.mutationVersion < marker.head.mutationVersion
    )
      throw Error('commitment_temporal_marker_unavailable')
    const old = this.temporal.read(head.id)
    const transition: CommitmentTransition = {
      schemaVersion: 1,
      id: head.id,
      sceneId: head.sceneId,
      mutationVersion: marker.head.mutationVersion,
      recordedAt: marker.recordedAt,
      reason: marker.reason,
      head: marker.head,
      states: null,
    }
    const previous = old?.transitions.find((t) => t.mutationVersion === transition.mutationVersion)
    if (previous) {
      if (!isDeepStrictEqual(previous, transition))
        throw Error('commitment_temporal_marker_conflict')
      return
    }
    if (old && transition.mutationVersion <= old.window.prunedThroughMutationVersion) return
    this.temporal.replace(head.id, this.temporal.append(old, transition))
  }
  recoverErase(id: string, sceneId: string, deletionEpoch?: number) {
    if (!isRecoveryMaintenance(this.storage.db)) throw Error('commitment_maintenance_required')
    this.storage.requireTransaction()
    const head = this.storage.head(id)
    if (!head) return
    if (head.sceneId !== sceneId) throw Error('commitment_scope_mismatch')
    if (head.deleted) {
      this.temporal.replace(id, undefined)
      this.storage.clear(id)
      return
    }
    this.write(
      'delete',
      {
        ...head,
        deleted: true,
        deletionEpoch: Math.max(head.deletionEpoch + 1, deletionEpoch ?? 0),
        mutationVersion: head.mutationVersion + 1,
      },
      undefined,
      [],
    )
  }
  erase(ctx: CommitmentContext, ref: CommitmentRef) {
    this.storage.requireTransaction()
    if (!this.enabled(ctx)) throw Error('commitment_unavailable')
    const head = this.storage.head(ref.id)
    if (
      !head ||
      head.sceneId !== ctx.sceneId ||
      head.deleted ||
      head.deletionEpoch !== ref.deletionEpoch
    )
      throw Error('commitment_unavailable')
    const next = {
      ...head,
      deleted: true,
      deletionEpoch: head.deletionEpoch + 1,
      mutationVersion: head.mutationVersion + 1,
    }
    this.write('delete', next, undefined, [])
    this.storage.clear(head.id)
  }
}
