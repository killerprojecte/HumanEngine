/** Synthetic event acceptance fixtures; the current TypeScript parsers are the oracle. */
import { writeFileSync } from 'node:fs'
import { parseAssertionEligibilityEvent } from '../../src/memory/recovery/eligibility-contract.ts'
import { DomainRecoveryEventSchema } from '../../src/memory/recovery/domain-contracts.ts'
import { SourceEditHeadEventSchema } from '../../src/memory/recovery/source-event-contract.ts'
import {
  parseConfigurationRecoveryEvent,
  configurationKeys,
  sceneFlagKeys,
} from '../../src/memory/recovery/configuration-contract.ts'
const hash = 'a'.repeat(64)
const seeds = [
  { kind: 'habit_delete', schemaVersion: 1, id: 'habit', fingerprint: hash },
  {
    kind: 'meme_delete',
    schemaVersion: 1,
    id: hash,
    contentHash: hash,
    qualificationHash: hash,
    format: 'png',
  },
  {
    kind: 'meme_state',
    schemaVersion: 1,
    id: hash,
    contentHash: hash,
    qualificationHash: hash,
    state: {
      available: true,
      disabled: false,
      knownOnly: false,
      shared: false,
      privacyReviewed: true,
    },
  },
  { kind: 'experience_delete', schemaVersion: 1, id: 'experience', sceneId: 'scene' },
  {
    kind: 'experience_state',
    schemaVersion: 1,
    id: 'experience',
    sceneId: 'scene',
    qualificationHash: hash,
    version: 1,
    status: 'active',
    reason: 'review_suspended',
  },
  {
    kind: 'experience_state',
    schemaVersion: 1,
    id: 'experience',
    sceneId: 'scene',
    qualificationHash: hash,
    version: 1,
    status: 'active',
    reason: 'experimental_mechanical_hint',
    activationBasis: 'administrator_review',
    validationId: 'validation',
  },
  {
    kind: 'source_edit_head',
    schemaVersion: 1,
    sceneId: 'scene',
    messageId: 'message',
    eventId: 'event',
    revision: 1,
    deletionEpoch: 0,
    identityHash: hash,
    headHash: hash,
  },
]
const since = {
  memoryExperienceFeedbackEnabled: 8,
  memorySemanticEnabled: 7,
  memorySemanticAutoAdmissionEnabled: 7,
  memorySemanticAnswerEnabled: 7,
  memoryKnowledgeEnabled: 6,
  memoryKnowledgeAutoAdmissionEnabled: 6,
  memoryKnowledgeAnswerEnabled: 6,
  topicAdmissionEnabled: 5,
  memoryWorkingSetEnabled: 4,
  memoryEpisodesEnabled: 4,
  memoryCommitmentsEnabled: 3,
  memoryCommitmentsAnswerEnabled: 3,
  memoryHistoricalReadEnabled: 3,
  sourceEventRevisionsEnabled: 2,
}
for (let version = 1; version <= 8; version++)
  seeds.push({
    kind: 'configuration_flags',
    schemaVersion: version,
    flags: Object.fromEntries(
      configurationKeys.filter((key) => (since[key] ?? 1) <= version).map((key) => [key, true]),
    ),
  })
for (let version = 1; version <= 2; version++)
  seeds.push({
    kind: 'scene_flags',
    schemaVersion: version,
    sceneId: 'scene',
    identityHash: hash,
    flags: Object.fromEntries(
      sceneFlagKeys
        .filter(
          (key) => version === 2 || !['memoryReadEnabled', 'personalizationEnabled'].includes(key),
        )
        .map((key) => [key, true]),
    ),
  })
for (const operationId of [
  '00000000-0000-0000-0000-000000000000',
  'ffffffff-ffff-ffff-ffff-ffffffffffff',
  'FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF',
  '12345678-1234-4123-8123-123456789012',
  '12345678-1234-9123-8123-123456789012',
])
  seeds.push({
    kind: 'assertion_eligibility',
    schemaVersion: 1,
    operationId,
    mode: 'baseline',
    changes: [],
  })
const cases = []
for (const seed of seeds) {
  cases.push(seed, { ...seed, unknown: true })
  if (seed.kind === 'scene_flags')
    for (const identityHash of [[hash], [[hash]], [hash, hash], [], {}])
      cases.push({ ...seed, identityHash })
  for (const key of Object.keys(seed)) {
    const missing = { ...seed }
    delete missing[key]
    cases.push(missing, { ...seed, [key]: null }, { ...seed, [key]: [] })
  }
  for (const key of ['id', 'sceneId', 'messageId', 'eventId'])
    if (typeof seed[key] === 'string' && seed[key] !== hash) {
      for (const n of [512, 513, 2000, 2001]) cases.push({ ...seed, [key]: '😀'.repeat(n) })
    }
  if (seed.flags)
    for (const key of Object.keys(seed.flags))
      cases.push({ ...seed, flags: { ...seed.flags, [key]: 'true' } })
}
function accept(input) {
  try {
    if (input.kind === 'assertion_eligibility') parseAssertionEligibilityEvent(input)
    else if (['configuration_flags', 'scene_flags'].includes(input.kind))
      parseConfigurationRecoveryEvent(input)
    else if (input.kind === 'source_edit_head') SourceEditHeadEventSchema.parse(input)
    else DomainRecoveryEventSchema.parse(input)
    return true
  } catch {
    return false
  }
}
writeFileSync(
  new URL('../internal/recovery/testdata/domain-events-node.json', import.meta.url),
  JSON.stringify(
    cases.map((input) => ({ input, valid: accept(input) })),
    null,
    2,
  ) + '\n',
)
