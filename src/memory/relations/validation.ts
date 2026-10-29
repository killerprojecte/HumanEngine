import type {
  RelationAssertionReference,
  RelationAssertionView,
  RelationContext,
  RelationRecord,
} from '../domain/relations.ts'
export const sameReference = (a: RelationAssertionReference, b: RelationAssertionReference) =>
  a.id === b.id && a.version === b.version && a.deletionEpoch === b.deletionEpoch
export function personProofs(view: RelationAssertionView, personId: string) {
  return view.proofs.filter(
    (proof) =>
      proof.sources.length > 0 &&
      proof.subjectWitness?.personId === personId &&
      proof.subjectWitness.sourceMessageId !== undefined &&
      proof.sources.some((source) => source.messageId === proof.subjectWitness!.sourceMessageId),
  )
}
export function validBacking(
  ctx: RelationContext,
  view: RelationAssertionView | undefined,
  personId?: string,
): view is RelationAssertionView {
  return (
    !!view &&
    view.record.scope.kind === 'scene' &&
    view.record.scope.sceneId === ctx.sceneId &&
    ['active', 'disputed'].includes(view.record.status) &&
    view.record.lineageStatus !== 'quarantined' &&
    (!personId ||
      (view.record.scope.personId === personId && personProofs(view, personId).length > 0))
  )
}
/** Deliberately narrow automatic indexing, never a truth/consent assertion. */
export function autoEligible(
  record: RelationRecord,
  view: RelationAssertionView,
  now: number,
  visibleConflict: boolean,
) {
  return (
    !visibleConflict &&
    view.record.status === 'active' &&
    view.record.epistemicKind === 'explicit' &&
    record.polarity === 'positive' &&
    record.modality === 'asserted' &&
    record.temporalScope === 'current' &&
    record.conditions.length === 0 &&
    record.occurredAt === undefined &&
    record.validFrom === undefined &&
    record.validUntil === undefined &&
    (!view.record.validFrom || Date.parse(view.record.validFrom) <= now) &&
    (!view.record.validUntil || Date.parse(view.record.validUntil) > now) &&
    view.record.occurredAt === undefined &&
    record.proofs.length > 0 &&
    record.proofs.every((proof) => proof.subjectWitness?.kind === 'self')
  )
}
