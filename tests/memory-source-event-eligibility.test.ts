import test from 'node:test'
import assert from 'node:assert/strict'
import {
  canonicalProofKey,
  validateNewProof,
  type SupportProof,
} from '../src/memory/domain/proofs.ts'
import { isCurrentlyApplicable } from '../src/memory/read/applicability.ts'
import {
  sourceEventEligibility,
  type SourceEventEligibilityPort,
} from '../src/memory/read/source-event-eligibility.ts'
import type { MemoryRecord, MemorySource } from '../src/memory/domain/contracts.ts'

const source: MemorySource = {
  sceneId: 'scene',
  messageId: 'message',
  senderId: 'person',
  eventRef: { eventId: 'event', revision: 1, deletionEpoch: 0 },
}
function fixture() {
  const state = { revision: 1, epoch: 0, recalled: false }
  const port: SourceEventEligibilityPort = {
    identity: (s) => ({
      sceneId: s.sceneId,
      messageId: s.messageId,
      senderId: s.senderId,
      adapterId: 'adapter',
      accountId: 'account',
    }),
    recalled: () => state.recalled,
    legacyEdited: () => state.revision > 1,
    current: (ref) => ({
      eligible:
        ref.eventId === 'event' &&
        ref.revision === state.revision &&
        ref.deletionEpoch === state.epoch,
      reason:
        ref.deletionEpoch !== state.epoch
          ? 'recalled'
          : ref.revision !== state.revision
            ? 'revision_changed'
            : 'current',
      bodyCoverage: 'unavailable',
    }),
    historical: (ref) => ({
      eligible:
        ref.eventId === 'event' &&
        ref.revision <= state.revision &&
        ref.deletionEpoch === state.epoch,
      reason: ref.deletionEpoch !== state.epoch ? 'recalled' : 'historical_metadata_only',
      bodyCoverage: 'unavailable',
    }),
  }
  return { state, port }
}
const proof = (sources: MemorySource[], parents: string[] = []): SupportProof => ({
  sources,
  parents,
  parentReferences: parents.map((id) => ({ id, version: 1, deletionEpoch: 0 })),
})
const record = (id: string): MemoryRecord => ({
  id,
  scope: { kind: 'scene', sceneId: 'scene' },
  kind: 'fact',
  content: 'Synthetic qualified assertion',
  sources: [source],
  importance: 'medium',
  confidence: 0.8,
  learnedAt: '2026-10-28T00:00:00Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  lineageStatus: 'verified',
})

test('source proof keys bind exact event revision and preserve legacy identity without fabrication', () => {
  const legacy = { sceneId: 'scene', messageId: 'message', senderId: 'person' }
  assert.equal(
    canonicalProofKey(proof([legacy])),
    JSON.stringify([[['scene', 'message', 'person']], [], [], null, null]),
  )
  const keys = [
    legacy,
    source,
    { ...source, eventRef: { ...source.eventRef!, revision: 2 } },
    { ...source, eventRef: { ...source.eventRef!, deletionEpoch: 1 } },
  ].map((s) => canonicalProofKey(proof([s])))
  assert.equal(new Set(keys).size, 4)
  assert.doesNotThrow(() => validateNewProof(proof([source]), 'source-only'))
  assert.throws(
    () =>
      validateNewProof(
        proof([{ ...source, eventRef: { ...source.eventRef!, revision: 0 } }]),
        'source-only',
      ),
    /source_event_binding_invalid/,
  )
  assert.equal(
    canonicalProofKey(proof([source, legacy])),
    canonicalProofKey(proof([legacy, source])),
  )
})

test('edit drift excludes current exact and legacy claims without retracting authorized historical assertions', () => {
  const { state, port } = fixture()
  assert.equal(sourceEventEligibility(source, 'current', port).eligible, true)
  state.revision = 2
  assert.deepEqual(sourceEventEligibility(source, 'current', port), {
    eligible: false,
    binding: 'bound',
    reason: 'revision_changed',
  })
  assert.equal(sourceEventEligibility(source, 'historical', port).eligible, true)
  const { eventRef: _, ...legacy } = source
  assert.equal(sourceEventEligibility(legacy, 'current', port).binding, 'legacy_edited_unknown')
  assert.equal(sourceEventEligibility(legacy, 'current', port).eligible, false)
  assert.deepEqual(sourceEventEligibility(legacy, 'historical', port), {
    eligible: true,
    binding: 'legacy_edited_unknown',
    reason: 'legacy_edited_unknown',
  })
  state.epoch = 1
  assert.equal(sourceEventEligibility(source, 'historical', port).eligible, false)
  state.recalled = true
  assert.equal(sourceEventEligibility(legacy, 'historical', port).eligible, false)
})

test('source scope and malformed refs fail closed before storage callbacks', () => {
  const { port } = fixture()
  let reads = 0
  port.current = () => {
    reads++
    throw new Error('must not read')
  }
  assert.equal(
    sourceEventEligibility(
      { ...source, eventRef: { ...source.eventRef!, revision: NaN } },
      'current',
      port,
    ).eligible,
    false,
  )
  port.identity = () => ({
    sceneId: 'foreign',
    messageId: source.messageId,
    senderId: source.senderId,
    adapterId: 'a',
    accountId: 'b',
  })
  assert.equal(sourceEventEligibility(source, 'current', port).reason, 'scope_mismatch')
  assert.equal(reads, 0)
})

test('current applicability preserves AND sources/parents and independent OR without irreversible invalidation', () => {
  const { state, port } = fixture()
  const a = record('a'),
    b = record('b'),
    c = record('c')
  const fresh = { ...source, messageId: 'other', eventRef: undefined }
  const proofs = new Map([
    ['a', [proof([source])]],
    ['b', [proof([fresh], ['a'])]],
    ['c', [proof([fresh, source]), proof([fresh])]],
  ])
  const rows = new Map([a, b, c].map((row) => [row.id, row]))
  const readPort = {
    record: (id: string) => rows.get(id),
    proofs: (id: string) => proofs.get(id) ?? [],
    recalled: () => false,
    supported: () => true,
    sourceCurrent: (s: MemorySource) =>
      s.messageId === 'other' || sourceEventEligibility(s, 'current', port).eligible,
  }
  assert.equal(isCurrentlyApplicable(b, readPort, Date.now()), true)
  state.revision = 2
  assert.equal(isCurrentlyApplicable(a, readPort, Date.now()), false)
  assert.equal(isCurrentlyApplicable(b, readPort, Date.now()), false)
  assert.equal(isCurrentlyApplicable(c, readPort, Date.now()), true)
  assert.equal(a.status, 'active')
  assert.equal(proofs.get('a')![0]!.invalidatedBy, undefined)
  assert.equal(
    isCurrentlyApplicable(a, { ...readPort, sourceCurrent: undefined }, Date.now()),
    false,
  )
  const unverified = { ...a, lineageStatus: 'unverified' as const }
  assert.equal(isCurrentlyApplicable(unverified, readPort, Date.now()), false)
  proofs.set('a', [proof([{ ...source, eventRef: { ...source.eventRef!, revision: 2 } }])])
  assert.equal(isCurrentlyApplicable(b, readPort, Date.now()), true)
})
