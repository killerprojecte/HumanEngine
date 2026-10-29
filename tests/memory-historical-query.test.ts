import test from 'node:test'
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import {
  AssertionHistory,
  type AssertionRevision,
} from '../src/memory/storage/assertion-history.ts'
import {
  queryAssertionHistory,
  type AssertionHistoricalReadPort,
} from '../src/memory/read/assertion-history-query.ts'
import type { MemoryRecord } from '../src/memory/domain/contracts.ts'

function fixture(count = 1) {
  const db = new Database(':memory:')
  const history = new AssertionHistory(db)
  const record: MemoryRecord = {
    id: 'assertion',
    scope: { kind: 'scene', sceneId: 'scene' },
    kind: 'claim',
    content: 'Synthetic complete conditional claim',
    sources: [],
    importance: 'medium',
    confidence: 0.5,
    learnedAt: '2026-01-01T00:00:00.000Z',
    status: 'active',
    locked: false,
    origin: 'manual',
    epistemicKind: 'reported',
  }
  const at = (version: number) =>
    new Date(Date.parse('2026-01-01T00:00:00Z') + version * 1000).toISOString()
  db.transaction(() => {
    for (let version = 1; version <= count; version++)
      history.append({
        record: {
          ...record,
          content: `Revision ${version}: only if both conditions hold; not a verified fact`,
        },
        supports: [],
        expectedVersion: version - 1,
        expectedDeletionEpoch: 0,
        idempotencyKey: `version-${version}`,
        recordedAt: at(version),
        validFrom: '2025-01-01T00:00:00.000Z',
        validTo: '2027-01-01T00:00:00.000Z',
        eventKind: 'admin_assertion',
      })
  })()
  let allowed = true
  const port: AssertionHistoricalReadPort = {
    authorizeAnchor: (ref) =>
      allowed &&
      ref.id === 'assertion' &&
      ref.version === count &&
      ref.deletionEpoch === 0 &&
      !history.state(ref.id).deleted
        ? 'scene:scene'
        : undefined,
    query: (id, scope, q) => history.queryRetained(id, scope, q),
    authorizeRevision: () => allowed,
  }
  return {
    db,
    history,
    port,
    at,
    anchor: { id: 'assertion', version: count, deletionEpoch: 0 },
    deny() {
      allowed = false
    },
  }
}

test('SQL time predicates find a cutoff older than the newest 200 revisions before applying limit', () => {
  const f = fixture(250)
  try {
    assert.equal(f.history.revisions('assertion', 'scene:scene', 200)[0]!.version, 51)
    const result = queryAssertionHistory(
      { anchor: f.anchor, asOfRecorded: f.at(5), validAt: '2026-01-01T00:00:00.000Z', limit: 2 },
      f.port,
    )
    assert.deepEqual(
      result.revisions.map((r) => r.version),
      [4, 5],
    )
    assert.equal(result.coverage.hasMore, true)
    assert.deepEqual(result.coverage.reasons, ['result_limit'])
    assert.equal(result.interpretation, 'previously_recorded_not_current_truth')
    assert.equal(result.sourceBodyCoverage, 'unavailable')
    assert.equal(
      result.revisions[1]!.record.content,
      'Revision 5: only if both conditions hold; not a verified fact',
    )
    assert.equal(result.revisions[1]!.record.epistemicKind, 'reported')
    assert.deepEqual(result.revisions[1]!.ref, { id: 'assertion', version: 5, deletionEpoch: 0 })
  } finally {
    f.db.close()
  }
})

test('valid time filters happen before limit, unknown times remain explicitly limited', () => {
  const f = fixture(220)
  try {
    // Later records do not apply at the requested time; an early bounded record must still be found.
    f.db
      .prepare(
        "UPDATE memory_assertion_revisions SET value=json_set(value,'$.validFrom','2026-06-01T00:00:00.000Z') WHERE version>2",
      )
      .run()
    const result = queryAssertionHistory(
      { anchor: f.anchor, validAt: '2026-01-02T00:00:00.000Z', limit: 1 },
      f.port,
    )
    assert.deepEqual(
      result.revisions.map((r) => r.version),
      [2],
    )
    f.db
      .prepare(
        "UPDATE memory_assertion_revisions SET recorded_at=NULL,value=json_set(value,'$.recordedAt',NULL,'$.validFrom',NULL,'$.event.legacyImport',json('true')) WHERE version=1",
      )
      .run()
    const historical = queryAssertionHistory(
      { anchor: f.anchor, asOfRecorded: f.at(2), limit: 20 },
      f.port,
    )
    assert.deepEqual(
      historical.revisions.map((r) => r.version),
      [2],
    )
    assert.ok(historical.coverage.reasons.includes('recorded_time_unknown'))
    const unknown = queryAssertionHistory(
      { anchor: f.anchor, validAt: '2026-01-02T00:00:00.000Z' },
      f.port,
    )
    assert.ok(unknown.coverage.reasons.includes('legacy_import'))
    assert.ok(unknown.coverage.reasons.includes('valid_time_unknown'))
    assert.equal(unknown.revisions[0]!.recordedAt, null)
  } finally {
    f.db.close()
  }
})

test('current privacy/epoch authority overrides old cutoff; per-revision proofs cannot be bypassed', () => {
  const f = fixture(3)
  try {
    let queried = 0
    const query = f.port.query
    f.port.query = (...args) => {
      queried++
      return query(...args)
    }
    assert.throws(
      () => queryAssertionHistory({ anchor: { ...f.anchor, deletionEpoch: 1 } }, f.port),
      /anchor_unavailable/,
    )
    assert.equal(queried, 0)
    f.port.authorizeRevision = (revision) => revision.version !== 2
    const result = queryAssertionHistory({ anchor: f.anchor }, f.port)
    assert.deepEqual(
      result.revisions.map((r) => r.version),
      [1, 3],
    )
    assert.ok(result.coverage.reasons.includes('authorization_or_proof_incomplete'))
    f.port.authorizeRevision = () => {
      throw new Error('recovery_authority_head_changed')
    }
    assert.throws(
      () => queryAssertionHistory({ anchor: f.anchor }, f.port),
      /recovery_authority_head_changed/,
    )
    f.deny()
    assert.throws(
      () => queryAssertionHistory({ anchor: f.anchor, asOfRecorded: f.at(1) }, f.port),
      /anchor_unavailable/,
    )
  } finally {
    f.db.close()
  }
})

test('retention gaps and omitted legacy source bindings are not represented as complete history', () => {
  const f = fixture(3)
  try {
    f.db.prepare('UPDATE memory_assertion_revisions SET value=NULL WHERE version=1').run()
    const revision = f.history.revision('assertion', 2, 'scene:scene')!
    revision.supports = [
      { sources: [{ sceneId: 'scene', messageId: 'legacy', senderId: 'person' }], parents: [] },
    ]
    f.db
      .prepare('UPDATE memory_assertion_revisions SET value=? WHERE version=2')
      .run(JSON.stringify(revision))
    const result = queryAssertionHistory({ anchor: f.anchor }, f.port)
    assert.ok(result.coverage.reasons.includes('retention_incomplete'))
    assert.ok(result.coverage.reasons.includes('legacy_proof_binding_unknown'))
    result.revisions[0]!.record.content = 'mutation'
    assert.notEqual(f.history.revision('assertion', 2, 'scene:scene')!.record.content, 'mutation')
    assert.deepEqual(
      f.history.queryRetained('assertion', 'scene:foreign', { throughVersion: 3, limit: 20 })
        .revisions,
      [],
    )
  } finally {
    f.db.close()
  }
})

test('query input bounds and second anchor check are enforced without swallowing authority failures', () => {
  const f = fixture()
  try {
    for (const input of [
      { limit: 21 },
      { limit: 0 },
      { asOfRecorded: '2026-01-01' },
      { validAt: 'not-time' },
    ])
      assert.throws(
        () => queryAssertionHistory({ anchor: f.anchor, ...input }, f.port),
        /query_invalid/,
      )
    assert.throws(
      () => f.history.queryRetained('assertion', 'scene:scene', { throughVersion: 1, limit: 21 }),
      /query_invalid/,
    )
    f.port.authorizeRevision = (_revision: AssertionRevision) => {
      f.deny()
      return true
    }
    assert.throws(() => queryAssertionHistory({ anchor: f.anchor }, f.port), /anchor_unavailable/)
  } finally {
    f.db.close()
  }
})

test('missing retained event metadata qualifies authorized assertions without deleting their history', () => {
  const f = fixture(2)
  try {
    const before = f.history.revisions('assertion', 'scene:scene')
    f.port.qualification = (revision) =>
      revision.version === 1
        ? ['source_event_metadata_unavailable', 'parent_revision_metadata_unavailable']
        : []
    const result = queryAssertionHistory({ anchor: f.anchor }, f.port)
    assert.equal(result.revisions.length, 2)
    assert.equal(result.coverage.status, 'limited')
    assert.deepEqual(result.coverage.reasons, [
      'parent_revision_metadata_unavailable',
      'source_event_metadata_unavailable',
    ])
    assert.equal(result.sourceBodyCoverage, 'unavailable')
    assert.deepEqual(f.history.revisions('assertion', 'scene:scene'), before)
    let qualified = 0
    f.port.authorizeRevision = () => false
    f.port.qualification = () => {
      qualified++
      return []
    }
    assert.equal(queryAssertionHistory({ anchor: f.anchor }, f.port).revisions.length, 0)
    assert.equal(qualified, 0)
  } finally {
    f.db.close()
  }
})

test('history qualification rejects arbitrary metadata and propagates recovery authority errors', () => {
  const f = fixture()
  try {
    f.port.qualification = () => ['untrusted arbitrary content' as never]
    assert.throws(
      () => queryAssertionHistory({ anchor: f.anchor }, f.port),
      /memory_history_qualification_invalid/,
    )
    f.port.qualification = () => {
      throw new Error('recovery_authority_head_changed')
    }
    assert.throws(
      () => queryAssertionHistory({ anchor: f.anchor }, f.port),
      /recovery_authority_head_changed/,
    )
  } finally {
    f.db.close()
  }
})
