import test from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync, readFileSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import Database from 'better-sqlite3'
import { enroll, metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { enrollLegacy } from '../scripts/recovery-authority.ts'

function legacy(path: string) {
  const db = new Database(path)
  db.exec(
    "CREATE TABLE legacy_data(value TEXT); INSERT INTO legacy_data VALUES('SYNTHETIC_RETAINED_ROW')",
  )
  db.close()
}
function fixture(run: (root: string, database: string, authority: string) => void) {
  const root = mkdtempSync(join(tmpdir(), 'he-enrollment-'))
  const database = join(root, 'data', 'humanengine.sqlite')
  mkdirSync(join(root, 'data'))
  legacy(database)
  try {
    run(root, database, join(root, 'authority'))
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

test('readonly and nested-transaction enrollment fail before any external authority is created', () => {
  fixture((_root, database, authority) => {
    const before = readFileSync(database)
    const readonly = new Database(database, { readonly: true })
    try {
      assert.throws(() => enroll(readonly, database, authority), /readonly/)
    } finally {
      readonly.close()
    }
    assert.equal(existsSync(authority), false)
    assert.deepEqual(readFileSync(database), before)
    const db = new Database(database)
    try {
      db.transaction(() =>
        assert.throws(
          () => enroll(db, database, authority),
          /recovery_enrollment_transaction_active/,
        ),
      )()
    } finally {
      db.close()
    }
    assert.equal(existsSync(authority), false)
    assert.deepEqual(readFileSync(database), before)
  })
})

test('incomplete or malformed gate is not treated as a legacy enrollment opportunity', () => {
  for (const malformed of [false, true])
    fixture((_root, database, authority) => {
      const db = new Database(database)
      db.exec('CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT)')
      if (malformed)
        db.prepare('INSERT INTO recovery_gate VALUES(1,?)').run('SYNTHETIC_PRIVATE_INVALID_JSON')
      db.close()
      const before = readFileSync(database)
      assert.throws(() => enrollLegacy(database, authority), { message: 'recovery_gate_invalid' })
      assert.deepEqual(readFileSync(database), before)
      assert.equal(existsSync(authority), false)
    })
})

test('an existing external authority is never reused or overwritten for another enrollment', () => {
  fixture((_root, database, authority) => {
    mkdirSync(authority)
    const sidecar = join(authority, 'authority.sqlite')
    writeFileSync(sidecar, 'SYNTHETIC_RETAINED_AUTHORITY')
    const before = readFileSync(database)
    assert.throws(
      () => enrollLegacy(database, authority),
      /recovery_existing_authority_requires_reconciliation/,
    )
    assert.deepEqual(readFileSync(database), before)
    assert.equal(readFileSync(sidecar, 'utf8'), 'SYNTHETIC_RETAINED_AUTHORITY')
  })
})

test('ordinary failure after exclusive authority reservation rolls back the gate and retains a closed fail-closed obligation', () => {
  fixture((_root, database, authority) => {
    const db = new Database(database)
    const before = readFileSync(database)
    const original = RecoveryAuthority.prototype.head
    RecoveryAuthority.prototype.head = () => {
      throw Error('synthetic_failure_after_authority_creation')
    }
    try {
      assert.throws(
        () => enroll(db, database, authority),
        /synthetic_failure_after_authority_creation/,
      )
    } finally {
      RecoveryAuthority.prototype.head = original
      db.close()
    }
    assert.deepEqual(readFileSync(database), before)
    const sidecar = join(authority, 'authority.sqlite')
    assert.ok(existsSync(sidecar))
    const reserved = readFileSync(sidecar)
    assert.throws(
      () => enrollLegacy(database, authority),
      /recovery_existing_authority_requires_reconciliation/,
    )
    assert.deepEqual(readFileSync(sidecar), reserved)
  })
})

function contender(database: string, authority: string) {
  return new Promise<{ code: number | null; output: string }>((resolve, reject) => {
    const entry = fileURLToPath(new URL('../scripts/recovery-authority.ts', import.meta.url))
    const child = spawn(
      process.execPath,
      [
        '--import',
        'tsx',
        entry,
        'enroll',
        database,
        authority,
        '--current-live-database-not-a-backup',
      ],
      { stdio: ['ignore', 'pipe', 'pipe'] },
    )
    let output = ''
    child.stdout.on('data', (data) => {
      output += data
    })
    child.stderr.on('data', (data) => {
      output += data
    })
    child.on('error', reject)
    child.on('exit', (code) => resolve({ code, output }))
  })
}

test('competing enrollment processes cannot both establish different authority identities for one database', async () => {
  const root = mkdtempSync(join(tmpdir(), 'he-enrollment-race-'))
  const database = join(root, 'data', 'humanengine.sqlite')
  mkdirSync(join(root, 'data'))
  legacy(database)
  try {
    const results = await Promise.all([
      contender(database, join(root, 'authority-one')),
      contender(database, join(root, 'authority-two')),
    ])
    assert.deepEqual(results.map((x) => x.code).sort(), [0, 1])
    const db = new Database(database)
    try {
      const gate = metadata(db)!
      assert.equal(
        (db.prepare('SELECT value FROM legacy_data').get() as { value: string }).value,
        'SYNTHETIC_RETAINED_ROW',
      )
      const other = gate.authority.endsWith('authority-one') ? 'authority-two' : 'authority-one'
      assert.equal(existsSync(join(root, other)), false)
    } finally {
      db.close()
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('competing databases cannot share one newly reserved authority', async () => {
  const root = mkdtempSync(join(tmpdir(), 'he-authority-race-'))
  mkdirSync(join(root, 'data'))
  const first = join(root, 'data', 'first.sqlite'),
    second = join(root, 'data', 'second.sqlite')
  legacy(first)
  legacy(second)
  try {
    const results = await Promise.all([
      contender(first, join(root, 'authority')),
      contender(second, join(root, 'authority')),
    ])
    assert.deepEqual(results.map((x) => x.code).sort(), [0, 1])
    const databases = [new Database(first), new Database(second)]
    try {
      assert.equal(databases.filter((db) => metadata(db)).length, 1)
    } finally {
      databases.forEach((db) => db.close())
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
