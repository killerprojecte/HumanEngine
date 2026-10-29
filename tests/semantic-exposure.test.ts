import test from 'node:test'
import assert from 'node:assert/strict'
import {
  SemanticExposure,
  SemanticStateEvidenceSchema,
  semanticModelPage,
} from '../src/memory/read/semantic-exposure.ts'
import type { SemanticView } from '../src/memory/semantic/contracts.ts'
import type { ModelMemoryContext } from '../src/memory/read/model-exposure.ts'
import { validWorkspaceState } from '../src/runtime/workspace-state-evidence.ts'
import { buildReplyBrief } from '../src/runtime/reply-brief.ts'
import type { ReplyPlan } from '../src/runtime/reply-plan.ts'
const ctx = (): ModelMemoryContext => ({
  scene: { id: 'scene' },
  mode: 'real',
  signal: new AbortController().signal,
  dependencies: new Set(),
  validations: [],
})
function view(): SemanticView {
  const parents = ['selected-a', 'selected-b', 'unselected'].map((id) => ({
    ref: { id, version: 1, deletionEpoch: 0 },
    dependencyKey: 'a'.repeat(64),
    fingerprint: 'b'.repeat(64),
    roots: ['c'.repeat(64)],
    proofs: [
      {
        proofId: 'private-' + id,
        sources: [{ sceneId: 'scene', messageId: 'source-' + id, senderId: 'person' }],
        parents: [],
        parentReferences: [],
      },
    ],
  }))
  return {
    candidate: {
      schemaVersion: 1,
      anchorDependencies: [],
      anchorQualifications: [],
      id: 'summary',
      sceneId: 'scene',
      personId: 'person',
      partitionKey: 'd'.repeat(64),
      version: 1,
      inputKey: 'e'.repeat(64),
      kind: 'summary_candidate',
      text: 'Attributed summary only.',
      selectedParentIds: ['selected-a', 'selected-b'],
      parents,
      hypothesis: true,
      status: 'admitted',
      admission: 'administrator_review',
      createdAt: '2026-10-29T00:00:00Z',
      applicableUntil: '2026-11-05T00:00:00Z',
    },
    declarations: parents.map((p) => ({
      ref: p.ref,
      record: {
        id: p.ref.id,
        scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
        content: 'I might NOT attend unless the weather clears; never on Sunday.',
        kind: 'claim',
        epistemicKind: 'reported',
        sources: p.proofs[0]!.sources,
        importance: 'low',
        confidence: 0.4,
        learnedAt: '2026-10-29T00:00:00Z',
        occurredAt: '2026-10-28T00:00:00Z',
        status: 'disputed',
        locked: false,
        origin: 'learned',
      },
    })),
    roots: ['c'.repeat(64)],
    applicability: 'current',
    interpretation: 'attributed_summary_not_verified_truth',
  }
}
test('semantic whole delivery rejects forged/omitted receipts, inherits every source, policy ABA and exact hidden revisions', () => {
  let raw = { items: [view()], coverage: 'complete' as const },
    policy = 1,
    allowed = true,
    deliveries = 0
  const exposure = new SemanticExposure({
    query: () => structuredClone(raw),
    policyIdentity: () => policy,
    allowed: () => allowed,
    onDelivered: () => {
      deliveries++
    },
  })
  const c = ctx(),
    prepared = exposure.query(c)
  assert.equal(c.dependencies.size, 0)
  assert.equal(c.validations.length, 0)
  assert.equal(prepared[0]!.page.items[0]!.declarations.length, 3)
  assert.doesNotMatch(
    JSON.stringify(prepared),
    /proofId|fingerprint|dependencyKey|partitionKey|inputKey|[a-f0-9]{64}/,
  )
  assert.deepEqual(exposure.deliver(c, structuredClone(prepared)), [])
  assert.deepEqual(exposure.deliver(c, []), [])
  assert.equal(exposure.deliver(c, prepared).length, 1)
  assert.deepEqual([...c.dependencies].sort(), [
    'source-selected-a',
    'source-selected-b',
    'source-unselected',
  ])
  assert.equal(deliveries, 1)
  exposure.deliver(c, prepared)
  assert.equal(deliveries, 1)
  const guard = exposure.captureGuard(c)!
  assert.equal(guard(), true)
  raw.items[0]!.candidate.parents[2]!.dependencyKey = 'f'.repeat(64)
  assert.equal(guard(), false)
  assert.deepEqual(exposure.readDelivered(c), [])
  raw = { items: [view()], coverage: 'complete' }
  const d = ctx(),
    next = exposure.query(d)
  policy++
  assert.deepEqual(exposure.deliver(d, next), [])
  assert.equal(d.dependencies.size, 0)
  allowed = false
  assert.deepEqual(exposure.query(ctx()), [])
  allowed = true
  const simulation = ctx()
  simulation.mode = 'simulation'
  assert.deepEqual(exposure.query(simulation), [])
  const cancelled = ctx()
  const controller = new AbortController()
  cancelled.signal = controller.signal
  const pending = exposure.query(cancelled)
  controller.abort()
  assert.deepEqual(exposure.deliver(cancelled, pending), [])
})
test('strict semantic DTO and Replyer brief keep every original qualification and hypothesis clock as one atom', () => {
  const raw = { items: [view()], coverage: 'limited' as const },
    packet = semanticModelPage(raw)
  assert.equal(validWorkspaceState('memory.summaries', packet), true)
  const broken = structuredClone(packet)
  broken.page.items[0]!.declarations.pop()
  assert.equal(SemanticStateEvidenceSchema.safeParse(broken).success, false)
  const candidate = structuredClone(raw)
  candidate.items[0]!.candidate.status = 'candidate'
  assert.throws(() => semanticModelPage(candidate))
  const plan = {
    action: 'reply',
    deliveryTool: 'messages.send',
    targetMessageIds: ['m'],
    contextMessageIds: ['m'],
    strategy: 'Qualified answer',
    constraints: [],
    facts: [],
    assets: [],
    emoji: [],
    expressionGoals: [],
    memorySelections: [],
    actions: [],
    stateEvidenceIds: ['semantic-receipt'],
  } as ReplyPlan
  const brief = buildReplyBrief(
    plan,
    [{ id: 'semantic-receipt', toolName: 'memory.summaries', value: [packet] }],
    [],
  )
  const encoded = JSON.stringify(brief)
  assert.match(encoded, /never on Sunday/)
  assert.match(encoded, /unselected/)
  assert.match(encoded, /reported/)
  assert.match(encoded, /2026-11-05/)
  assert.match(encoded, /attributed_summary_not_verified_truth/)
  assert.match(encoded, /limited/)
  assert.doesNotMatch(encoded, /proofId|fingerprint|[a-f0-9]{64}/)
})
