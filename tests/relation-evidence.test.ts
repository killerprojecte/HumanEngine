import test from 'node:test'
import assert from 'node:assert/strict'
import { validateRelationSelection } from '../src/runtime/relation-evidence.ts'
import { compareRelationRetrieval } from '../src/memory/relations/shadow.ts'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import { packReplyEvidence } from '../src/runtime/reply-evidence-budget.ts'
import { ContextProjection } from '../src/runtime/context-projection.ts'
import type { ReplyPlan } from '../src/runtime/reply-plan.ts'
const edge = (id: string, polarity: string, requiredMemoryIds: string[]) => ({
  id,
  version: 1,
  predicate: 'participates_in',
  personId: 'alice',
  target: { entityId: 'entity', kind: 'plan', title: 'Synthetic trip' },
  polarity,
  modality: 'possible',
  temporalScope: 'future',
  conditions: ['Only if weather permits; not a promise'],
  status: polarity === 'negative' ? 'candidate' : 'accepted',
  admission: 'reviewed',
  epistemicKind: 'reported',
  applicability: 'future',
  validFrom: '2099-01-01T00:00:00Z',
  requiredMemoryIds,
})
const records = [
  {
    id: 'p',
    content: 'Synthetic positive report',
    relationEvidence: [edge('r1', 'positive', ['p', 'target'])],
  },
  {
    id: 'n',
    content: 'Synthetic negative report',
    relationEvidence: [edge('r2', 'negative', ['n', 'target'])],
  },
  {
    id: 'target',
    content: 'Synthetic original plan',
    relationEvidence: [
      edge('r1', 'positive', ['p', 'target']),
      edge('r2', 'negative', ['n', 'target']),
    ],
  },
]
const plan: ReplyPlan = {
  action: 'reply',
  deliveryTool: 'messages.send',
  targetMessageIds: [],
  contextMessageIds: [],
  strategy: 'Express uncertainty',
  constraints: [],
  facts: [],
  assets: [],
  emoji: [],
  expressionGoals: [],
  actions: [],
  memorySelections: [{ evidenceId: 'receipt', memoryIds: ['p', 'n', 'target'] }],
}
test('relation selection keeps counterclaims and both endpoints, not a positive-only shortcut', () => {
  assert.throws(() => validateRelationSelection(records, ['p', 'target']), /incomplete_relation/)
  assert.throws(() => validateRelationSelection(records, ['p']), /incomplete_relation/)
  assert.doesNotThrow(() => validateRelationSelection(records, ['p', 'n', 'target']))
})
test('complete relation qualification survives brief, short-reference serialization and whole-group budget', () => {
  const brief = buildReplyBrief(
    plan,
    [{ id: 'receipt', toolName: 'memory.relations', value: records }],
    [],
  )
  const projection = new ContextProjection('scene')
  const packed = packReplyEvidence({
    brief,
    envelope: {},
    serialize: (value) => projection.stringify(value),
    budget: { mode: 'bytes', limit: 10000 },
  })
  assert.equal(packed.status, 'selected')
  for (const expected of [
    'negative',
    'possible',
    'future',
    'reported',
    '2099-01-01',
    'Only if weather permits; not a promise',
    'candidate',
  ])
    assert.ok(packed.serialized?.includes(expected))
  const omitted = packReplyEvidence({
    brief,
    envelope: {},
    serialize: JSON.stringify,
    budget: { mode: 'bytes', limit: 900 },
  })
  assert.notEqual(omitted.status, 'invalid')
  if (omitted.serialized) {
    assert.equal(omitted.serialized.includes('Synthetic positive report'), false)
    assert.equal(omitted.serialized.includes('Synthetic negative report'), false)
  }
})
test('shadow comparison reports only set cardinalities and explicit limited coverage', () => {
  const value = compareRelationRetrieval([{ id: 'private-id' }] as never, {
    items: [],
    coverage: 'limited',
  })
  assert.equal(value.baselineCount, 1)
  assert.equal(value.graphCoverage, 'limited')
  assert.doesNotMatch(JSON.stringify(value), /private-id/)
  assert.match(value.interpretation, /not_quality/)
})
