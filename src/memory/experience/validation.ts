import { createHash } from 'node:crypto'
import {
  experienceCandidateHash,
  type ExperienceAction,
  type ExperienceCondition,
  type ExperiencePolicyCandidate,
  type MechanicalReceipt,
} from '../domain/experience.ts'
import { isExperienceActionCondition, renderExperienceAction } from './policy-actions.ts'

import { experienceBehaviorHoldouts, runExperienceBehaviorHoldouts } from './behavior-holdouts.ts'

const digest = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const ruleVersion = 'experience-mechanical-v2'
const datasetVersion = 'experience-fixed-behavior-holdout-v2'
// Engineering holdouts test protocol safety, not independent real-world learning effectiveness.
const fixtures: {
  id: string
  action: ExperienceAction
  condition: ExperienceCondition
  expected: boolean
}[] = [
  {
    id: 'exact-person',
    action: 'exact_person_before_semantic',
    condition: { kind: 'person_question', personId: 'holdout-person' },
    expected: true,
  },
  {
    id: 'plan-qualifiers',
    action: 'verify_plan_qualifiers',
    condition: { kind: 'plan_question' },
    expected: true,
  },
  {
    id: 'style-scoped',
    action: 'conclusion_before_detail',
    condition: { kind: 'response_style', personId: 'holdout-person' },
    expected: true,
  },
  {
    id: 'cross-condition',
    action: 'conclusion_before_detail',
    condition: { kind: 'plan_question' },
    expected: false,
  },
  {
    id: 'person-unresolved',
    action: 'exact_person_before_semantic',
    condition: { kind: 'person_question', personId: '' },
    expected: false,
  },
]
const checks = [
  'candidate_shape',
  'exact_semantic_hash',
  'action_condition_whitelist',
  'support_lineage',
  'holdout_root_disjoint',
  'holdout_family_disjoint',
  'bounded_expiry',
  'no_counterexamples',
  'fixed_protocol_holdouts',
  'fixed_operation_behavior_holdouts',
  'fixed_non_authoritative_templates',
]
const actions: ExperienceAction[] = [
  'exact_person_before_semantic',
  'verify_plan_qualifiers',
  'conclusion_before_detail',
]
const datasetHash = digest({ protocol: fixtures, behavior: experienceBehaviorHoldouts })
const ruleHash = digest({
  ruleVersion,
  checks,
  templates: actions.map((action) => [action, renderExperienceAction(action)]),
})
const strings = (values: unknown, nonempty = false): values is string[] =>
  Array.isArray(values) &&
  values.length <= 64 &&
  (!nonempty || values.length > 0) &&
  values.every((value) => typeof value === 'string' && value.length > 0 && value.length <= 512) &&
  new Set(values).size === values.length

/** Trusted deterministic runner. No supplied fixture, passed flag, model verdict, network or model call. */
export function validateExperiencePolicy(candidate: ExperiencePolicyCandidate): MechanicalReceipt {
  const failures: string[] = []
  const fail = (check: string, passed: boolean) => {
    if (!passed) failures.push(check)
  }
  const generationRoots = strings(candidate.generationRoots) ? [...candidate.generationRoots] : []
  const generationFamilies = strings(candidate.generationFamilies)
    ? [...candidate.generationFamilies]
    : []
  const heldoutFixtures = [...fixtures, ...experienceBehaviorHoldouts]
  const heldoutRoots = heldoutFixtures.map((item) => 'holdout-root:' + digest(item.id))
  const heldoutFamilies = heldoutFixtures.map((item) => 'engineering-family:' + digest(item.id))
  const allowed = new Set([
    'id',
    'version',
    'hash',
    'sceneId',
    'action',
    'condition',
    'status',
    'supportIds',
    'counterexampleIds',
    'generationRoots',
    'generationFamilies',
    'createdAt',
    'expiresAt',
    'reason',
    'validationId',
    'activationBasis',
  ])
  fail(
    'candidate_shape',
    Object.keys(candidate).every((key) => allowed.has(key)) &&
      typeof candidate.id === 'string' &&
      candidate.id.length > 0 &&
      candidate.id.length <= 512 &&
      Number.isSafeInteger(candidate.version) &&
      candidate.version >= 1 &&
      typeof candidate.sceneId === 'string' &&
      candidate.sceneId.length > 0 &&
      candidate.sceneId.length <= 512 &&
      ['candidate', 'validated', 'active', 'suspended', 'retired'].includes(candidate.status) &&
      (candidate.activationBasis === undefined ||
        ['explicit_preference', 'administrator_review'].includes(candidate.activationBasis)),
  )
  let hashMatches = false
  try {
    hashMatches =
      typeof candidate.hash === 'string' && candidate.hash === experienceCandidateHash(candidate)
  } catch {
    /* malformed proposal is not authority */
  }
  fail('exact_semantic_hash', hashMatches)
  fail(
    'action_condition_whitelist',
    isExperienceActionCondition(candidate.action, candidate.condition),
  )
  fail(
    'support_lineage',
    strings(candidate.supportIds, true) &&
      strings(candidate.generationRoots, true) &&
      strings(candidate.generationFamilies, true),
  )
  fail(
    'holdout_root_disjoint',
    generationRoots.every((root) => !heldoutRoots.includes(root)),
  )
  fail(
    'holdout_family_disjoint',
    generationFamilies.every((family) => !heldoutFamilies.includes(family)),
  )
  const start = Date.parse(candidate.createdAt),
    end = Date.parse(candidate.expiresAt)
  fail(
    'bounded_expiry',
    Number.isFinite(start) && Number.isFinite(end) && end > start && end - start <= 366 * 86400000,
  )
  fail(
    'no_counterexamples',
    Array.isArray(candidate.counterexampleIds) && candidate.counterexampleIds.length === 0,
  )
  fail(
    'fixed_protocol_holdouts',
    fixtures.every(
      (item) => isExperienceActionCondition(item.action, item.condition) === item.expected,
    ),
  )
  fail(
    'fixed_operation_behavior_holdouts',
    runExperienceBehaviorHoldouts().every((result) => result.passed),
  )
  fail(
    'fixed_non_authoritative_templates',
    actions.every((action) => {
      const text = renderExperienceAction(action)
      return text.length <= 500 && !text.includes('holdout-person')
    }),
  )
  return {
    candidateId: candidate.id,
    candidateVersion: candidate.version,
    candidateHash: candidate.hash,
    tier: 'mechanical_only',
    ruleVersion,
    ruleHash,
    datasetVersion,
    datasetHash,
    generationRoots,
    generationFamilies,
    heldoutRoots,
    heldoutFamilies,
    checks: [...checks],
    failures,
    passed: failures.length === 0,
  }
}
