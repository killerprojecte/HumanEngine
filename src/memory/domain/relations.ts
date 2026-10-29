import { z } from 'zod'
import type { MemoryRecord } from './contracts.ts'
import type { SupportProof } from './proofs.ts'
export const RelationProposalSchema = z
  .object({
    predicate: z.literal('participates_in'),
    modality: z.enum(['asserted', 'possible']),
    temporalScope: z.enum(['current', 'past', 'future', 'unspecified']),
    personId: z.string().min(1).max(512),
    target: z.discriminatedUnion('kind', [
      z
        .object({
          kind: z.literal('new'),
          entityKind: z.enum(['plan', 'event']),
          title: z.string().min(1).max(500),
        })
        .strict(),
      z.object({ kind: z.literal('existing'), entityId: z.string().min(1).max(512) }).strict(),
    ]),
    polarity: z.enum(['positive', 'negative']),
    conditions: z.array(z.string().min(1).max(2000)).max(8),
    occurredAt: z.iso.datetime({ offset: true }).optional(),
    validFrom: z.iso.datetime({ offset: true }).optional(),
    validUntil: z.iso.datetime({ offset: true }).optional(),
  })
  .strict()
  .refine(
    (value) =>
      !value.validFrom ||
      !value.validUntil ||
      Date.parse(value.validFrom) <= Date.parse(value.validUntil),
    { message: 'invalid_relation_time_interval' },
  )
export type RelationProposal = z.infer<typeof RelationProposalSchema>
export interface RelationAssertionReference {
  id: string
  version: number
  deletionEpoch: number
}
export interface RelationEntity {
  id: string
  sceneId: string
  kind: 'plan' | 'event'
  title: string
  backing: RelationAssertionReference
}
export interface RelationProof extends SupportProof {
  proofId: string
}
export interface RelationAssertionView {
  record: MemoryRecord
  proofs: RelationProof[]
}
export interface RelationContext {
  sceneId: string
  simulation: boolean
  provenance: 'source-only' | 'contextual'
  exposedEntities?: readonly { entityId: string; backingRef: RelationAssertionReference }[]
}
export interface RelationRecord {
  id: string
  version: number
  sceneId: string
  predicate: 'participates_in'
  modality: 'asserted' | 'possible'
  temporalScope: 'current' | 'past' | 'future' | 'unspecified'
  personId: string
  target: RelationEntity
  polarity: 'positive' | 'negative'
  conditions: string[]
  occurredAt?: string
  validFrom?: string
  validUntil?: string
  epistemicKind?: MemoryRecord['epistemicKind']
  backing: RelationAssertionReference
  proofs: RelationProof[]
  status: 'candidate' | 'accepted' | 'rejected'
  admission: 'automatic' | 'candidate' | 'reviewed'
  autoReason?: 'self_explicit_no_visible_conflict'
  createdAt: string
  reviewedAt?: string
  actorId?: string
}
export interface RelationAuthority {
  /** Exact current authorized snapshot, including complete currently valid proof alternatives. */
  current(
    ctx: RelationContext,
    reference: RelationAssertionReference,
  ): RelationAssertionView | undefined
  enabled(ctx: RelationContext): boolean
  autoAdmission(ctx: RelationContext): boolean
  learningEnabled(ctx: RelationContext): boolean
}
