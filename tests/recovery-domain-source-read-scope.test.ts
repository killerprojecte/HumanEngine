import { recoveryHashProbe } from './helpers/recovery-hashes.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Store } from '../src/store.ts'
import { domainSourceCurrent } from '../src/memory/read/domain-source-events.ts'
import {
  currentDomainSources,
  metadata,
  RecoveryAuthority,
  revoke,
} from '../src/memory/recovery/ledger.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'domain-source-scope-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 's',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  store.db.transaction(() =>
    revoke(store.db, { kind: 'source', sceneId: 'other', messageId: 'synthetic' }),
  )()
  const sources = Array.from({ length: 24 }, (_, i) => ({
    sceneId: 's',
    messageId: String(i),
    senderId: 'person',
  }))
  store.sourceEvents.observe({
    kind: 'ingress',
    identity: { ...sources[0]!, adapterId: 'fixture', accountId: 'bot' },
    bodyHash: 'a'.repeat(64),
    occurredAt: null,
  })
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  return {
    store,
    sources,
    authority,
    close() {
      authority.close()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('domain-source batch preserves source qualification and bounds full-chain scans', () => {
  const f = fixture(),
    original = recoveryHashProbe()
  try {
    const expected = f.sources.map((source) => domainSourceCurrent(f.store, source))
    original.start()
    assert.deepEqual(currentDomainSources(f.store, f.sources), expected)
    assert.equal(original.count, 0)
    original.stop()
    f.store.recall('s', '2')
    assert.equal(currentDomainSources(f.store, f.sources)[2], false)
    const registered = f.store.sourceEvents.registered('s', '0')!
    assert.deepEqual(
      currentDomainSources(f.store, [
        { ...f.sources[0]!, eventRef: registered.ref },
        { ...f.sources[0]!, senderId: 'wrong' },
      ]),
      [true, false],
    )
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  } finally {
    original.stop()
    f.close()
  }
})

test('domain-source read scope denies same-connection and external authority writes and detects later tampering', () => {
  const f = fixture(),
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
    assert.ok(currentDomainSources(f.store, f.sources).every(Boolean))
    assert.equal(attempted, true)
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(() => currentDomainSources(f.store, f.sources), /recovery_chain_invalid/)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  } finally {
    f.close()
  }
})

test('domain-source exceptions restore writes and nested transactions retain live checks', () => {
  const f = fixture(),
    recalled = f.store.recalled.bind(f.store)
  try {
    f.store.recalled = () => {
      throw new Error('synthetic')
    }
    assert.throws(() => currentDomainSources(f.store, f.sources), /synthetic/)
    f.store.recalled = recalled
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    f.store.db.transaction(() => {
      assert.ok(currentDomainSources(f.store, f.sources).every(Boolean))
      f.store.db
        .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
        .run('s', '3', new Date().toISOString())
      assert.equal(currentDomainSources(f.store, f.sources)[3], false)
      assert.equal(f.store.db.inTransaction, true)
    })()
  } finally {
    f.close()
  }
})

test('caller accessors execute before the fixed snapshot and cannot bless a recalled source', () => {
  const f = fixture()
  try {
    const source = { ...f.sources[1]! }
    Object.defineProperty(source, 'messageId', {
      enumerable: true,
      get() {
        f.store.recall('s', '1')
        return '1'
      },
    })
    assert.deepEqual(currentDomainSources(f.store, [source]), [false])
    assert.equal(f.store.db.inTransaction, false)
  } finally {
    f.close()
  }
})
