import { createHash } from 'node:crypto'
import { z } from 'zod'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { RelationProof } from '../domain/relations.ts'
import { SourceEventRefSchema } from '../domain/events.ts'
import { EntityKindSchema, KnowledgeProposalSchema } from '../knowledge/contracts.ts'
import { parentBindingState } from '../domain/proofs.ts'
const id = z.string().min(1).max(512)
const count = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER)
const hash = z.string().regex(/^[a-f0-9]{64}$/)
const time = z.iso.datetime({ offset: true })
export const SemanticRefSchema = z
  .object({ id, version: count.min(1), deletionEpoch: count })
  .strict()
export type SemanticRef = z.infer<typeof SemanticRefSchema>
export const SemanticSelectionSchema = z
  .object({ ref: SemanticRefSchema, dependencyKey: hash })
  .strict()
export type SemanticSelection = z.infer<typeof SemanticSelectionSchema>
const source = z
  .object({ sceneId: id, messageId: id, senderId: id, eventRef: SourceEventRefSchema.optional() })
  .strict()
export const SemanticProofSchema = z
  .object({
    proofId: id,
    sources: z.array(source).max(128),
    parents: z.array(id).max(128),
    parentReferences: z.array(SemanticRefSchema).max(128).optional(),
    subjectWitness: z
      .object({
        kind: z.enum(['self', 'mentioned', 'legacy_sender', 'unresolved']),
        personId: id.optional(),
        sourceMessageId: id.optional(),
        mentionPartIndex: count.optional(),
      })
      .strict()
      .optional(),
    sourceObservations: z
      .array(source.extend({ sentAt: time.optional(), receivedAt: time.optional() }))
      .max(128)
      .optional(),
    invalidatedBy: id.optional(),
  })
  .strict()
  .refine((p) => parentBindingState(p) !== 'invalid')
export const SemanticProposalSchema = z
  .object({
    kind: z.enum(['summary_candidate', 'duplicate_candidate', 'episode_candidate']),
    selectedParentIds: z.array(id).min(2).max(12),
    text: z.string().min(1).max(6000),
    episodeAnchor: z
      .object({ id, version: count.min(1) })
      .strict()
      .optional(),
    hypothesis: z.boolean(),
  })
  .strict()
  .superRefine((value, ctx) => {
    if ((value.kind === 'episode_candidate') !== !!value.episodeAnchor)
      ctx.addIssue({ code: 'custom', message: 'semantic_episode_anchor_required' })
    if (new Set(value.selectedParentIds).size !== value.selectedParentIds.length)
      ctx.addIssue({ code: 'custom', message: 'semantic_duplicate_parent' })
  })
export type SemanticProposal = z.infer<typeof SemanticProposalSchema>
export const SemanticParentSchema = SemanticSelectionSchema.extend({
  fingerprint: hash,
  proofs: z.array(SemanticProofSchema).min(1).max(128),
  roots: z.array(hash).min(1).max(128),
}).strict()
export type SemanticParent = z.infer<typeof SemanticParentSchema>
export const SemanticAnchorDependencySchema = z
  .object({
    ref: SemanticRefSchema,
    fingerprint: hash,
    proofs: z.array(SemanticProofSchema).min(1).max(128),
  })
  .strict()
export type SemanticAnchorDependency = z.infer<typeof SemanticAnchorDependencySchema>
const anchorEndpoint = z.union([
  z.object({ kind: z.literal('person'), personId: id }).strict(),
  z
    .object({
      kind: z.literal('entity'),
      entityId: id,
      entityKind: EntityKindSchema,
      backing: SemanticRefSchema,
      originEdgeId: id,
    })
    .strict(),
  z
    .object({
      kind: z.literal('literal'),
      valueType: z.literal('text'),
      value: z.string().min(1).max(2000),
    })
    .strict(),
  z
    .object({
      kind: z.literal('literal'),
      valueType: z.literal('number'),
      value: z.number().finite(),
      unit: z.string().min(1).max(80).nullable(),
    })
    .strict(),
  z
    .object({ kind: z.literal('literal'), valueType: z.literal('boolean'), value: z.boolean() })
    .strict(),
])
export const SemanticAnchorQualificationSchema = z
  .object({
    edgeId: id,
    edgeVersion: count.min(1),
    registryVersion: z.literal(1),
    predicate: KnowledgeProposalSchema.shape.predicate,
    property: KnowledgeProposalSchema.shape.property,
    subject: anchorEndpoint,
    object: anchorEndpoint,
    polarity: KnowledgeProposalSchema.shape.polarity,
    modality: KnowledgeProposalSchema.shape.modality,
    temporalScope: KnowledgeProposalSchema.shape.temporalScope,
    conditions: KnowledgeProposalSchema.shape.conditions,
    occurredAt: time.nullable(),
    validFrom: time.nullable(),
    validUntil: time.nullable(),
    status: z.enum(['candidate', 'accepted']),
    admission: z.enum([
      'qualified_candidate',
      'self_explicit_no_visible_conflict',
      'direct_assertion_no_visible_conflict',
    ]),
    interpretation: z.literal('attributed_proposed_association_not_observed_event'),
  })
  .strict()
export type SemanticAnchorQualification = z.infer<typeof SemanticAnchorQualificationSchema>
export const SemanticCandidateSchema = z
  .object({
    schemaVersion: z.literal(1),
    id,
    sceneId: id,
    personId: id.nullable(),
    partitionKey: hash.nullable(),
    version: count.min(1),
    inputKey: hash,
    kind: z.enum(['summary_candidate', 'duplicate_candidate', 'episode_candidate']),
    text: z.string().min(1).max(6000),
    selectedParentIds: z.array(id).min(2).max(12),
    parents: z.array(SemanticParentSchema).min(2).max(12),
    episodeAnchor: z
      .object({ id, version: count.min(1) })
      .strict()
      .optional(),
    episodeMembershipKey: hash.optional(),
    anchorDependencies: z.array(SemanticAnchorDependencySchema).max(128),
    anchorQualifications: z.array(SemanticAnchorQualificationSchema).max(40),
    hypothesis: z.boolean(),
    status: z.enum(['candidate', 'admitted', 'rejected']),
    admission: z.enum([
      'qualified_candidate',
      'structural_low_risk_summary',
      'administrator_review',
    ]),
    createdAt: time,
    applicableUntil: time.nullable(),
  })
  .strict()
  .superRefine((value, ctx) => {
    const ids = new Set(value.parents.map((p) => p.ref.id))
    if (
      ids.size !== value.parents.length ||
      value.selectedParentIds.some((id) => !ids.has(id)) ||
      new Set(value.selectedParentIds).size !== value.selectedParentIds.length
    )
      ctx.addIssue({ code: 'custom', message: 'semantic_parent_membership' })
    if ((value.kind === 'episode_candidate') !== (value.partitionKey === null))
      ctx.addIssue({ code: 'custom', message: 'semantic_partition_key_invalid' })
    if ((value.kind === 'episode_candidate') !== value.anchorQualifications.length > 0)
      ctx.addIssue({ code: 'custom', message: 'semantic_anchor_qualifications_required' })
    if (
      new Set(value.anchorQualifications.map((q) => q.edgeId)).size !==
      value.anchorQualifications.length
    )
      ctx.addIssue({ code: 'custom', message: 'semantic_anchor_qualifications_duplicate' })
    if ((value.kind === 'episode_candidate') !== value.anchorDependencies.length > 0)
      ctx.addIssue({ code: 'custom', message: 'semantic_anchor_dependencies_required' })
    if ((value.kind === 'episode_candidate') !== !!value.episodeMembershipKey)
      ctx.addIssue({ code: 'custom', message: 'semantic_episode_membership_required' })
    if ((value.kind === 'episode_candidate') !== !!value.episodeAnchor)
      ctx.addIssue({ code: 'custom', message: 'semantic_episode_anchor_required' })
  })
export type SemanticCandidate = z.infer<typeof SemanticCandidateSchema>
export interface SemanticContext {
  sceneId: string
  simulation: boolean
}
export interface SemanticAssertion {
  record: MemoryRecord
  proofs: RelationProof[]
  dependencyKey: string
  /** Complete original event-root closure, computed by trusted provenance traversal. */
  roots: string[]
  rootCoverage: 'complete' | 'limited'
  derived: boolean
}
export interface SemanticAuthority {
  assertReady(): void
  enabled(ctx: SemanticContext): boolean
  learningEnabled(ctx: SemanticContext): boolean
  autoSummary(ctx: SemanticContext): boolean
  partitionKey(ctx: SemanticContext, personId: string | null): string | undefined
  current(ctx: SemanticContext, ref: SemanticRef): SemanticAssertion | undefined
  anchorCurrent(
    ctx: SemanticContext,
    ref: SemanticRef,
  ): { record: MemoryRecord; proofs: RelationProof[] } | undefined
  /** Every supplied original parent must independently share this exact authorized anchor. */
  episodeAnchor(
    ctx: SemanticContext,
    anchor: { id: string; version: number },
    parents: readonly SemanticRef[],
    expectedMembershipKey: string,
  ): boolean
}
export interface SemanticMutation {
  operation: 'admit' | 'review' | 'privacy' | 'attempt'
  id: string
  sceneId: string
  previousVersion: number | null
  nextVersion: number | null
  nextState?: { status: SemanticCandidate['status']; admission: SemanticCandidate['admission'] }
  beforeHash: string | null
  afterHash: string | null
}
export interface SemanticView {
  candidate: SemanticCandidate
  declarations: { ref: SemanticRef; record: MemoryRecord }[]
  roots: string[]
  applicability: 'current' | 'review_needed'
  interpretation:
    | 'attributed_summary_not_verified_truth'
    | 'possible_duplicate_not_merged'
    | 'grouped_declarations_not_observed_event'
}
export const semanticHash = (value: unknown) =>
  createHash('sha256').update(JSON.stringify(value)).digest('hex')
export const semanticFingerprint = (record: MemoryRecord) =>
  semanticHash([
    record.id,
    record.scope,
    record.content,
    record.kind,
    record.epistemicKind ?? null,
    record.occurredAt ?? null,
    record.validFrom ?? null,
    record.validUntil ?? null,
    record.internalUse ?? null,
  ])

export const SemanticAttemptSchema = z
  .object({
    schemaVersion: z.literal(1),
    sceneId: id,
    inputKey: hash,
    status: z.enum(['started', 'finished', 'abandoned']),
    version: count.min(1),
    parents: z.array(SemanticSelectionSchema).max(12),
    roots: z.array(hash).max(1536),
  })
  .strict()
export type SemanticAttempt = z.infer<typeof SemanticAttemptSchema>

/** Trusted pre-recall maintenance snapshot; not a model supplied admission field. */
export interface SemanticQualification {
  id: string
  inputKey: string
  definitionHash: string
  partitionKey: string | null
  parents: SemanticSelection[]
}
export const semanticDefinitionHash = (candidate: SemanticCandidate) =>
  semanticHash([
    candidate.id,
    candidate.sceneId,
    candidate.personId,
    candidate.inputKey,
    candidate.kind,
    candidate.text,
    candidate.selectedParentIds,
    candidate.episodeAnchor ?? null,
    candidate.episodeMembershipKey ?? null,
    candidate.anchorQualifications,
    candidate.hypothesis,
    candidate.status,
    candidate.admission,
    candidate.createdAt,
    candidate.applicableUntil,
  ])

export interface SemanticTrustedEpisode {
  anchor: { id: string; version: number }
  membershipKey: string
  anchorDependencies: SemanticAnchorDependency[]
  anchorQualifications: SemanticAnchorQualification[]
}
