import type { KnowledgeProposal, KnowledgeEdge, KnowledgeContext } from '../knowledge/contracts.ts'
import type { CommitmentCommand, CommitmentView, CommitmentExposure } from './commitments.ts'
import type { SourceEventRef } from './events.ts'
import type { RelationProposal, RelationRecord, RelationContext } from './relations.ts'
import type { ProofParentReference } from './proofs.ts'

export type EpistemicKind = 'explicit' | 'reported' | 'observed' | 'inferred' | 'hypothesis'

export type MemoryScope =
  { kind: 'scene'; sceneId: string; personId?: string } | { kind: 'sharedPublicKnowledge' }
export type MemoryKind = 'fact' | 'claim' | 'term' | 'experience' | 'summary'
export interface MemorySource {
  /** Trusted first-exposure binding; absent legacy records remain unknown. */
  eventRef?: SourceEventRef
  sceneId: string
  messageId: string
  senderId: string
}
export type CandidateSubject =
  | { kind: 'self'; sourceMessageId: string }
  | { kind: 'mentioned'; personId: string; sourceMessageId: string; mentionPartIndex: number }
  | { kind: 'unresolved' }
export interface SubjectWitness {
  kind: 'self' | 'mentioned' | 'legacy_sender' | 'unresolved'
  personId?: string
  sourceMessageId?: string
  mentionPartIndex?: number
}
export interface SourceObservation extends MemorySource {
  sentAt?: string
  receivedAt?: string
}
export interface SourceEvidence extends MemorySource {
  /** Transient exact text from the trusted runtime, never a model argument. */
  sourceText?: string
  /** Transient first-captured original parts; UTF-16 offsets use these exact text parts, never joined sourceText. */
  parts?: readonly ({ type: 'text'; text: string } | { type: 'other' })[]
  mentions?: { partIndex: number; userId: string; isBot?: boolean }[]
  sentAt?: string
  receivedAt?: string
  available: boolean
  isBot: boolean
}
export interface MemoryContext {
  sceneId: string
  simulation: boolean
  sourceEvidence?: SourceEvidence[]
  botUserId?: string
  /** Trusted extraction boundary; never populated from model arguments. */
  provenance?: 'source-only' | 'contextual'
  dependencyMemoryIds?: string[]
  /** Exact snapshots captured at trusted read handoff, never supplied by a model. */
  dependencyParentReferences?: ProofParentReference[]
  exposedEntities?: RelationContext['exposedEntities']
  exposedCommitments?: readonly CommitmentExposure[]
  exposedKnowledgeEntities?: KnowledgeContext['exposedEntities']
  exposedKnowledgeEdges?: readonly { id: string; version: number; backing: ProofParentReference }[]
  /** Trusted server-only internal declaration marker; never a candidate field. */
  internalDeclaration?: boolean | 'knowledge_declaration'
}
export interface MemoryRecord {
  internalUse?: 'commitment_declaration' | 'knowledge_declaration'
  id: string
  scope: MemoryScope
  kind: MemoryKind
  content: string
  sources: MemorySource[]
  importance: 'low' | 'medium' | 'high'
  confidence: number
  learnedAt: string
  /** Read-derived earliest trusted source receipt time; unknown remains absent. */
  observedAt?: string
  validFrom?: string
  epistemicKind?: EpistemicKind
  occurredAt?: string
  validUntil?: string
  status: 'active' | 'disputed' | 'superseded' | 'invalidated'
  locked: boolean
  origin: 'manual' | 'learned'
  supersedesId?: string
  score?: number
  /** Read projection qualification; never a model-provided write authority. */
  parentBinding?: 'bound' | 'legacy_unknown'
  lineageStatus?: 'verified' | 'unverified' | 'quarantined'
}
export interface MemoryCandidate {
  knowledge?: KnowledgeProposal[]
  commitment?: CommitmentCommand
  relations?: RelationProposal[]
  subject?: CandidateSubject
  content: string
  kind: MemoryKind
  personId?: string
  sources: MemorySource[]
  importance?: MemoryRecord['importance']
  confidence?: number
  validFrom?: string
  epistemicKind?: EpistemicKind
  occurredAt?: string
  validUntil?: string
  supersedesId?: string
  mergeIntoId?: string
}
export interface VectorSpace {
  providerId: string
  modelId: string
  apiModelName: string
  deployment: string
  dimensions: number
  preprocessing: string
  normalized: boolean
  generation?: string
  encoderRevision?: string
}
export type ProposalResult =
  | {
      status: 'saved'
      memory: MemoryRecord
      relations?: { created: RelationRecord[]; rejected: { index: number; reason: string }[] }
      knowledge?: { created: KnowledgeEdge[]; rejected: { index: number; reason: string }[] }
      commitment?: {
        status: 'saved' | 'duplicate' | 'rejected'
        reason?: string
        item?: CommitmentView
      }
    }
  | { status: 'rejected'; reason: string }
