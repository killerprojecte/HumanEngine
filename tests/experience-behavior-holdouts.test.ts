import test from 'node:test'
import assert from 'node:assert/strict'
import {
  experienceCandidateHash,
  type ExperiencePolicyCandidate,
} from '../src/memory/domain/experience.ts'
import {
  experienceBehaviorHoldouts,
  runExperienceBehaviorHoldouts,
} from '../src/memory/experience/behavior-holdouts.ts'
import { validateExperiencePolicy } from '../src/memory/experience/validation.ts'

function candidate(): ExperiencePolicyCandidate {
  const value: ExperiencePolicyCandidate = {
    id: 'behavior-policy',
    version: 1,
    hash: '',
    sceneId: 'synthetic-scene',
    action: 'exact_person_before_semantic',
    condition: { kind: 'person_question', personId: 'requester' },
    status: 'candidate',
    supportIds: ['instruction'],
    counterexampleIds: [],
    generationRoots: ['new-instruction-root'],
    generationFamilies: ['explicit-instruction'],
    createdAt: '2026-10-29T00:00:00Z',
    expiresAt: '2026-11-05T00:00:00Z',
  }
  value.hash = experienceCandidateHash(value)
  return value
}
test('fixed behavioral holdouts exercise actual operation eligibility without models or benefit claims', () => {
  const results = runExperienceBehaviorHoldouts()
  assert.ok(results.length >= 15)
  assert.ok(results.every((value) => value.passed))
  assert.ok(results.some((value) => value.id === 'transport-ack-is-not-application'))
  assert.ok(results.some((value) => value.id === 'plan-limited-coverage'))
  assert.ok(Object.isFrozen(experienceBehaviorHoldouts))
  assert.ok(Object.isFrozen(experienceBehaviorHoldouts[0]!.input.condition))
  assert.throws(() => {
    ;(experienceBehaviorHoldouts[0]!.input as { turnValid: boolean }).turnValid = false
  })
  assert.deepEqual(runExperienceBehaviorHoldouts(), results)
})
test('v2 mechanical receipt binds protocol and actual behavior datasets with disjoint evidence roots', () => {
  const value = candidate()
  const receipt = validateExperiencePolicy(value)
  assert.equal(receipt.passed, true)
  assert.equal(receipt.tier, 'mechanical_only')
  assert.equal(receipt.ruleVersion, 'experience-mechanical-v2')
  assert.equal(receipt.datasetVersion, 'experience-fixed-behavior-holdout-v2')
  assert.ok(receipt.checks.includes('fixed_operation_behavior_holdouts'))
  assert.equal(receipt.heldoutRoots.length, 5 + experienceBehaviorHoldouts.length)
  assert.equal(new Set(receipt.heldoutRoots).size, receipt.heldoutRoots.length)
  const contaminated = { ...value, generationRoots: [receipt.heldoutRoots.at(-1)!] }
  contaminated.hash = experienceCandidateHash(contaminated)
  assert.equal(validateExperiencePolicy(contaminated).passed, false)
})
