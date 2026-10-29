import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { SourceEventStore } from '../src/memory/storage/event-revisions.ts'
import { CommitmentService } from '../src/memory/commitments/service.ts'
import type { CommitmentAssertion } from '../src/memory/domain/commitments.ts'

const identity = { sceneId: 's', adapterId: 'a', accountId: 'bot', messageId: 'm', senderId: 'p' }
const ingress = { kind: 'ingress' as const, identity, bodyHash: 'a'.repeat(64), occurredAt: null }
test('source post-write subscriptions see exact stored head in the original transaction and rollback failures', () => {
  const db = new Database(':memory:')
  const events = new SourceEventStore(db)
  let calls = 0
  const off = events.subscribeCommitted((change) => {
    calls++
    assert.equal(db.inTransaction, true)
    assert.deepEqual(events.headMetadata(identity)?.ref, change.ref)
  })
  const first = events.observe(ingress).ref!
  events.observe(ingress)
  assert.equal(calls, 1)
  const bad = events.subscribeCommitted(() => {
    throw Error('subscriber failure')
  })
  assert.throws(
    () =>
      events.observe({
        ...ingress,
        kind: 'edit',
        bodyHash: 'b'.repeat(64),
        editId: 'e',
        order: { kind: 'observed_cas', expectedBase: first },
      }),
    /subscriber failure/,
  )
  assert.deepEqual(events.headMetadata(identity)?.ref, first)
  bad()
  off()
  const asyncOff = events.subscribeCommitted((() => Promise.resolve()) as unknown as () => void)
  assert.throws(
    () =>
      events.observe({
        ...ingress,
        kind: 'edit',
        bodyHash: 'b'.repeat(64),
        editId: 'e',
        order: { kind: 'observed_cas', expectedBase: first },
      }),
    /source_event_async_subscriber/,
  )
  assert.deepEqual(events.headMetadata(identity)?.ref, first)
  asyncOff()
  events.observe({
    ...ingress,
    kind: 'edit',
    bodyHash: 'b'.repeat(64),
    editId: 'e',
    order: { kind: 'observed_cas', expectedBase: first },
  })
  assert.equal(events.headMetadata(identity)?.ref.revision, 2)
  db.close()
})
test('plan post-write hook observes committed terms plus prior revisions and cannot escape transaction rollback', () => {
  const db = new Database(':memory:')
  const source = { sceneId: 's', messageId: 'm', senderId: 'p' }
  const view: CommitmentAssertion = {
    record: {
      id: 'assertion',
      scope: { kind: 'scene', sceneId: 's', personId: 'p' },
      content: 'Synthetic explicit plan',
      kind: 'fact',
      epistemicKind: 'explicit',
      sources: [source],
      importance: 'low',
      confidence: 0.5,
      learnedAt: '2026-10-29T00:00:00Z',
      status: 'active',
      locked: false,
      origin: 'learned',
      lineageStatus: 'verified',
    },
    proofs: [
      {
        proofId: 'proof',
        sources: [source],
        parents: [],
        parentReferences: [],
        subjectWitness: { kind: 'self', personId: 'p', sourceMessageId: 'm' },
      },
    ],
  }
  let fail = false,
    calls = 0
  const service = new CommitmentService(
    db,
    {
      enabled: () => true,
      learningEnabled: () => true,
      current: () => view,
      people: () => ['p'],
      afterWrite: (head, revisions, prior) => {
        calls++
        assert.equal(db.inTransaction, true)
        assert.deepEqual(service.storage.head(head.id), head)
        assert.deepEqual(service.storage.revisions(head.id), revisions)
        assert.equal(prior.length, 0)
        if (fail) throw Error('projection failure')
      },
    },
    { id: () => 'plan', now: () => '2026-10-29T00:00:00Z' },
  )
  const command = {
    kind: 'propose',
    terms: {
      kind: 'plan',
      title: 'Synthetic',
      participantIds: ['p'],
      polarity: 'positive',
      modality: 'asserted',
      temporalScope: 'future',
      conditions: [],
      timeQualification: 'unknown',
    },
  }
  const run = () =>
    db.transaction(() =>
      service.admit(
        { sceneId: 's', simulation: false, provenance: 'source-only' },
        { id: 'assertion', version: 1, deletionEpoch: 0 },
        command,
      ),
    )()
  fail = true
  assert.throws(run, /projection failure/)
  assert.equal(service.storage.head('plan'), undefined)
  fail = false
  assert.equal(run().status, 'saved')
  assert.equal(calls, 2)
  db.close()
})
