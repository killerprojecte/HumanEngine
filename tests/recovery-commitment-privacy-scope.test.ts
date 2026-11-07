import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { readMemoryList } from '../src/memory/read/list.ts'
import { readCommitmentPrivacy } from '../src/memory/read/commitment-privacy.ts'
import {
  captureCommitmentPrivacySnapshot,
  listMemorySnapshot,
  metadata,
  RecoveryAuthority,
  revoke,
} from '../src/memory/recovery/ledger.ts'

function fixture(count = 3) {
  const root = mkdtempSync(join(tmpdir(), 'commitment-privacy-scope-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'offline',
    accountId: 'offline',
    platformSceneId: 's',
    participationEnabled: false,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  const memory = new MemoryService(store)
  const records = Array.from({ length: count }, (_, i) =>
    memory.create({
      scope: { kind: 'scene', sceneId: 's' },
      content: `Synthetic ${i}`,
      kind: 'fact',
      sources: [{ sceneId: 's', messageId: String(i), senderId: 'person' }],
    }),
  )
  store.db.transaction(() =>
    revoke(store.db, { kind: 'source', sceneId: 'other', messageId: 'seed' }),
  )()
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  const bound = (store as unknown as { recoveryAuthority: RecoveryAuthority }).recoveryAuthority
  return {
    store,
    memory,
    records,
    authority,
    bound,
    close() {
      memory.dispose()
      authority.close()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}
function restored(f: ReturnType<typeof fixture>, nested: boolean) {
  assert.equal(f.store.db.inTransaction, nested)
  assert.equal(f.bound.db.inTransaction, false)
  assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  assert.equal(f.bound.db.pragma('query_only', { simple: true }), 0)
}
for (const nested of [false, true])
  test(`capture preserves exact refs with two scans, nested=${nested}`, () => {
    const f = fixture(16),
      original = RecoveryAuthority.prototype.entries
    try {
      const baseline = readCommitmentPrivacy(f.store)
      assert.equal(baseline.size, 16)
      let scans = 0
      RecoveryAuthority.prototype.entries = function (collect) {
        scans++
        return original.call(this, collect)
      }
      const run = () => {
        assert.deepEqual(captureCommitmentPrivacySnapshot(f.store), baseline)
        assert.equal(scans, 2)
        restored(f, nested)
        f.store.db.prepare('INSERT INTO config VALUES(?,?)').run('after-capture', 'true')
      }
      if (nested) f.store.db.transaction(run)()
      else run()
      restored(f, false)
      assert.equal(f.store.get('after-capture', false), true)
    } finally {
      RecoveryAuthority.prototype.entries = original
      f.close()
    }
  })
test('capture sees uncommitted outer changes without committing or rolling them back', () => {
  const f = fixture()
  try {
    assert.throws(
      () =>
        f.store.db.transaction(() => {
          f.store.db
            .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
            .run('s', '0', new Date().toISOString())
          assert(!captureCommitmentPrivacySnapshot(f.store).has(f.records[0]!.id))
          restored(f, true)
          throw new Error('rollback outer')
        })(),
      /rollback outer/,
    )
    assert(captureCommitmentPrivacySnapshot(f.store).has(f.records[0]!.id))
    restored(f, false)
  } finally {
    f.close()
  }
})
test('capture blocks local writes and external authority tamper, then detects changed rows', () => {
  const f = fixture(),
    recalled = f.store.recalled.bind(f.store)
  let checked = false
  f.authority.db.pragma('busy_timeout = 0')
  f.store.recalled = (scene, id) => {
    if (!checked) {
      checked = true
      assert.throws(
        () => f.store.db.prepare('INSERT INTO config VALUES(?,?)').run('bad', 'true'),
        /readonly/,
      )
      assert.throws(
        () => revoke(f.store.db, { kind: 'source', sceneId: 's', messageId: '0' }),
        /recovery_read_scope_write_denied/,
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
    f.store.db.transaction(() => {
      assert.equal(captureCommitmentPrivacySnapshot(f.store).size, 3)
      restored(f, true)
    })()
    assert(checked)
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(() => captureCommitmentPrivacySnapshot(f.store), /recovery_chain_invalid/)
    restored(f, false)
  } finally {
    f.close()
  }
})
test('failed pre-recall capture restores writes so the durable root revocation is still recorded', () => {
  const f = fixture(),
    recalled = f.store.recalled.bind(f.store),
    before = metadata(f.store.db)!.head
  let injected = false
  f.store.recalled = (scene, id) => {
    if (!injected) {
      injected = true
      throw new Error('synthetic capture failure')
    }
    return recalled(scene, id)
  }
  try {
    assert.throws(() => f.memory.recall('s', '0'), /synthetic capture failure/)
    restored(f, false)
    assert.equal(metadata(f.store.db)!.head.sequence, before.sequence)
    assert.equal(f.authority.head().sequence, before.sequence + 1)
    assert.throws(() => f.store.assertRecoveryReady(), /recovery_authority_head_changed/)
  } finally {
    f.close()
  }
})
test('post-unlock validation fails before captured refs escape', () => {
  const f = fixture(),
    original = RecoveryAuthority.prototype.entries
  let scans = 0
  RecoveryAuthority.prototype.entries = function (collect) {
    if (++scans === 2)
      f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    return original.call(this, collect)
  }
  try {
    assert.throws(
      () => f.store.db.transaction(() => captureCommitmentPrivacySnapshot(f.store))(),
      /recovery_chain_invalid/,
    )
    restored(f, false)
  } finally {
    RecoveryAuthority.prototype.entries = original
    f.close()
  }
})
test('capture invariant failure rolls back its savepoint while preserving caller changes', () => {
  const f = fixture(),
    recalled = f.store.recalled.bind(f.store)
  let injected = false
  f.store.recalled = (scene, id) => {
    const result = recalled(scene, id)
    if (!injected) {
      injected = true
      f.store.db.pragma('query_only = OFF')
      f.store.db.prepare('INSERT INTO config VALUES(?,?)').run('bad', 'true')
      f.store.db.pragma('query_only = ON')
    }
    return result
  }
  try {
    f.store.db.transaction(() => {
      f.store.db.prepare('INSERT INTO config VALUES(?,?)').run('outer', 'true')
      assert.throws(() => captureCommitmentPrivacySnapshot(f.store), /recovery_read_scope_changed/)
      restored(f, true)
      assert.equal(f.store.get('bad', false), false)
      assert.equal(f.store.get('outer', false), true)
    })()
  } finally {
    f.close()
  }
})

for (const nested of [false, true])
  test(`list qualification preserves exact ordered results with two scans, nested=${nested}`, () => {
    const f = fixture(16),
      original = RecoveryAuthority.prototype.entries
    const input = { sceneId: 's', simulation: false }
    try {
      const baseline = readMemoryList(f.store, input)
      assert.equal(baseline.length, 16)
      let scans = 0
      RecoveryAuthority.prototype.entries = function (collect) {
        scans++
        return original.call(this, collect)
      }
      const run = () => {
        assert.deepEqual(listMemorySnapshot(f.store, input), baseline)
        assert.equal(scans, 2)
        restored(f, nested)
        const ids = [f.records[1]!.id, f.records[0]!.id, f.records[1]!.id, 'missing']
        assert.deepEqual(
          listMemorySnapshot(f.store, { ...input, ids }),
          baseline.filter((r) => ids.includes(r.id)),
        )
        assert.equal(scans, 4)
        restored(f, nested)
      }
      if (nested) f.store.db.transaction(run)()
      else run()
    } finally {
      RecoveryAuthority.prototype.entries = original
      f.close()
    }
  })
test('list uses fresh source recall and main-snapshot rows, with no result reused after rollback', () => {
  const f = fixture(),
    input = { sceneId: 's', simulation: false }
  try {
    const before = listMemorySnapshot(f.store, input)
    assert.throws(
      () =>
        f.store.db.transaction(() => {
          f.store.db
            .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
            .run('s', '0', new Date().toISOString())
          assert(!listMemorySnapshot(f.store, input).some((r) => r.id === f.records[0]!.id))
          restored(f, true)
          throw new Error('rollback list')
        })(),
      /rollback list/,
    )
    assert.deepEqual(listMemorySnapshot(f.store, input), before)
    before[0]!.content = 'poisoned caller result'
    assert.notEqual(listMemorySnapshot(f.store, input)[0]!.content, 'poisoned caller result')
  } finally {
    f.close()
  }
})
test('list keeps caller iterators outside the fixed read scope and preserves simulation filtering', () => {
  const f = fixture()
  try {
    const ids: Iterable<string> = {
      *[Symbol.iterator]() {
        assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
        yield f.records[0]!.id
      },
    }
    assert.equal(
      f.memory.listByIds({ sceneId: 's', simulation: false }, ids as unknown as string[]).length,
      1,
    )
    assert.deepEqual(f.memory.list({ sceneId: 's', simulation: true }), [])
    assert.deepEqual(f.memory.list({ sceneId: 's', simulation: true }, 'person'), [])
    assert.deepEqual(
      f.memory.listByIds({ sceneId: 'other', simulation: false }, [f.records[0]!.id]),
      [],
    )
    restored(f, false)
  } finally {
    f.close()
  }
})
