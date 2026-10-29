import { z } from 'zod'
import { createHash } from 'node:crypto'
import {
  CommitmentRefSchema,
  commitmentQualificationHash,
  type CommitmentHead,
  type CommitmentRevision,
} from '../domain/commitments.ts'
import { CommitmentHeadSchema, CommitmentEvidenceSchema } from './storage.ts'
const id = z.string().min(1).max(512),
  version = z.number().int().positive().safe(),
  time = z.iso.datetime({ offset: true })
export const RecordedCommitmentStateSchema = z
  .object({
    ref: CommitmentRefSchema,
    mutationVersion: version,
    sceneId: id,
    termsHash: z
      .string()
      .regex(/^[a-f0-9]{64}$/)
      .optional(),
    termsRecordedAt: time.optional(),
    confirmation: z.enum(['proposed', 'confirmed', 'incomplete']),
    lifecycle: z.enum(['pending', 'done', 'cancelled', 'needs_review']),
    qualification: z.literal('declared_not_verified'),
    sourceBodyCoverage: z.literal('unavailable'),
    changed: z.boolean(),
    confirmedParticipantIds: z.array(id).max(8),
    missingParticipantIds: z.array(id).max(8),
    completedParticipantIds: z.array(id).max(8),
    cancelledParticipantIds: z.array(id).max(8),
    evidence: z.array(CommitmentEvidenceSchema).max(64),
    requiredProposalGroups: z
      .array(z.array(CommitmentEvidenceSchema).max(64))
      .max(64)
      .refine((groups) => groups.reduce((sum, g) => sum + g.length, 0) <= 64),
  })
  .strict()
export type RecordedCommitmentState = z.infer<typeof RecordedCommitmentStateSchema>
export const CommitmentTransitionSchema = z
  .object({
    schemaVersion: z.literal(1),
    id,
    sceneId: id,
    mutationVersion: version,
    recordedAt: time,
    reason: z.enum(['propose', 'amend', 'confirm', 'complete', 'cancel', 'purge', 'requalify']),
    head: CommitmentHeadSchema,
    states: z.array(RecordedCommitmentStateSchema).max(2).nullable(),
  })
  .strict()
  .superRefine((v, c) => {
    if (
      v.id !== v.head.id ||
      v.sceneId !== v.head.sceneId ||
      v.mutationVersion !== v.head.mutationVersion ||
      v.head.deleted
    )
      c.addIssue({ code: 'custom', message: 'commitment_transition_identity' })
    if (
      v.states?.some(
        (s) =>
          s.ref.id !== v.id ||
          s.sceneId !== v.sceneId ||
          s.ref.deletionEpoch !== v.head.deletionEpoch ||
          s.mutationVersion !== v.mutationVersion ||
          s.ref.version > v.head.latestVersion,
      )
    )
      c.addIssue({ code: 'custom', message: 'commitment_transition_identity' })
    if (v.states && new Set(v.states.map((s) => s.ref.version)).size !== v.states.length)
      c.addIssue({ code: 'custom', message: 'commitment_transition_duplicate' })
  })
export type CommitmentTransition = z.infer<typeof CommitmentTransitionSchema>
export const TemporalMarkerSchema = z
  .object({ recordedAt: time, head: CommitmentHeadSchema, reason: z.enum(['purge', 'requalify']) })
  .strict()
  .refine((v) => !v.head.deleted)
export type TemporalMarker = z.infer<typeof TemporalMarkerSchema>

export const CommitmentWindowSchema = z
  .object({
    schemaVersion: z.literal(1),
    id,
    sceneId: id,
    firstMutationVersion: version,
    firstRecordedAt: time,
    prunedThroughMutationVersion: z.number().int().nonnegative().safe(),
    clockNonmonotonic: z.boolean(),
  })
  .strict()
export type CommitmentWindow = z.infer<typeof CommitmentWindowSchema>
export interface CommitmentTimeline {
  window: CommitmentWindow
  transitions: CommitmentTransition[]
}
/** Stable complete-state witness; never hash a pre-schema object against a decoded row. */
export const temporalHash = (value: unknown): string => {
  const canonical = (v: unknown): unknown =>
    Array.isArray(v)
      ? v.map(canonical)
      : v && typeof v === 'object'
        ? Object.fromEntries(
            Object.entries(v)
              .filter(([, x]) => x !== undefined)
              .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
              .map(([k, x]) => [k, canonical(x)]),
          )
        : v
  return createHash('sha256')
    .update(JSON.stringify(canonical(value)))
    .digest('hex')
}
export function commitmentTemporalQualification(
  head: CommitmentHead,
  revisions: readonly CommitmentRevision[],
  timeline: CommitmentTimeline | undefined,
) {
  const original = commitmentQualificationHash(head, revisions)
  return timeline ? temporalHash({ original, temporal: timeline }) : original
}
/** Captured per-person declarations remain historical; loss never recomputes a different past state. */
export function qualifyRecordedState(
  state: RecordedCommitmentState,
  transform: (
    e: RecordedCommitmentState['evidence'][number],
  ) => RecordedCommitmentState['evidence'][number] | undefined,
): RecordedCommitmentState | undefined {
  const filter = (items: RecordedCommitmentState['evidence']) =>
    items.map(transform).filter((e): e is RecordedCommitmentState['evidence'][number] => !!e)
  const evidence = filter(state.evidence),
    requiredProposalGroups = state.requiredProposalGroups.map(filter)
  if (
    state.evidence.some(
      (old) => !evidence.some((e) => e.kind === old.kind && e.personId === old.personId),
    ) ||
    requiredProposalGroups.some((g) => g.length === 0)
  )
    return undefined
  return { ...state, evidence, requiredProposalGroups }
}
