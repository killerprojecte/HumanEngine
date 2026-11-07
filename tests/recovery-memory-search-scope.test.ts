import { recoveryHashProbe } from './helpers/recovery-hashes.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { MemoryService, type MemoryRecord } from '../src/memory.ts'
import type { ProofParentReference } from '../src/memory/domain/proofs.ts'
import {
  metadata,
  RecoveryAuthority,
  revoke,
  searchMemorySnapshot,
} from '../src/memory/recovery/ledger.ts'

function fixture(count = 16) {
  const root = mkdtempSync(join(tmpdir(), 'memory-search-scope-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 's',
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  })
  const memory = new MemoryService(store)
  const records = Array.from({ length: count }, (_, i) =>
    memory.create({
      scope: { kind: 'scene', sceneId: 's' },
      content: `Search boundary record ${i}`,
      kind: 'fact',
      sources: [{ sceneId: 's', messageId: String(i), senderId: 'person' }],
    }),
  )
  store.db.transaction(() =>
    revoke(store.db, { kind: 'source', sceneId: 'other', messageId: 'synthetic' }),
  )()
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  const input = { sceneId: 's', simulation: false, query: '', terms: [] as string[] }
  return {
    store,
    memory,
    records,
    authority,
    input,
    search: () => searchMemorySnapshot(store, input),
    close() {
      authority.close()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}
function clean(f: ReturnType<typeof fixture>) {
  assert.equal(f.store.db.inTransaction, false)
  assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  assert.equal(f.authority.db.inTransaction, false)
}

test('fixed search bounds full authority scans independently of source count without schema writes', () => {
  const f = fixture(),
    original = recoveryHashProbe()
  try {
    const schema = f.store.db.pragma('schema_version', { simple: true })
    const changes = f.store.db.prepare('SELECT total_changes() AS n').get()
    original.start()
    const first = f.search()
    assert.equal(first.length, f.records.length)
    assert.equal(original.count, 0)
    assert.deepEqual(f.search(), first)
    assert.equal(original.count, 0)
    assert.equal(f.store.db.pragma('schema_version', { simple: true }), schema)
    assert.deepEqual(f.store.db.prepare('SELECT total_changes() AS n').get(), changes)
    clean(f)
  } finally {
    original.stop()
    f.close()
  }
})

test('search snapshots reject local writes and lock external authority tampering, then detect tampering after release', () => {
  const f = fixture(2),
    recalled = f.store.recalled.bind(f.store)
  let attempted = false
  f.authority.db.pragma('busy_timeout = 0')
  f.store.recalled = (scene, id) => {
    if (!attempted) {
      attempted = true
      assert.throws(
        () =>
          f.store.db
            .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
            .run('s', '0', new Date().toISOString()),
        /readonly/,
      )
      assert.throws(
        () =>
          f.authority.db
            .prepare('UPDATE revocations SET hash=? WHERE sequence=1')
            .run('0'.repeat(64)),
        /locked/,
      )
    }
    return recalled(scene, id)
  }
  try {
    assert.equal(f.search().length, 2)
    assert.ok(attempted)
    clean(f)
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(f.search, /recovery_chain_invalid/)
    clean(f)
  } finally {
    f.close()
  }
})

test('search post-unlock verification detects tampering before releasing results', () => {
  const f = fixture(1),
    original = RecoveryAuthority.prototype.head
  let scans = 0
  RecoveryAuthority.prototype.head = function () {
    if (++scans === 2)
      f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    return original.call(this)
  }
  try {
    assert.throws(f.search, /recovery_chain_invalid/)
    assert.equal(scans, 2)
    clean(f)
  } finally {
    RecoveryAuthority.prototype.head = original
    f.close()
  }
})

test('search exceptions release read scope and restore writes', () => {
  const f = fixture(1),
    recalled = f.store.recalled.bind(f.store)
  try {
    f.store.recalled = () => {
      throw new Error('synthetic_search_failure')
    }
    assert.throws(f.search, /synthetic_search_failure/)
    clean(f)
    f.store.recalled = recalled
    assert.equal(f.search().length, 1)
    f.store.recall('s', '0')
    assert.deepEqual(f.search(), [])
  } finally {
    f.close()
  }
})

test('search reads updates and recall freshly across calls and awaits', async () => {
  const f = fixture(2)
  try {
    const first = f.search()
    assert.equal(first.length, 2)
    first[0]!.content = 'caller mutation'
    assert.equal(
      f.search().some((record) => record.content === 'caller mutation'),
      false,
    )
    await Promise.resolve()
    f.memory.update(f.records[0]!.id, { content: 'Updated search boundary' })
    assert.ok(f.search().some((record) => record.content === 'Updated search boundary'))
    await Promise.resolve()
    f.store.recall('s', '0')
    assert.deepEqual(
      f.search().map((record) => record.id),
      [f.records[1]!.id],
    )
    clean(f)
  } finally {
    f.close()
  }
})

test('nested transaction and non-WAL search preserve caller ownership and live reads', () => {
  const f = fixture(2)
  try {
    f.store.db.transaction(() => {
      assert.equal(f.search().length, 2)
      assert.equal(f.store.db.inTransaction, true)
      assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
      f.store.db
        .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
        .run('s', '0', new Date().toISOString())
      assert.deepEqual(
        f.search().map((record) => record.id),
        [f.records[1]!.id],
      )
      assert.equal(f.store.db.inTransaction, true)
    })()
    f.store.db.pragma('journal_mode = DELETE')
    assert.equal(f.search().length, 1)
    assert.equal(f.store.db.pragma('journal_mode', { simple: true }), 'delete')
    clean(f)
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(f.search, /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('search input accessors execute before snapshots and cannot bless recalled evidence', () => {
  const f = fixture(1)
  try {
    const input = {
      ...f.input,
      get query() {
        assert.equal(f.store.db.inTransaction, false)
        assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
        f.store.recall('s', '0')
        return ''
      },
    }
    assert.deepEqual(searchMemorySnapshot(f.store, input), [])
    clean(f)
  } finally {
    f.close()
  }
})

test('search observes source edits without refreshing or reviving legacy evidence', () => {
  const f = fixture(1)
  const identity = {
    sceneId: 's',
    messageId: '0',
    senderId: 'person',
    adapterId: 'fixture',
    accountId: 'bot',
  }
  try {
    f.store.sourceEvents.observe({
      kind: 'ingress',
      identity,
      bodyHash: 'a'.repeat(64),
      occurredAt: null,
    })
    assert.equal(f.search().length, 1)
    f.store.sourceEvents.observe({
      kind: 'edit',
      identity,
      bodyHash: 'b'.repeat(64),
      occurredAt: null,
      editId: 'edit-1',
      order: { kind: 'platform_sequence', sequence: 1 },
    })
    assert.deepEqual(f.search(), [])
    clean(f)
  } finally {
    f.close()
  }
})

test('search rejects a query-only invariant change and recovers after SQL preparation errors', () => {
  const f = fixture(1),
    recalled = f.store.recalled.bind(f.store),
    prepare = f.store.db.prepare.bind(f.store.db)
  try {
    f.store.recalled = (scene, id) => {
      f.store.db.pragma('query_only = OFF')
      return recalled(scene, id)
    }
    assert.throws(f.search, /recovery_read_scope_changed/)
    clean(f)
    f.store.recalled = recalled
    f.store.db.prepare = ((sql: string) => {
      if (sql.includes('WITH scoped AS MATERIALIZED'))
        throw new Error('synthetic_rank_prepare_failure')
      return prepare(sql)
    }) as typeof f.store.db.prepare
    assert.throws(f.search, /synthetic_rank_prepare_failure/)
    clean(f)
    f.store.db.prepare = prepare
    assert.equal(f.search().length, 1)
  } finally {
    f.store.db.prepare = prepare
    f.close()
  }
})

function learned(
  f: ReturnType<typeof fixture>,
  content: string,
  messageId: string,
  parents: ProofParentReference[] = [],
) {
  const source = { sceneId: 's', messageId, senderId: 'person' }
  const result = f.memory.propose(
    {
      sceneId: 's',
      simulation: false,
      provenance: parents.length ? 'contextual' : 'source-only',
      dependencyMemoryIds: parents.map((parent) => parent.id),
      dependencyParentReferences: parents,
      sourceEvidence: [{ ...source, available: true, isBot: false }],
    },
    { content, kind: 'claim', sources: [source] },
  )
  assert.equal(result.status, 'saved', JSON.stringify(result))
  if (result.status !== 'saved') throw new Error('fixture_proposal_rejected')
  return result.memory
}
function bound(f: ReturnType<typeof fixture>, record: MemoryRecord) {
  const reference = f.memory.captureParentReference({ sceneId: 's', simulation: false }, record)
  assert.ok(reference)
  return reference
}

test('durable search validates bound AND/OR proofs, expiry, version and epoch drift with no repeated full scans', async () => {
  const f = fixture(0),
    original = recoveryHashProbe(),
    now = Date.now
  try {
    const p = learned(f, 'Bound premise P', 'p')
    const q = learned(f, 'Bound premise Q', 'q')
    const r = learned(f, 'Bound premise R', 'r')
    const expiring = f.memory.update(p.id, { validUntil: '2100-01-01T00:00:00.000Z' })
    const refs = [bound(f, expiring), bound(f, q), bound(f, r)]
    const required = learned(f, 'Required AND child', 'and', refs.slice(0, 2))
    const alternative = learned(f, 'Alternative OR child', 'dependent', refs.slice(0, 2))
    learned(f, alternative.content, 'independent', refs.slice(2))
    const grandchild = learned(f, 'Transitive bound child', 'grandchild', [bound(f, required)])
    const search = () => {
      original.start()
      try {
        const result = f.search()
        assert.equal(original.count, 0)
        return new Set(result.map((record) => record.id))
      } finally {
        original.stop()
      }
    }
    const initial = search()
    for (const record of [required, alternative, grandchild]) assert.ok(initial.has(record.id))
    Date.now = () => Date.parse('2101-01-01T00:00:00.000Z')
    const expired = search()
    assert.equal(expired.has(required.id), false)
    assert.equal(expired.has(grandchild.id), false)
    assert.ok(
      expired.has(alternative.id),
      'independent complete OR proof survives expired AND parent',
    )
    Date.now = now
    assert.ok(search().has(required.id), 'temporal exclusion must not destroy proof history')
    await Promise.resolve()
    f.memory.update(p.id, { locked: true })
    const updated = search()
    assert.equal(updated.has(required.id), false)
    assert.equal(updated.has(grandchild.id), false)
    assert.ok(updated.has(alternative.id))
    const stateBefore = f.memory.revisionState(r.id)
    await Promise.resolve()
    f.store.recall('s', 'r')
    f.memory.recall('s', 'r')
    assert.ok(f.memory.revisionState(r.id).deletionEpoch > stateBefore.deletionEpoch)
    assert.equal(
      search().has(alternative.id),
      false,
      'revoked OR parent must not reuse earlier authority',
    )
    clean(f)
  } finally {
    Date.now = now
    original.stop()
    f.close()
  }
})

test('public search commits support invalidation before acquiring its query-only snapshots', () => {
  const f = fixture(0),
    original = RecoveryAuthority.prototype.head
  try {
    const parent = learned(f, 'Refresh parent', 'parent')
    const child = learned(f, 'Refresh dependent', 'child', [bound(f, parent)])
    f.store.recall('s', 'parent')
    assert.equal(f.memory.adminList().find((record) => record.id === child.id)?.status, 'active')
    let observedSnapshot = false
    RecoveryAuthority.prototype.head = function () {
      if (f.store.db.pragma('query_only', { simple: true }) === 1) {
        observedSnapshot = true
        const row = f.store.db.prepare('SELECT value FROM memories WHERE id=?').get(child.id) as {
          value: string
        }
        assert.equal(
          JSON.parse(row.value).status,
          'invalidated',
          'refresh must commit before read snapshot',
        )
      }
      return original.call(this)
    }
    assert.deepEqual(f.memory.search({ sceneId: 's', simulation: false }, ''), [])
    assert.ok(observedSnapshot)
    assert.ok(f.memory.revisionState(child.id).deletionEpoch > 0)
    clean(f)
  } finally {
    RecoveryAuthority.prototype.head = original
    f.close()
  }
})

test('search owns shared vector bytes before acquiring snapshots', () => {
  const f = fixture(2),
    original = RecoveryAuthority.prototype.head
  try {
    const spaceId = f.memory.registerSpace({
      providerId: 'fixture',
      modelId: 'fixture',
      apiModelName: 'fixture',
      deployment: 'offline',
      dimensions: 2,
      preprocessing: 'plain',
      normalized: true,
    })
    f.memory.index(f.records[0]!.id, spaceId, [1, 0])
    f.memory.index(f.records[1]!.id, spaceId, [0, 1])
    const shared = new Float32Array(new SharedArrayBuffer(8))
    shared.set([1, 0])
    RecoveryAuthority.prototype.head = function () {
      shared.set([0, 1])
      return original.call(this)
    }
    const result = searchMemorySnapshot(f.store, {
      ...f.input,
      query: 'semantic-only',
      options: { spaceId, vector: new Uint8Array(shared.buffer) },
    })
    assert.deepEqual(
      result.map((record) => record.id),
      [f.records[0]!.id],
    )
    clean(f)
  } finally {
    RecoveryAuthority.prototype.head = original
    f.close()
  }
})
