import { createHash } from 'node:crypto'
import { z } from 'zod'

const identifier = z.string().min(1).max(512)
export const SourceEventIdentitySchema = z
  .object({
    sceneId: identifier,
    adapterId: identifier,
    accountId: identifier,
    messageId: identifier,
    senderId: identifier,
  })
  .strict()
export type SourceEventIdentity = z.infer<typeof SourceEventIdentitySchema>
export const SourceEventRefSchema = z
  .object({
    eventId: identifier,
    revision: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
    deletionEpoch: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
  })
  .strict()
export type SourceEventRef = z.infer<typeof SourceEventRefSchema>
const fields = {
  identity: SourceEventIdentitySchema,
  bodyHash: z.string().regex(/^[a-f0-9]{64}$/),
  occurredAt: z.iso.datetime({ offset: true }).nullable(),
}
export const TrustedSourceChangeSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('ingress'), ...fields }).strict(),
  z
    .object({
      kind: z.literal('edit'),
      ...fields,
      editId: identifier,
      order: z.discriminatedUnion('kind', [
        z
          .object({
            kind: z.literal('platform_sequence'),
            sequence: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
          })
          .strict(),
        z.object({ kind: z.literal('observed_cas'), expectedBase: SourceEventRefSchema }).strict(),
      ]),
    })
    .strict(),
])
export type TrustedSourceChange = z.infer<typeof TrustedSourceChangeSchema>
export interface SourceEventMetadata {
  identity: SourceEventIdentity
  ref: SourceEventRef
  bodyHash: string
  observedAt: string
  occurredAt: string | null
  bodyCoverage: 'unavailable'
}
export interface SourceEventMutation {
  headHash?: string
  kind: 'new' | 'edited' | 'recalled'
  identity: SourceEventIdentity
  ref: SourceEventRef
  previousRef?: SourceEventRef
  metadata?: SourceEventMetadata
}
export interface SourceEventResult {
  status:
    | 'new'
    | 'edited'
    | 'duplicate'
    | 'conflict'
    | 'unorderable'
    | 'recalled'
    | 'simulation'
    | 'capacity'
  ref?: SourceEventRef
  previousRef?: SourceEventRef
  bodyCoverage: 'unavailable'
}
export interface SourceEventEligibility {
  eligible: boolean
  reason:
    | 'current'
    | 'historical_metadata_only'
    | 'missing'
    | 'revision_changed'
    | 'recalled'
    | 'scope_mismatch'
  metadata?: SourceEventMetadata
  bodyCoverage: 'unavailable'
}
export function sourceIdentityKey(identity: SourceEventIdentity): string {
  const i = SourceEventIdentitySchema.parse(identity)
  return createHash('sha256')
    .update(JSON.stringify([i.sceneId, i.adapterId, i.accountId, i.messageId, i.senderId]))
    .digest('hex')
}
export function sameSourceRef(a: SourceEventRef, b: SourceEventRef): boolean {
  return a.eventId === b.eventId && a.revision === b.revision && a.deletionEpoch === b.deletionEpoch
}

/** Stable authority witness; metadata only, never source text. */
export function sourceEventHeadHash(
  metadata: SourceEventMetadata,
  sequence: number | null,
  orderMode: 'platform_sequence' | 'observed_cas' | null,
): string {
  return createHash('sha256')
    .update(
      JSON.stringify([
        sourceIdentityKey(metadata.identity),
        metadata.ref.eventId,
        metadata.ref.revision,
        metadata.ref.deletionEpoch,
        metadata.bodyHash,
        metadata.observedAt,
        metadata.occurredAt,
        metadata.bodyCoverage,
        sequence,
        orderMode,
      ]),
    )
    .digest('hex')
}
