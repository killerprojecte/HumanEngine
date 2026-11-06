import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { PluginManager } from '../src/plugins/manager.ts'
import { currentSourceSnapshot } from '../src/runtime/source-events.ts'
import { Runtime, type RuntimeMessage, type TurnContext } from '../src/runtime.ts'
import {
  RecoveryAuthority,
  metadata,
  revoke,
  validateUnrecalledDependencies,
} from '../src/memory/recovery/ledger.ts'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'dependency-read-scope-'))
  const store = new Store(join(root, 'data', 'state.sqlite'))
  store.saveScene({
    id: 's',
    name: 's',
    kind: 'group',
    adapterId: 'fixture',
    accountId: 'bot',
    platformSceneId: 'group',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  })
  store.db.transaction(() =>
    revoke(store.db, { kind: 'source', sceneId: 'other', messageId: 'synthetic' }),
  )()
  const authority = new RecoveryAuthority(metadata(store.db)!.authority)
  const messages: RuntimeMessage[] = Array.from({ length: 24 }, (_, i) => ({
    id: String(i),
    sceneId: 's',
    senderId: 'person',
    origin: 'live',
    status: 'available',
    sentAt: new Date().toISOString(),
    receivedAt: new Date().toISOString(),
    parts: [{ type: 'text', text: 'synthetic' }],
  }))
  return {
    store,
    authority,
    messages,
    close() {
      authority.close()
      store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}
test('fixed snapshot batch verifies full chain bounded times and restores writable connections', () => {
  const f = fixture()
  const original = RecoveryAuthority.prototype.entries
  let scans = 0
  RecoveryAuthority.prototype.entries = function () {
    scans++
    return original.call(this)
  }
  try {
    assert.equal(
      validateUnrecalledDependencies(
        f.store,
        's',
        f.messages.map((message) => message.id),
      ),
      true,
    )
    assert.equal(scans, 2)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    f.store.saveScene({ ...f.store.scene('s')!, name: 'updated' })
    assert.equal(f.store.scene('s')!.name, 'updated')
  } finally {
    RecoveryAuthority.prototype.entries = original
    f.close()
  }
})
test('external writer is blocked during pinned batch and immediately detected after scope', () => {
  const f = fixture()
  const recalled = f.store.recalled.bind(f.store)
  let attempted = false
  f.authority.db.pragma('busy_timeout = 0')
  f.store.recalled = (scene, id) => {
    if (!attempted) {
      attempted = true
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
    assert.equal(
      validateUnrecalledDependencies(
        f.store,
        's',
        f.messages.map((message) => message.id),
      ),
      true,
    )
    assert.equal(attempted, true)
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    assert.throws(
      () =>
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
      /recovery_chain_invalid/,
    )
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
  } finally {
    f.close()
  }
})
test('same-connection writes and durable revoke are denied inside source reads; exceptions restore state', () => {
  const f = fixture()
  const recalled = f.store.recalled.bind(f.store)
  let checked = false
  f.store.recalled = (scene, id) => {
    if (!checked) {
      checked = true
      assert.throws(
        () =>
          f.store.db
            .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
            .run('s', '0', new Date().toISOString()),
        /readonly/,
      )
      assert.throws(
        () => revoke(f.store.db, { kind: 'source', sceneId: 's', messageId: '0' }),
        /recovery_read_scope_write_denied/,
      )
      throw new Error('synthetic_read_failure')
    }
    return recalled(scene, id)
  }
  try {
    const before = f.authority.head()
    assert.throws(
      () =>
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
      /synthetic_read_failure/,
    )
    assert.deepEqual(f.authority.head(), before)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    assert.equal(
      validateUnrecalledDependencies(
        f.store,
        's',
        f.messages.map((message) => message.id),
      ),
      true,
    )
    f.store.recall('s', '0')
    assert.equal(
      validateUnrecalledDependencies(
        f.store,
        's',
        f.messages.map((message) => message.id),
      ),
      false,
    )
  } finally {
    f.close()
  }
})
test('existing main transaction falls back to original checks without changing transaction ownership', () => {
  const f = fixture()
  try {
    f.store.db.transaction(() => {
      assert.equal(
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
        true,
      )
      assert.equal(f.store.db.inTransaction, true)
      assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
      f.store.db
        .prepare('INSERT INTO recall_tombstones VALUES(?,?,?)')
        .run('s', '0', new Date().toISOString())
      assert.equal(
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
        false,
      )
    })()
    assert.equal(f.store.recalled('s', '0'), true)
  } finally {
    f.close()
  }
})
test('external valid authority append after scope does not retain old permission', () => {
  const f = fixture()
  try {
    assert.equal(
      validateUnrecalledDependencies(
        f.store,
        's',
        f.messages.map((message) => message.id),
      ),
      true,
    )
    f.authority.append({ kind: 'source', sceneId: 's', messageId: '0' }, f.authority.head(), true)
    assert.throws(
      () =>
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
      /recovery_authority_head_changed/,
    )
  } finally {
    f.close()
  }
})

test('the same batch fails if authority changes immediately after snapshot unlock', () => {
  const f = fixture()
  const pragma = f.store.db.pragma.bind(f.store.db)
  let changed = false
  f.store.db.pragma = ((source: string, options?: unknown) => {
    const value = (pragma as any)(source, options)
    if (source === 'query_only = OFF' && !changed) {
      changed = true
      f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run('0'.repeat(64))
    }
    return value
  }) as typeof f.store.db.pragma
  try {
    assert.throws(
      () =>
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
      /recovery_chain_invalid/,
    )
    assert.equal(changed, true)
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(pragma('query_only', { simple: true }), 0)
  } finally {
    f.store.db.pragma = pragma as typeof f.store.db.pragma
    f.close()
  }
})

test('preexisting authority writer contention fails within configured busy timeout without a leaked scope', () => {
  const f = fixture()
  const bound = (f.store as unknown as { recoveryAuthority: RecoveryAuthority }).recoveryAuthority
  bound.db.pragma('busy_timeout = 10')
  try {
    f.authority.db.exec('BEGIN EXCLUSIVE')
    assert.throws(
      () =>
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((message) => message.id),
        ),
      /locked/,
    )
    f.authority.db.exec('ROLLBACK')
    assert.equal(f.store.db.inTransaction, false)
    assert.equal(bound.db.inTransaction, false)
    assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
    assert.equal(bound.db.pragma('query_only', { simple: true }), 0)
    assert.equal(
      validateUnrecalledDependencies(
        f.store,
        's',
        f.messages.map((message) => message.id),
      ),
      true,
    )
  } finally {
    if (f.authority.db.inTransaction) f.authority.db.exec('ROLLBACK')
    f.close()
  }
})

test('empty batch performs no authority reads and rejects nonprimitive dependency input', () => {
  const f = fixture()
  const original = RecoveryAuthority.prototype.entries
  let scans = 0
  RecoveryAuthority.prototype.entries = function () {
    scans++
    return original.call(this)
  }
  try {
    assert.equal(validateUnrecalledDependencies(f.store, 's', []), true)
    assert.equal(scans, 0)
    assert.equal(validateUnrecalledDependencies(f.store, 's', [{} as string]), false)
    assert.equal(scans, 0)
  } finally {
    RecoveryAuthority.prototype.entries = original
    f.close()
  }
})

test('dependency iterator runs before readonly scope and its revocation is observed', () => {
  const f = fixture()
  const ids = ['0']
  ids[Symbol.iterator] = function* () {
    assert.equal(f.store.db.inTransaction, false)
    f.store.recall('s', '0')
    yield '0'
    return undefined
  }
  try {
    assert.equal(validateUnrecalledDependencies(f.store, 's', ids), false)
  } finally {
    f.close()
  }
})

test('final dependency read mutation is rejected and rolled back', () => {
  const f = fixture()
  const recalled = f.store.recalled.bind(f.store)
  let calls = 0
  f.store.recalled = (scene, id) => {
    const result = recalled(scene, id)
    if (++calls === f.messages.length) {
      f.store.db.pragma('query_only = OFF')
      f.store.db.prepare('INSERT INTO config(key,value) VALUES(?,?)').run('scope-canary', 'true')
      f.store.db.pragma('query_only = ON')
    }
    return result
  }
  try {
    assert.throws(
      () =>
        validateUnrecalledDependencies(
          f.store,
          's',
          f.messages.map((m) => m.id),
        ),
      /recovery_read_scope_changed/,
    )
    assert.equal(f.store.get('scope-canary', false), false)
    assert.equal(f.store.db.inTransaction, false)
  } finally {
    f.close()
  }
})

for (const mutation of ['recall', 'abort', 'reject', 'add-recalled-dependency'] as const)
  test(`runtime extension ${mutation} remains outside snapshot and prevents eligibility`, () => {
    const f = fixture()
    const runtime = new Runtime(f.store)
    const controller = new AbortController()
    const context: TurnContext = {
      scene: f.store.scene('s')!,
      mode: 'real',
      turnId: 'fixture',
      signal: controller.signal,
      dependencies: new Set(['0']),
      validations: [],
    }
    runtime.ingest(f.messages[0]!, context.scene, false)
    let calls = 0
    context.validations.push(() => {
      calls++
      assert.equal(f.store.db.inTransaction, false)
      assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
      if (mutation === 'recall') f.store.recall('s', '0')
      if (mutation === 'abort') controller.abort()
      if (mutation === 'add-recalled-dependency') {
        f.store.recall('s', 'new')
        context.dependencies.add('new')
      }
      return mutation !== 'reject'
    })
    try {
      assert.equal((runtime as any).eligible(context), false)
      assert.equal(calls, 1)
    } finally {
      runtime.dispose()
      f.close()
    }
  })

test('runtime dependency authorization expires across an await', async () => {
  const f = fixture()
  const runtime = new Runtime(f.store)
  const context: TurnContext = {
    scene: f.store.scene('s')!,
    mode: 'real',
    turnId: 'fixture',
    signal: new AbortController().signal,
    dependencies: new Set(['0']),
    validations: [],
  }
  runtime.ingest(f.messages[0]!, context.scene, false)
  try {
    assert.equal((runtime as any).eligible(context), true)
    await Promise.resolve()
    f.store.recall('s', '0')
    assert.equal((runtime as any).eligible(context), false)
  } finally {
    runtime.dispose()
    f.close()
  }
})

test('cached result owner uses fixed batches without memoizing extension guards', async () => {
  const f = fixture()
  const runtime = new Runtime(f.store)
  let extensionCalls = 0
  let recall = false
  runtime.addExtension({
    tools: [],
    execute: async () => undefined,
    captureToolResultGuards: () => [
      () => {
        extensionCalls++
        assert.equal(f.store.db.inTransaction, false)
        assert.equal(f.store.db.pragma('query_only', { simple: true }), 0)
        if (recall) f.store.recall('s', '0')
        return true
      },
    ],
  })
  const manager = new PluginManager(f.store, runtime)
  const context: TurnContext = {
    scene: f.store.scene('s')!,
    mode: 'real',
    turnId: 'fixture',
    signal: new AbortController().signal,
    dependencies: new Set(f.messages.map((m) => m.id)),
    validations: [],
    sourceSnapshots: f.messages,
  }
  const owner = (manager as any).resultOwner(context, 'fixture') as {
    validations: Array<() => boolean>
  }
  const original = RecoveryAuthority.prototype.entries
  let scans = 0
  RecoveryAuthority.prototype.entries = function () {
    scans++
    return original.call(this)
  }
  try {
    assert.equal(
      owner.validations.every((valid) => valid()),
      true,
    )
    assert.equal(scans, 4)
    assert.equal(extensionCalls, 1)
    await Promise.resolve()
    recall = true
    assert.equal(
      owner.validations.every((valid) => valid()),
      false,
    )
    assert.equal(extensionCalls, 2)
  } finally {
    RecoveryAuthority.prototype.entries = original
    await manager.dispose()
    runtime.dispose()
    f.close()
  }
})
