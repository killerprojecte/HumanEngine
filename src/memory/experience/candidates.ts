import { sourceKey } from './outcomes.ts'
import {
  experienceCandidateHash,
  type ExperienceOutcome,
  type ExperiencePolicyCandidate,
  type MechanicalReceipt,
} from '../domain/experience.ts'
/** Seven days is a conservative engineering expiry, not an efficacy result. */
export const EXPERIENCE_TTL_MS = 7 * 24 * 60 * 60 * 1000
export function candidateFromOutcome(
  outcome: ExperienceOutcome,
  id: string,
  now: number,
): ExperiencePolicyCandidate {
  if (
    [
      'counterexample',
      'withdraw_instruction',
      'user_reported_helpful',
      'withdraw_person_instruction',
      'withdraw_plan_instruction',
    ].includes(outcome.code)
  )
    throw new Error('experience_counterexample_not_candidate')
  const action =
    outcome.code === 'request_exact_person_lookup'
      ? 'exact_person_before_semantic'
      : outcome.code === 'request_verify_plan_qualifiers'
        ? 'verify_plan_qualifiers'
        : 'conclusion_before_detail'
  const candidate: ExperiencePolicyCandidate = {
    id,
    version: 1,
    hash: '',
    sceneId: outcome.sceneId,
    action,
    condition:
      action === 'exact_person_before_semantic'
        ? { kind: 'person_question', personId: outcome.source.senderId }
        : action === 'verify_plan_qualifiers'
          ? { kind: 'plan_question' }
          : { kind: 'response_style', personId: outcome.source.senderId },
    status: 'candidate',
    supportIds: [outcome.id],
    counterexampleIds: [],
    generationRoots: [sourceKey(outcome.sceneId, outcome.source.messageId)],
    generationFamilies: [outcome.rootFamily],
    createdAt: new Date(now).toISOString(),
    expiresAt: new Date(now + EXPERIENCE_TTL_MS).toISOString(),
  }
  candidate.hash = experienceCandidateHash(candidate)
  return candidate
}
export function validMechanicalReceipt(
  candidate: ExperiencePolicyCandidate,
  receipt: MechanicalReceipt,
): boolean {
  if (
    receipt.candidateId !== candidate.id ||
    receipt.candidateVersion !== candidate.version ||
    receipt.candidateHash !== candidate.hash ||
    experienceCandidateHash(candidate) !== candidate.hash ||
    receipt.tier !== 'mechanical_only' ||
    !receipt.passed ||
    receipt.failures.length ||
    !receipt.checks.length ||
    !receipt.ruleHash ||
    !receipt.ruleVersion ||
    !receipt.datasetVersion ||
    !receipt.datasetHash ||
    !receipt.heldoutRoots.length ||
    !receipt.heldoutFamilies.length
  )
    return false
  const equal = (a: string[], b: string[]) =>
    JSON.stringify([...a].sort()) === JSON.stringify([...b].sort())
  return (
    equal(receipt.generationRoots, candidate.generationRoots) &&
    equal(receipt.generationFamilies, candidate.generationFamilies) &&
    !receipt.heldoutRoots.some((root) => candidate.generationRoots.includes(root)) &&
    !receipt.heldoutFamilies.some((root) => candidate.generationFamilies.includes(root))
  )
}
