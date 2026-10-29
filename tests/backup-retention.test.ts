import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, readFile, writeFile, symlink, unlink, access } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import { backup } from '../scripts/backup.ts'
import { BackupRetention } from '../src/backup-retention/service.ts'
async function fixture() {
  const base = await mkdtemp(join(tmpdir(), 'he-retention-')),
    root = join(base, 'managed'),
    registry = join(base, 'registry.sqlite'),
    database = join(base, 'live', 'db.sqlite'),
    mediaRoot = join(base, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  const store = new Store(database)
  let service = BackupRetention.registerRoot(root, registry)
  const create = async (name = 'backup-one') => {
    await backup({ database, mediaRoot, destination: join(root, name) })
    const createdAt = JSON.parse(
      await readFile(join(root, name, 'manifest.json'), 'utf8'),
    ).createdAt
    const expiry = Date.parse(createdAt) + 86400000
    service.registerBackup(name, new Date(expiry).toISOString())
    return expiry
  }
  return {
    base,
    root,
    registry,
    store,
    get service() {
      return service
    },
    create,
    restart: () => {
      service.close()
      service = new BackupRetention(registry)
    },
    close: async () => {
      service.close()
      store.close()
      await rm(base, { recursive: true, force: true })
    },
  }
}
test('only explicitly registered backups expire; default dry-run preserves bytes and independent authority', async () => {
  const f = await fixture()
  try {
    const expiry = await f.create()
    await mkdir(join(f.root, 'unregistered'))
    await writeFile(join(f.root, 'unregistered', 'source.tar'), 'not a managed backup')
    const authority = metadata(f.store.db)!.authority
    const before = await readFile(join(f.root, 'backup-one', 'humanengine.sqlite'))
    const dry = f.service.expire({ now: expiry + 1 })
    assert.equal(dry.dryRun, true)
    assert.equal(dry.items[0]!.state, 'retained')
    assert.deepEqual(await readFile(join(f.root, 'backup-one', 'humanengine.sqlite')), before)
    const result = f.service.expire({ execute: true, now: expiry + 1 })
    assert.equal(result.items[0]!.state, 'verified_absent')
    await assert.rejects(access(join(f.root, 'backup-one')))
    await access(authority)
    await access(join(f.root, 'unregistered', 'source.tar'))
    assert.equal(
      f.service.expire({ execute: true, now: expiry + 2 }).items[0]!.state,
      'verified_absent',
    )
  } finally {
    await f.close()
  }
})
test('extra files, symlink replacement and mismatched identity refuse removal', async () => {
  for (const mode of ['extra', 'symlink', 'replace'] as const) {
    const f = await fixture()
    try {
      const expiry = await f.create(),
        file = join(f.root, 'backup-one', 'humanengine.sqlite'),
        outside = join(f.base, 'outside')
      await writeFile(outside, 'DO_NOT_DELETE')
      if (mode === 'extra')
        await writeFile(join(f.root, 'backup-one', 'source.tar'), 'unregistered')
      if (mode === 'symlink') {
        await unlink(file)
        await symlink(outside, file)
      }
      if (mode === 'replace') await writeFile(file, 'different')
      const result = f.service.expire({ execute: true, now: expiry + 1 })
      assert.equal(result.items[0]!.state, 'unverifiable')
      assert.equal(await readFile(outside, 'utf8'), 'DO_NOT_DELETE')
      await access(join(f.root, 'backup-one'))
    } finally {
      await f.close()
    }
  }
})
test('pending deletion resumes after process restart with only already-verified missing members allowed', async () => {
  const f = await fixture()
  try {
    const expiry = await f.create()
    const db = new Database(f.registry)
    const row = db.prepare('SELECT value FROM retention_backups').get() as { value: string }
    const value = JSON.parse(row.value)
    value.state = 'deletion_pending'
    db.prepare('UPDATE retention_backups SET value=?').run(JSON.stringify(value))
    db.close()
    await unlink(join(f.root, 'backup-one', 'humanengine.sqlite'))
    f.restart()
    assert.equal(
      f.service.expire({ execute: true, now: expiry + 1 }).items[0]!.state,
      'verified_absent',
    )
    await assert.rejects(access(join(f.root, 'backup-one')))
  } finally {
    await f.close()
  }
})
test('registration rejects traversal, symlink ancestry and source archives; registry cannot be inside deletion root', async () => {
  const f = await fixture()
  try {
    assert.throws(() => f.service.registerBackup('../live', '2027-01-01T00:00:00Z'))
    await symlink(join(f.base, 'live'), join(f.root, 'linked'))
    assert.throws(() => f.service.registerBackup('linked', '2027-01-01T00:00:00Z'), /unsafe/)
    await mkdir(join(f.root, 'archive'))
    await writeFile(join(f.root, 'archive', 'source.tar'), 'source')
    assert.throws(() => f.service.registerBackup('archive', '2027-01-01T00:00:00Z'))
    assert.throws(() => BackupRetention.registerRoot(f.root, join(f.root, 'registry')), /overlap/)
  } finally {
    await f.close()
  }
})

test('backup normalization affects only the copied database, never the live WAL or authority head', async () => {
  const f = await fixture()
  try {
    const mode = f.store.db.pragma('journal_mode', { simple: true }),
      gate = metadata(f.store.db)
    const livePath = join(f.base, 'live', 'db.sqlite'),
      before = await readFile(livePath)
    await f.create()
    assert.equal(f.store.db.pragma('journal_mode', { simple: true }), mode)
    assert.deepEqual(metadata(f.store.db), gate)
    assert.deepEqual(await readFile(livePath), before)
    const db = new Database(join(f.root, 'backup-one', 'humanengine.sqlite'), { readonly: true })
    try {
      assert.equal(db.pragma('journal_mode', { simple: true }), 'delete')
    } finally {
      db.close()
    }
    for (const suffix of ['-wal', '-shm'])
      await assert.rejects(access(join(f.root, 'backup-one', 'humanengine.sqlite' + suffix)))
  } finally {
    await f.close()
  }
})

test('a managed root containing the independent authority cannot register its backups', async () => {
  const f = await fixture(),
    control = await mkdtemp(join(tmpdir(), 'he-retention-control-'))
  let dangerous: BackupRetention | undefined
  try {
    const destination = join(f.base, 'whole-root-backup')
    await backup({
      database: join(f.base, 'live', 'db.sqlite'),
      mediaRoot: join(f.base, 'live', 'media'),
      destination,
    })
    dangerous = BackupRetention.registerRoot(f.base, join(control, 'registry.sqlite'))
    assert.throws(
      () => dangerous!.registerBackup('whole-root-backup', '2027-01-01T00:00:00Z'),
      /retention_authority_overlap/,
    )
    await access(metadata(f.store.db)!.authority)
  } finally {
    dangerous?.close()
    await f.close()
    await rm(control, { recursive: true, force: true })
  }
})

test('inventory reports changed registered material as unverifiable without treating it as successfully erased', async () => {
  const f = await fixture()
  try {
    await f.create()
    await writeFile(join(f.root, 'backup-one', 'unmanaged.txt'), 'keep')
    const report = f.service.inventory()
    assert.equal(report.coverage, 'registered_only')
    assert.equal(report.items[0]!.state, 'unverifiable')
    assert.equal(report.items[0]!.verifiedAt, null)
    await access(join(f.root, 'backup-one', 'humanengine.sqlite'))
  } finally {
    await f.close()
  }
})
