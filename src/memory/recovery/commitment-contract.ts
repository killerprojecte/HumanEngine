import { TemporalMarkerSchema } from '../commitments/temporal-contracts.ts'
import { z } from 'zod'
const id = z.string().min(1).max(512)
const counter = z.number().int().nonnegative().safe()
const common = { schemaVersion: z.literal(1), sceneId: id, id, deletionEpoch: counter }
/** Exact snapshot qualification only; never contains terms or participant-confirmation text. */
export const CommitmentRecoveryEventSchema = z
  .discriminatedUnion('kind', [
    z
      .object({
        ...common,
        schemaVersion: z.union([z.literal(1), z.literal(2)]),
        temporalVersion: z.literal(1).optional(),
        privacyMarker: TemporalMarkerSchema.optional(),
        kind: z.literal('commitment_head'),
        termsVersion: counter.positive(),
        mutationVersion: counter.positive(),
        qualificationHash: z.string().regex(/^[a-f0-9]{64}$/),
      })
      .strict(),
    z.object({ ...common, kind: z.literal('commitment_delete') }).strict(),
  ])
  .superRefine((event, ctx) => {
    if (
      event.kind === 'commitment_head' &&
      (event.schemaVersion === 2) !== (event.temporalVersion === 1)
    )
      ctx.addIssue({ code: 'custom', message: 'invalid_temporal_version' })
    if (
      event.kind === 'commitment_head' &&
      event.privacyMarker &&
      (event.schemaVersion !== 2 ||
        event.privacyMarker.head.id !== event.id ||
        event.privacyMarker.head.sceneId !== event.sceneId ||
        event.privacyMarker.head.mutationVersion !== event.mutationVersion ||
        event.privacyMarker.head.latestVersion !== event.termsVersion ||
        event.privacyMarker.head.deletionEpoch !== event.deletionEpoch)
    )
      ctx.addIssue({ code: 'custom', message: 'invalid_temporal_marker' })
  })
export type CommitmentRecoveryEvent = z.infer<typeof CommitmentRecoveryEventSchema>
export function parseCommitmentRecoveryEvent(value: unknown): CommitmentRecoveryEvent {
  const parsed = CommitmentRecoveryEventSchema.safeParse(value)
  if (!parsed.success) throw new Error('recovery_commitment_event_invalid')
  return parsed.data
}
