import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { MemoryService } from '../src/memory.ts'
import { metadata, revoke, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { backup } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

test('old snapshot restore requires retained authority and purges later deletion', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-'))
  const db = join(root, 'live', 'humanengine.sqlite')
  const store = new Store(db)
  const authorityDirectory = metadata(store.db)!.authority
  try {
    store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'a',
      accountId: '1',
      platformSceneId: '2',
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const record = memory.create({
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'fact',
      content: 'SYNTHETIC_DELETED_FACT',
    })
    await mkdir(join(root, 'media'))
    await backup({
      database: db,
      mediaRoot: join(root, 'media'),
      destination: join(root, 'backup'),
    })
    memory.delete(record.id)
    await assert.rejects(
      restore({ backupRoot: join(root, 'backup'), destination: join(root, 'denied') }),
      /recovery_authority_required/,
    )
    const output = await restore({
      backupRoot: join(root, 'backup'),
      destination: join(root, 'restored'),
      authorityDirectory,
    })
    const restored = new Store(output.database)
    try {
      assert.equal(new MemoryService(restored).adminList().length, 0)
      assert.equal(
        restored.db
          .prepare('SELECT 1 FROM memory_assertion_revisions WHERE assertion_id=?')
          .get(record.id),
        undefined,
      )
      assert.equal(metadata(restored.db)!.head.sequence, metadata(store.db)!.head.sequence)
    } finally {
      restored.close()
    }
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('rolled-back revocation blocks same process and cannot be skipped by a later write', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-'))
  const store = new Store(join(root, 'live', 'humanengine.sqlite'))
  try {
    assert.throws(
      () =>
        store.db
          .transaction(() => {
            revoke(store.db, {
              kind: 'assertion',
              id: 'synthetic',
              throughVersion: 1,
              terminal: true,
            })
            throw new Error('rollback')
          })
          .immediate(),
      /rollback/,
    )
    assert.throws(() => store.get('x', null), /recovery_authority_head_changed/)
    assert.throws(() => store.recall('s', 'later'), /recovery_authority_head_changed/)
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('nonempty identity-less database cannot enroll implicitly', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-')),
    path = join(root, 'old.sqlite')
  const db = new Database(path)
  db.exec('CREATE TABLE old_data(value TEXT)')
  db.close()
  try {
    assert.throws(() => new Store(path), /recovery_legacy_enrollment_required/)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})

test('second connection observes a rollback gap and old copied authority cannot release a restore', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-'))
  const path = join(root, 'live', 'humanengine.sqlite'),
    first = new Store(path)
  const second = new Store(path)
  try {
    const memory = new MemoryService(first)
    first.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'a',
      accountId: '1',
      platformSceneId: '2',
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
    const record = memory.create({
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'fact',
      content: 'synthetic',
    })
    await mkdir(join(root, 'media'))
    await backup({
      database: path,
      mediaRoot: join(root, 'media'),
      destination: join(root, 'backup'),
    })
    const { cp } = await import('node:fs/promises')
    const copy = join(root, 'copied-authority')
    await cp(metadata(first.db)!.authority, copy, { recursive: true })
    assert.throws(() =>
      first.db
        .transaction(() => {
          revoke(first.db, { kind: 'assertion', id: record.id, throughVersion: 1, terminal: true })
          throw new Error('rollback')
        })
        .immediate(),
    )
    assert.throws(
      () => new MemoryService(second).list({ sceneId: 's', simulation: false }),
      /recovery_authority_head_changed/,
    )
    await assert.rejects(
      restore({
        backupRoot: join(root, 'backup'),
        destination: join(root, 'restored'),
        authorityDirectory: copy,
      }),
      /recovery_instance_mismatch/,
    )
  } finally {
    first.close()
    second.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('unapplied history redaction is applied despite a higher local version without deleting valid current knowledge', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-'))
  const database = join(root, 'live', 'humanengine.sqlite'),
    store = new Store(database)
  const authorityDirectory = metadata(store.db)!.authority
  try {
    store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'a',
      accountId: '1',
      platformSceneId: '2',
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const record = memory.create({
      scope: { kind: 'scene', sceneId: 's' },
      kind: 'fact',
      content: 'private synthetic',
    })
    for (let i = 0; i < 4; i++) memory.update(record.id, { importance: i % 2 ? 'high' : 'low' })
    await mkdir(join(root, 'media'))
    await backup({ database, mediaRoot: join(root, 'media'), destination: join(root, 'backup') })
    const authority = new RecoveryAuthority(authorityDirectory)
    authority.append(
      { kind: 'assertion', id: record.id, throughVersion: 1, terminal: false },
      authority.head(),
    )
    authority.close()
    const output = await restore({
      backupRoot: join(root, 'backup'),
      destination: join(root, 'restored'),
      authorityDirectory,
    })
    const restored = new Store(output.database)
    try {
      assert.equal(new MemoryService(restored).adminList().length, 1)
      assert.equal(
        restored.db
          .prepare(
            'SELECT 1 FROM memory_assertion_revisions WHERE assertion_id=? AND version<=5 AND value IS NOT NULL',
          )
          .get(record.id),
        undefined,
      )
    } finally {
      restored.close()
    }
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('malformed legacy habit data cannot retain a revoked source and release recovery', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-'))
  const database = join(root, 'live', 'humanengine.sqlite'),
    store = new Store(database)
  const authorityDirectory = metadata(store.db)!.authority
  try {
    store.db.exec('CREATE TABLE habits(id TEXT PRIMARY KEY, fingerprint TEXT UNIQUE, value TEXT)')
    store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(
      'h',
      'f',
      JSON.stringify({
        sources: [{ sceneId: 's', messageId: 'm', senderId: 'p' }],
        content: 'SYNTHETIC_REVOKED_HABIT',
      }),
    )
    await mkdir(join(root, 'media'))
    await backup({ database, mediaRoot: join(root, 'media'), destination: join(root, 'backup') })
    store.recall('s', 'm')
    await assert.rejects(
      restore({
        backupRoot: join(root, 'backup'),
        destination: join(root, 'restored'),
        authorityDirectory,
      }),
      /recovery_domain_source_remaining/,
    )
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('unknown reader format and forged applied prefix do not clear quarantine', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-')),
    path = join(root, 'live', 'humanengine.sqlite')
  const store = new Store(path)
  const gate = metadata(store.db)!
  try {
    const { saveGate } = await import('../src/memory/recovery/ledger.ts')
    saveGate(store.db, { ...gate, minimumReader: 99 })
    assert.throws(() => new Store(path), /unsupported/)
    assert.throws(() => store.get('settings', {}), /unsupported/)
    saveGate(store.db, { ...gate, head: { ...gate.head, hash: 'f'.repeat(64) } })
    assert.throws(() => new Store(path), /head_changed/)
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('restored source recall retains independent OR proof and strips the withdrawn source', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-')),
    database = join(root, 'live', 'humanengine.sqlite'),
    store = new Store(database)
  const authorityDirectory = metadata(store.db)!.authority
  try {
    store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'a',
      accountId: '1',
      platformSceneId: '2',
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    let id = ''
    for (const messageId of ['first', 'independent']) {
      const source = { sceneId: 's', messageId, senderId: 'p' }
      const result = memory.propose(
        {
          sceneId: 's',
          simulation: false,
          provenance: 'source-only',
          dependencyMemoryIds: [],
          sourceEvidence: [{ ...source, available: true, isBot: false }],
        },
        { kind: 'fact', content: 'SYNTHETIC_INDEPENDENT_OR', sources: [source] },
      )
      assert.equal(result.status, 'saved')
      if (result.status === 'saved') id = result.memory.id
    }
    await mkdir(join(root, 'media'))
    await backup({ database, mediaRoot: join(root, 'media'), destination: join(root, 'backup') })
    memory.recall('s', 'first')
    const expected = memory.list({ sceneId: 's', simulation: false })
    assert.equal(expected.length, 1)
    const output = await restore({
      backupRoot: join(root, 'backup'),
      destination: join(root, 'restored'),
      authorityDirectory,
    })
    const restored = new Store(output.database)
    try {
      const actual = new MemoryService(restored).list({ sceneId: 's', simulation: false })
      assert.equal(actual.length, 1)
      assert.equal(actual[0]!.id, id)
      assert.deepEqual(actual[0]!.sources, expected[0]!.sources)
      assert.equal(
        restored.db
          .prepare(
            'SELECT 1 FROM memory_assertion_revision_sources WHERE scene_id=? AND message_id=?',
          )
          .get('s', 'first'),
        undefined,
      )
    } finally {
      restored.close()
    }
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('authority transaction CAS prevents independent database writers from skipping a revocation', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-')),
    store = new Store(join(root, 'live', 'humanengine.sqlite'))
  const path = metadata(store.db)!.authority
  const first = new RecoveryAuthority(path),
    second = new RecoveryAuthority(path)
  try {
    const stale = first.head()
    second.append({ kind: 'source', sceneId: 's', messageId: 'b' }, stale)
    assert.throws(
      () => first.append({ kind: 'source', sceneId: 's', messageId: 'a' }, stale),
      /head_changed/,
    )
    assert.equal(first.head().sequence, stale.sequence + 1)
    assert.equal(first.entries().length, 1)
    assert.throws(() => store.get('value', null), /head_changed/)
  } finally {
    first.close()
    second.close()
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})

test('root source intent survives a fault before the first historical redaction', async () => {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-')),
    database = join(root, 'live', 'humanengine.sqlite'),
    store = new Store(database)
  const authorityDirectory = metadata(store.db)!.authority
  try {
    store.saveScene({
      id: 's',
      kind: 'group',
      adapterId: 'a',
      accountId: '1',
      platformSceneId: '2',
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
    const memory = new MemoryService(store)
    const source = { sceneId: 's', messageId: 'withdrawn', senderId: 'p' }
    const result = memory.propose(
      {
        sceneId: 's',
        simulation: false,
        provenance: 'source-only',
        dependencyMemoryIds: [],
        sourceEvidence: [{ ...source, available: true, isBot: false }],
      },
      { kind: 'fact', content: 'SYNTHETIC_RECALL_FAULT', sources: [source] },
    )
    assert.equal(result.status, 'saved')
    await mkdir(join(root, 'media'))
    await backup({ database, mediaRoot: join(root, 'media'), destination: join(root, 'backup') })
    const { AssertionHistory } = await import('../src/memory/storage/assertion-history.ts')
    const original = AssertionHistory.prototype.redactSource
    AssertionHistory.prototype.redactSource = () => {
      throw new Error('synthetic_fault_before_redact')
    }
    try {
      assert.throws(() => memory.recall('s', 'withdrawn'), /synthetic_fault/)
    } finally {
      AssertionHistory.prototype.redactSource = original
    }
    assert.throws(() => memory.list({ sceneId: 's', simulation: false }), /head_changed/)
    const output = await restore({
      backupRoot: join(root, 'backup'),
      destination: join(root, 'restored'),
      authorityDirectory,
    })
    const restored = new Store(output.database)
    try {
      assert.equal(new MemoryService(restored).list({ sceneId: 's', simulation: false }).length, 0)
      assert.equal(restored.recalled('s', 'withdrawn'), true)
    } finally {
      restored.close()
    }
  } finally {
    store.close()
    await rm(root, { recursive: true, force: true })
  }
})
