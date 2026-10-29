import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { RelationCandidates } from '../src/memory/relations/candidates.ts'
import type {
  RelationAssertionView,
  RelationContext,
  RelationProposal,
} from '../src/memory/domain/relations.ts'
import type { RelationProof } from '../src/memory/domain/relations.ts'
const ctx: RelationContext = { sceneId: 'scene', simulation: false, provenance: 'source-only' }
function proof(messageId: string): RelationProof {
  return {
    proofId: messageId,
    sources: [
      {
        sceneId: 'scene',
        messageId,
        senderId: 'person',
        eventRef: { eventId: messageId, revision: 1, deletionEpoch: 0 },
      },
    ],
    parents: [],
    parentReferences: [],
    subjectWitness: { kind: 'self', personId: 'person', sourceMessageId: messageId },
    sourceObservations: [
      { sceneId: 'scene', messageId, senderId: 'person', receivedAt: '2026-10-28T00:00:00Z' },
    ],
  }
}
test('relation current projection intersects original complete proofs without changing persisted history', () => {
  const db = new Database(':memory:')
  let active = [proof('a'), proof('b')]
  let id = 0
  const view = (): RelationAssertionView => ({
    record: {
      id: 'assertion',
      scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
      content: 'Synthetic participation',
      kind: 'fact',
      epistemicKind: 'explicit',
      sources: active.flatMap((p) => p.sources),
      confidence: 0.5,
      importance: 'low',
      learnedAt: '2026-10-28T00:00:00Z',
      status: 'active',
      locked: false,
      origin: 'learned',
      lineageStatus: 'verified',
    },
    proofs: active,
  })
  const service = new RelationCandidates(
    db,
    {
      enabled: () => true,
      learningEnabled: () => true,
      autoAdmission: () => false,
      current: (_, ref) =>
        ref.id === 'assertion' && ref.version === 1 && ref.deletionEpoch === 0 ? view() : undefined,
    },
    { id: () => String(++id) },
  )
  const proposal: RelationProposal = {
    predicate: 'participates_in',
    modality: 'asserted',
    temporalScope: 'current',
    personId: 'person',
    target: { kind: 'new', entityKind: 'plan', title: 'Synthetic plan' },
    polarity: 'positive',
    conditions: [],
  }
  const result = db.transaction(() =>
    service.admit(ctx, { id: 'assertion', version: 1, deletionEpoch: 0 }, [proposal]),
  )()
  assert.equal(result.created.length, 1)
  const edge = result.created[0]!
  const stored = JSON.stringify(service.storage.get(edge.id))
  active = [proof('b')]
  const projected = service.list(ctx).items[0]!
  assert.deepEqual(
    projected.proofs.map((p) => p.proofId),
    ['b'],
  )
  assert.deepEqual(projected.proofs[0]!.subjectWitness, proof('b').subjectWitness)
  assert.deepEqual(projected.proofs[0]!.sourceObservations, proof('b').sourceObservations)
  assert.equal(JSON.stringify(service.storage.get(edge.id)), stored)
  active = [proof('new')]
  assert.equal(service.list(ctx).items.length, 0)
  active = [
    {
      ...proof('a'),
      sources: [
        { ...proof('a').sources[0]!, eventRef: { eventId: 'a', revision: 2, deletionEpoch: 0 } },
      ],
    },
  ]
  assert.equal(service.list(ctx).items.length, 0)
  active = [
    {
      ...proof('b'),
      sourceObservations: [
        { ...proof('b').sourceObservations![0]!, receivedAt: '2026-10-29T00:00:00Z' },
      ],
    },
  ]
  assert.equal(service.list(ctx).items.length, 0)
  active = [proof('b')]
  const reviewed = db.transaction(() => service.review(ctx, edge.id, edge.version, 'accepted'))()
  assert.deepEqual(
    reviewed.proofs.map((p) => p.proofId),
    ['b'],
  )
  assert.equal(service.storage.get(edge.id)!.proofs.length, 2)
  db.close()
})
