import { z } from 'zod'
import type { MemoryRecord, MemorySource, SourceEvidence } from '../domain/contracts.ts'
import type { RelationProof } from '../domain/relations.ts'
const id = z.string().min(1).max(512)
export const KnowledgeRefSchema = z
  .object({
    id,
    version: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
    deletionEpoch: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
  })
  .strict()
export type KnowledgeRef = z.infer<typeof KnowledgeRefSchema>
export const EntityKindSchema = z.enum([
  'plan',
  'event',
  'topic',
  'tool',
  'organization',
  'concept',
  'task',
])
const newEntity = z
  .object({
    kind: z.literal('new'),
    entityKind: EntityKindSchema,
    title: z.string().min(1).max(500),
  })
  .strict()
const existing = z.object({ kind: z.literal('existing'), entityId: id }).strict()
const literal = z.discriminatedUnion('valueType', [
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
export const KnowledgeProposalSchema = z
  .object({
    registryVersion: z.literal(1),
    predicate: z.enum([
      'participates_in',
      'prefers_topic',
      'uses_tool',
      'member_of',
      'part_of',
      'depends_on',
      'has_property',
    ]),
    property: z.enum(['version', 'count', 'duration', 'enabled']).optional(),
    subject: z.union([
      z.object({ kind: z.literal('self'), personId: id }).strict(),
      newEntity,
      existing,
    ]),
    object: z.union([newEntity, existing, literal]),
    polarity: z.enum(['positive', 'negative']),
    modality: z.enum(['asserted', 'possible']),
    temporalScope: z.enum(['current', 'past', 'future', 'unspecified']),
    conditions: z.array(z.string().min(1).max(2000)).max(8),
    validFrom: z.iso.datetime({ offset: true }).optional(),
    validUntil: z.iso.datetime({ offset: true }).optional(),
    sourceSpans: z
      .array(
        z
          .object({
            messageId: id,
            partIndex: z.number().int().min(0).max(4095),
            start: z.number().int().min(0).max(1000000),
            end: z.number().int().positive().max(1000000),
            stance: z.enum(['self', 'asserted', 'reported', 'quoted']),
          })
          .strict(),
      )
      .min(1)
      .max(8),
  })
  .strict()
  .refine(
    (p) => !p.validFrom || !p.validUntil || Date.parse(p.validFrom) <= Date.parse(p.validUntil),
    'knowledge_invalid_time',
  )
  .refine(
    (p) => (p.predicate === 'has_property') === (p.property !== undefined),
    'knowledge_property_required',
  )
export type KnowledgeProposal = z.infer<typeof KnowledgeProposalSchema>
export interface KnowledgeEntity {
  id: string
  sceneId: string
  kind: z.infer<typeof EntityKindSchema>
  title: string
  backing: KnowledgeRef
  originEdgeId: string
}
export type KnowledgeEndpoint =
  | { kind: 'person'; personId: string }
  | { kind: 'entity'; entity: KnowledgeEntity }
  | z.infer<typeof literal>
export interface KnowledgeSpan {
  source: MemorySource
  rootId: string
  partIndex: number
  start: number
  end: number
  spanHash: string
  stance: 'self' | 'asserted' | 'reported' | 'quoted'
}
export interface KnowledgeDependency {
  backingFingerprint: string
  entity: KnowledgeEntity
  proofs: RelationProof[]
}
export interface KnowledgeReadDependency {
  edgeId?: string
  edgeVersion?: number
  backing: KnowledgeRef
  backingFingerprint: string
  proofs: RelationProof[]
}
export interface KnowledgeEdge {
  id: string
  version: number
  sceneId: string
  registryVersion: 1
  predicate: KnowledgeProposal['predicate']
  property?: KnowledgeProposal['property']
  subject: KnowledgeEndpoint
  object: KnowledgeEndpoint
  polarity: KnowledgeProposal['polarity']
  modality: KnowledgeProposal['modality']
  temporalScope: KnowledgeProposal['temporalScope']
  conditions: string[]
  validFrom?: string
  validUntil?: string
  backing: KnowledgeRef
  backingFingerprint: string
  proofs: RelationProof[]
  spans: KnowledgeSpan[]
  dependencies: KnowledgeDependency[]
  readDependencies: KnowledgeReadDependency[]
  status: 'candidate' | 'accepted'
  admission:
    | 'qualified_candidate'
    | 'self_explicit_no_visible_conflict'
    | 'direct_assertion_no_visible_conflict'
  admissionPolicyVersion: 1
  createdAt: string
}
export interface KnowledgeContext {
  sceneId: string
  simulation: boolean
  provenance: 'source-only' | 'contextual'
  sourceEvidence?: readonly SourceEvidence[]
  exposedAssertions?: readonly KnowledgeRef[]
  exposedEdges?: readonly { id: string; version: number; backing: KnowledgeRef }[]
  exposedEntities?: readonly { entityId: string; backingRef: KnowledgeRef }[]
}
export interface KnowledgeAuthority {
  assertReady(): void
  current(
    ctx: KnowledgeContext,
    ref: KnowledgeRef,
  ): { record: MemoryRecord; proofs: RelationProof[] } | undefined
  source(
    ctx: KnowledgeContext,
    messageId: string,
  ):
    | {
        source: MemorySource
        parts: readonly ({ type: 'text'; text: string } | { type: 'other' })[]
      }
    | undefined
  enabled(ctx: KnowledgeContext): boolean
  learningEnabled(ctx: KnowledgeContext): boolean
  autoAdmission(ctx: KnowledgeContext): boolean
}
export interface KnowledgeQuery {
  anchor:
    | { kind: 'person'; personId: string }
    | { kind: 'entity'; entityId: string; backingRef: KnowledgeRef }
  predicates: KnowledgeProposal['predicate'][]
  depth: 1 | 2
}
export type KnowledgeEdgeView = KnowledgeEdge & {
  requiresPersonalization: boolean
  backingStatus: MemoryRecord['status']
  dependencyStatuses: { entityId: string; status: MemoryRecord['status'] }[]
  applicability: 'current' | 'future' | 'expired' | 'past' | 'unspecified'
}
export interface KnowledgeQueryResult {
  supportClosure: {
    edges: KnowledgeEdgeView[]
    records: { ref: KnowledgeRef; record: MemoryRecord }[]
  }
  edges: KnowledgeEdgeView[]
  paths: string[][]
  coverage: 'complete' | 'limited'
  reason?: 'scan_limit' | 'edge_limit' | 'path_limit' | 'dependency_limit'
  interpretation: 'attributed_claims_not_transitive_truth'
}
