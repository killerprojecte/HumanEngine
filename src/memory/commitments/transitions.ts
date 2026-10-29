import { commitmentBackingFingerprint } from '../domain/commitments.ts'
import { isDeepStrictEqual } from 'node:util'
import { canonicalProofKey } from '../domain/proofs.ts'
import type {
  CommitmentAuthority,
  CommitmentContext,
  CommitmentEvidence,
  CommitmentHead,
  CommitmentRevision,
  CommitmentView,
  CommitmentAssertion,
} from '../domain/commitments.ts'
export function eligibleAssertion(
  ctx: CommitmentContext,
  view: CommitmentAssertion | undefined,
): view is CommitmentAssertion {
  return (
    !!view &&
    view.record.scope.kind === 'scene' &&
    view.record.scope.sceneId === ctx.sceneId &&
    ['active', 'disputed'].includes(view.record.status) &&
    view.record.lineageStatus !== 'quarantined' &&
    view.proofs.length > 0
  )
}
export function currentEvidence(
  ctx: CommitmentContext,
  evidence: CommitmentEvidence,
  authority: CommitmentAuthority,
  historical = false,
): CommitmentEvidence | undefined {
  const view = historical
    ? authority.historical?.(ctx, evidence.backing)
    : authority.current(ctx, evidence.qualificationRef ?? evidence.backing)
  if (
    !eligibleAssertion(ctx, view) ||
    view.record.id !== evidence.backing.id ||
    commitmentBackingFingerprint(view.record) !== evidence.backingFingerprint
  )
    return undefined
  const proofs = evidence.proofs.filter((p) =>
    view.proofs.some(
      (v) =>
        v.proofId === p.proofId &&
        canonicalProofKey(v) === canonicalProofKey(p) &&
        isDeepStrictEqual(v, p),
    ),
  )
  return proofs.length ? structuredClone({ ...evidence, proofs }) : undefined
}
export function selfPerson(view: CommitmentAssertion): string | undefined {
  if (view.record.epistemicKind !== 'explicit' || view.record.status !== 'active') return undefined
  const person = view.record.scope.kind === 'scene' ? view.record.scope.personId : undefined
  if (!person) return undefined
  return view.proofs.some(
    (p) =>
      p.subjectWitness?.kind === 'self' &&
      p.subjectWitness.personId === person &&
      p.sources.some(
        (s) => s.senderId === person && s.messageId === p.subjectWitness?.sourceMessageId,
      ),
  )
    ? person
    : undefined
}
export function canConfirm(revision: CommitmentRevision) {
  return (
    revision.terms.polarity === 'positive' &&
    revision.terms.modality === 'asserted' &&
    revision.terms.conditions.length === 0 &&
    revision.terms.temporalScope !== 'past' &&
    revision.terms.temporalScope !== 'unspecified'
  )
}
export function projectCommitment(
  ctx: CommitmentContext,
  head: CommitmentHead,
  revision: CommitmentRevision | undefined,
  authority: CommitmentAuthority,
): CommitmentView {
  const evidence =
    revision?.evidence
      .map((e) => currentEvidence(ctx, e, authority))
      .filter((e): e is CommitmentEvidence => !!e) ?? []
  const requiredProposalGroups =
    revision?.requiredProposalGroups.map((group) =>
      group
        .map((e) => currentEvidence(ctx, e, authority))
        .filter((e): e is CommitmentEvidence => !!e),
    ) ?? []
  const proposed =
    evidence.some((e) => e.kind === 'proposal') &&
    requiredProposalGroups.every((group) => group.length > 0)
  const participants = revision?.terms.participantIds ?? []
  const ids = (kind: CommitmentEvidence['kind']) => [
    ...new Set(
      evidence
        .filter((e) => e.kind === kind && e.personId && participants.includes(e.personId))
        .map((e) => e.personId!),
    ),
  ]
  const confirmed = ids('confirm'),
    complete = ids('complete'),
    cancelled = ids('cancel')
  const full =
    !!revision &&
    proposed &&
    canConfirm(revision) &&
    participants.every((p) => confirmed.includes(p))
  const changed = head.everActivatedVersion !== null
  const terminal =
    head.everTerminal?.version === (revision?.ref.version ?? head.activeVersion)
      ? head.everTerminal
      : undefined
  const actualTerminal =
    full && cancelled.length
      ? 'cancelled'
      : full && participants.every((p) => complete.includes(p))
        ? 'done'
        : undefined
  const lostTerminal = !!terminal && terminal.state !== actualTerminal
  return {
    ref: revision?.ref ?? {
      id: head.id,
      version: head.activeVersion ?? head.latestVersion,
      deletionEpoch: head.deletionEpoch,
    },
    mutationVersion: head.mutationVersion,
    sceneId: head.sceneId,
    ...(revision && proposed
      ? { terms: structuredClone(revision.terms), recordedAt: revision.recordedAt }
      : {}),
    confirmation: lostTerminal
      ? 'incomplete'
      : full
        ? 'confirmed'
        : (changed && head.activeVersion === revision?.ref.version) || (changed && !revision)
          ? 'incomplete'
          : 'proposed',
    lifecycle: lostTerminal ? 'needs_review' : (actualTerminal ?? 'pending'),
    qualification: 'declared_not_verified',
    sourceBodyCoverage: 'unavailable',
    changed,
    confirmedParticipantIds: confirmed,
    missingParticipantIds: participants.filter((p) => !confirmed.includes(p)),
    completedParticipantIds: complete,
    cancelledParticipantIds: cancelled,
    evidence,
    requiredProposalGroups,
  }
}
