import { createHash } from 'node:crypto'
import { z } from 'zod'
import type { MemoryRecord } from './contracts.ts'
import type { RelationProof } from './relations.ts'
const id = z.string().min(1).max(512)
export const CommitmentRefSchema = z
  .object({
    id,
    version: z.number().int().positive().safe(),
    deletionEpoch: z.number().int().nonnegative().safe(),
  })
  .strict()
export type CommitmentRef = z.infer<typeof CommitmentRefSchema>
export type CommitmentExposure = CommitmentRef & { mutationVersion: number }
export const CommitmentTermsSchema = z
  .object({
    kind: z.literal('plan'),
    title: z.string().min(1).max(500),
    participantIds: z
      .array(id)
      .min(1)
      .max(8)
      .refine((ids) => new Set(ids).size === ids.length),
    temporalScope: z.enum(['current', 'past', 'future', 'unspecified']),
    polarity: z.enum(['positive', 'negative']),
    modality: z.enum(['asserted', 'possible']),
    conditions: z.array(z.string().min(1).max(2000)).max(8),
    timeQualification: z.string().min(1).max(500).optional(),
    occurredAt: z.iso.datetime({ offset: true }).optional(),
    validFrom: z.iso.datetime({ offset: true }).optional(),
    validUntil: z.iso.datetime({ offset: true }).optional(),
  })
  .strict()
  .refine(
    (t) => !t.validFrom || !t.validUntil || Date.parse(t.validFrom) <= Date.parse(t.validUntil),
  )
export type CommitmentTerms = z.infer<typeof CommitmentTermsSchema>
export const CommitmentCommandSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('propose'), terms: CommitmentTermsSchema }).strict(),
  z
    .object({ kind: z.literal('amend'), target: CommitmentRefSchema, terms: CommitmentTermsSchema })
    .strict(),
  z
    .object({ kind: z.enum(['confirm', 'complete', 'cancel']), target: CommitmentRefSchema })
    .strict(),
])
export type CommitmentCommand = z.infer<typeof CommitmentCommandSchema>
export interface CommitmentContext {
  sceneId: string
  simulation: boolean
  provenance: 'source-only' | 'contextual'
  exposedRefs?: readonly CommitmentExposure[]
}
export interface CommitmentAssertion {
  record: MemoryRecord
  proofs: RelationProof[]
}
export interface CommitmentEvidence {
  backing: CommitmentRef
  qualificationRef?: CommitmentRef
  backingFingerprint: string
  proofs: RelationProof[]
  personId?: string
  kind: 'proposal' | 'confirm' | 'complete' | 'cancel'
}
export interface CommitmentRevision {
  ref: CommitmentRef
  sceneId: string
  terms: CommitmentTerms
  recordedAt: string
  evidence: CommitmentEvidence[]
  requiredProposalGroups: CommitmentEvidence[][]
}
export interface CommitmentHead {
  id: string
  sceneId: string
  latestVersion: number
  activeVersion: number | null
  everActivatedVersion: number | null
  everTerminal: { version: number; state: 'done' | 'cancelled' } | null
  deletionEpoch: number
  mutationVersion: number
  deleted: boolean
}
export interface CommitmentView {
  ref: CommitmentRef
  mutationVersion: number
  sceneId: string
  terms?: CommitmentTerms
  recordedAt?: string
  confirmation: 'proposed' | 'confirmed' | 'incomplete'
  lifecycle: 'pending' | 'done' | 'cancelled' | 'needs_review'
  qualification: 'declared_not_verified'
  sourceBodyCoverage: 'unavailable'
  changed: boolean
  confirmedParticipantIds: string[]
  missingParticipantIds: string[]
  completedParticipantIds: string[]
  cancelledParticipantIds: string[]
  evidence: CommitmentEvidence[]
  requiredProposalGroups: CommitmentEvidence[][]
}
export interface CommitmentPrivacyMarker {
  recordedAt: string
  reason: 'purge' | 'requalify'
  head: CommitmentHead
}
export interface CommitmentMutation {
  privacyMarker?: CommitmentPrivacyMarker
  temporalVersion?: 1
  kind: 'propose' | 'amend' | 'confirm' | 'complete' | 'cancel' | 'purge' | 'requalify' | 'delete'
  head: CommitmentHead
  qualificationHash: string
}
export interface CommitmentAuthority {
  enabled(ctx: CommitmentContext): boolean
  learningEnabled(ctx: CommitmentContext): boolean
  current(ctx: CommitmentContext, ref: CommitmentRef): CommitmentAssertion | undefined
  historical?(ctx: CommitmentContext, ref: CommitmentRef): CommitmentAssertion | undefined
  /** Canonical people validated from this exact assertion's trusted source/mention witnesses. */
  people(ctx: CommitmentContext, ref: CommitmentRef): readonly string[]
  surviving?(
    ctx: CommitmentContext,
    evidence: CommitmentEvidence,
  ): { ref: CommitmentRef; view: CommitmentAssertion } | undefined
  beforeWrite?(mutation: CommitmentMutation): void
  /** Same transaction, after persisted final head and revisions; must not await. */
  afterWrite?(
    head: CommitmentHead,
    revisions: readonly CommitmentRevision[],
    priorRevisions: readonly CommitmentRevision[],
  ): void
}

export function commitmentQualificationHash(
  head: CommitmentHead,
  revisions: readonly CommitmentRevision[],
): string {
  const canonical = (value: unknown): unknown =>
    Array.isArray(value)
      ? value.map(canonical)
      : value && typeof value === 'object'
        ? Object.fromEntries(
            Object.entries(value)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => [k, canonical(v)]),
          )
        : value
  return createHash('sha256')
    .update(
      JSON.stringify(
        canonical({
          head,
          revisions: [...revisions].sort((a, b) => a.ref.version - b.ref.version),
        }),
      ),
    )
    .digest('hex')
}

export function commitmentBackingFingerprint(record: MemoryRecord): string {
  return createHash('sha256')
    .update(
      JSON.stringify([
        record.id,
        record.scope,
        record.content,
        record.kind,
        record.epistemicKind ?? null,
        record.occurredAt ?? null,
        record.validFrom ?? null,
        record.validUntil ?? null,
      ]),
    )
    .digest('hex')
}
