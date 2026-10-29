import type { SourceEventRef } from './events.ts'
import { createHash } from 'node:crypto'

export type ExperienceAction =
  'exact_person_before_semantic' | 'verify_plan_qualifiers' | 'conclusion_before_detail'
export type ExperienceCondition =
  | { kind: 'person_question'; personId: string }
  | { kind: 'plan_question' }
  | { kind: 'response_style'; personId: string }
export type ExperienceFeedbackCode =
  | 'request_exact_person_lookup'
  | 'request_verify_plan_qualifiers'
  | 'request_conclusion_first'
  | 'counterexample'
  | 'withdraw_instruction'
  | 'user_reported_helpful'
  | 'withdraw_person_instruction'
  | 'withdraw_plan_instruction'
export interface ExperienceContext {
  sceneId: string
  simulation: boolean
}
export interface ExperienceReference {
  id: string
  version: number
}
/** Only the live feedback adapter creates this input. It contains no message body. */
export interface TrustedFeedback {
  source: { messageId: string; senderId: string; eventRef?: SourceEventRef }
  target: { messageId: string; turnId?: string; eventRef?: SourceEventRef }
  code: ExperienceFeedbackCode
  policyRef?: ExperienceReference
  observedAt: string
}
export interface ExperienceOutcome extends TrustedFeedback {
  id: string
  version: number
  rootId: string
  rootFamily: string
  sceneId: string
  snapshotBound?: true
}
export type ExperienceStatus = 'candidate' | 'validated' | 'active' | 'suspended' | 'retired'
export interface ExperiencePolicyCandidate {
  id: string
  version: number
  hash: string
  sceneId: string
  action: ExperienceAction
  condition: ExperienceCondition
  status: ExperienceStatus
  supportIds: string[]
  counterexampleIds: string[]
  generationRoots: string[]
  generationFamilies: string[]
  createdAt: string
  expiresAt: string
  reason?: string
  validationId?: string
  activationBasis?: 'explicit_preference' | 'administrator_review'
}
/** Mechanical checks establish bounded hint safety, never effectiveness. */
export interface MechanicalReceipt {
  candidateId: string
  candidateVersion: number
  candidateHash: string
  tier: 'mechanical_only'
  ruleVersion: string
  ruleHash: string
  datasetVersion: string
  datasetHash: string
  generationRoots: string[]
  generationFamilies: string[]
  heldoutRoots: string[]
  heldoutFamilies: string[]
  checks: string[]
  failures: string[]
  passed: boolean
}
export interface ExperienceHint {
  id: string
  version: number
  hash: string
  action: ExperienceAction
  condition: ExperienceCondition
  expiresAt: string
  tier: 'mechanical_only'
  eligibilityBasis?: 'explicit_instruction_trial' | 'user_reported_support'
  feedbackRevision?: number
  trialVersion?: number
  trialExpiresAt?: string
  reportedHelpfulCount?: number
}
export type ExperienceUseKind = 'exposed' | 'applied' | 'declined'
export type ExperienceOperation =
  'memory_person' | 'plan_qualifiers_checked' | 'conclusion_first_response'
export interface TrustedPolicyUse {
  turnId: string
  policyRef: ExperienceReference
  kind: ExperienceUseKind
  operation?: ExperienceOperation
  targetMessageId?: string
}
export function experienceCandidateHash(
  candidate: Omit<ExperiencePolicyCandidate, 'hash'> | ExperiencePolicyCandidate,
): string {
  return createHash('sha256')
    .update(
      JSON.stringify({
        sceneId: candidate.sceneId,
        action: candidate.action,
        condition: candidate.condition,
        supportIds: [...candidate.supportIds].sort(),
        counterexampleIds: [...candidate.counterexampleIds].sort(),
        generationRoots: [...candidate.generationRoots].sort(),
        generationFamilies: [...candidate.generationFamilies].sort(),
        createdAt: candidate.createdAt,
        expiresAt: candidate.expiresAt,
      }),
    )
    .digest('hex')
}

export interface ExperiencePolicyProposal {
  action: ExperienceAction
  condition: ExperienceCondition
  outcomeRefs: ExperienceReference[]
}
