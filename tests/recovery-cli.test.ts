import test, { after, before, type TestContext } from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import Database from 'better-sqlite3'
import { metadata } from '../src/memory/recovery/ledger.ts'

const project = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const acknowledgment = '--current-live-database-not-a-backup'
const fixtureValue = 'SYNTHETIC_PRIVATE_LEGACY_ROW_测试'
let outputDirectory: string
let cli: string
const env = {
  PATH: process.env.PATH,
  HOME: tmpdir(),
  TMPDIR: tmpdir(),
  NODE_OPTIONS: `--import=${join(project, 'scripts/acceptance/network-deny.mjs')}`,
}

before(async () => {
  await mkdir(join(project, '.output'), { recursive: true })
  outputDirectory = await mkdtemp(join(project, '.output/recovery-cli-test-'))
  cli = join(outputDirectory, 'recovery-authority.mjs')
  const result = spawnSync(process.execPath, ['scripts/build-recovery-cli.mjs', cli], {
    cwd: project,
    env,
    encoding: 'utf8',
    timeout: 30_000,
  })
  assert.equal(result.status, 0, result.stderr)
  assert.equal(existsSync(cli), true)
})
after(async () => {
  if (outputDirectory) await rm(outputDirectory, { recursive: true, force: true })
})
function run(...args: string[]) {
  // The production entrypoint runs under plain Node, with no tsx loader/source.
  return spawnSync(process.execPath, [cli, ...args], {
    cwd: outputDirectory,
    env,
    encoding: 'utf8',
    timeout: 15_000,
  })
}
async function fixture(t: TestContext) {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-cli-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const database = join(root, 'live', 'humanengine.sqlite')
  const authority = join(root, 'authority')
  await mkdir(dirname(database))
  const db = new Database(database)
  db.exec('CREATE TABLE legacy_data(id TEXT PRIMARY KEY, value TEXT NOT NULL)')
  db.prepare('INSERT INTO legacy_data VALUES(?, ?)').run('one', fixtureValue)
  db.close()
  return { root, database, authority }
}
const digest = async (file: string) =>
  createHash('sha256')
    .update(await readFile(file))
    .digest('hex')

test('bundled enrollment requires the exact acknowledgment before any database write', async (t) => {
  const { database, authority } = await fixture(t)
  const before = await digest(database)
  for (const args of [
    ['enroll', database, authority],
    ['enroll', database, authority, '--assume-current'],
    ['enroll', database, authority, acknowledgment, 'extra'],
  ]) {
    const result = run(...args)
    assert.equal(result.status, 1)
    assert.match(result.stderr, /usage:/)
    assert.match(result.stderr, /Stop all writers/)
    assert.equal(result.stdout, '')
    assert.equal(await digest(database), before)
    assert.equal(existsSync(authority), false)
  }
})

test('plain Node bundled enrollment preserves legacy rows and refuses reenrollment', async (t) => {
  const { database, authority } = await fixture(t)
  const result = run('enroll', database, authority, acknowledgment)
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /enrollment completed/)
  assert.equal(result.stderr, '')
  const db = new Database(database, { readonly: true })
  try {
    assert.deepEqual(db.prepare('SELECT * FROM legacy_data').all(), [
      { id: 'one', value: fixtureValue },
    ])
    const gate = metadata(db)!
    assert.equal(gate.authority, authority)
    assert.equal(gate.state, 'ready')
    assert.equal(gate.head.sequence, 0)
  } finally {
    db.close()
  }
  const before = await digest(database)
  const authorityBefore = await digest(join(authority, 'authority.sqlite'))
  const repeated = run('enroll', database, authority, acknowledgment)
  assert.equal(repeated.status, 1)
  assert.match(repeated.stderr, /recovery_already_enrolled:/)
  assert.equal(await digest(database), before)
  assert.equal(await digest(join(authority, 'authority.sqlite')), authorityBefore)
  assert.equal((result.stdout + repeated.stderr).includes(fixtureValue), false)
})

test('missing authority is not recreated by enrollment or export', async (t) => {
  const { root, database, authority } = await fixture(t)
  assert.equal(run('enroll', database, authority, acknowledgment).status, 0)
  await rename(authority, join(root, 'retained-authority'))
  const before = await digest(database)
  const enrolled = run('enroll', database, authority, acknowledgment)
  assert.equal(enrolled.status, 1)
  assert.match(enrolled.stderr, /recovery_already_enrolled:/)
  const exported = run('export', database, join(root, 'export.json'))
  assert.equal(exported.status, 1)
  assert.match(exported.stderr, /recovery_authority_missing:/)
  assert.equal(existsSync(authority), false)
  assert.equal(existsSync(join(root, 'export.json')), false)
  assert.equal(await digest(database), before)
})

test('a preexisting authority cannot silently enroll another database', async (t) => {
  const first = await fixture(t)
  const second = await fixture(t)
  assert.equal(run('enroll', first.database, first.authority, acknowledgment).status, 0)
  const before = await digest(second.database)
  const authorityBefore = await digest(join(first.authority, 'authority.sqlite'))
  const result = run('enroll', second.database, first.authority, acknowledgment)
  assert.equal(result.status, 1)
  assert.match(result.stderr, /recovery_existing_authority_requires_reconciliation:/)
  assert.equal(await digest(second.database), before)
  assert.equal(await digest(join(first.authority, 'authority.sqlite')), authorityBefore)
})

test('bundled CLI never emits private database bytes or paths on an unknown failure', async (t) => {
  const { database, authority } = await fixture(t)
  const db = new Database(database)
  db.exec('CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY, value TEXT NOT NULL)')
  db.prepare('INSERT INTO recovery_gate VALUES(1, ?)').run('SYNTHETIC_PRIVATE_GATE_CANARY')
  db.close()
  const before = await digest(database)
  const result = run('enroll', database, authority, acknowledgment)
  assert.equal(result.status, 1)
  assert.match(result.stderr, /recovery_operation_failed:|recovery_gate_invalid:/)
  assert.equal(result.stdout, '')
  for (const privateValue of [database, authority, fixtureValue, 'SYNTHETIC_PRIVATE_GATE_CANARY'])
    assert.equal(result.stderr.includes(privateValue), false)
  assert.equal(await digest(database), before)
  assert.equal(existsSync(authority), false)
})

test('bundled enrollment refuses a nonexistent live database without creating it', async (t) => {
  const { root } = await fixture(t)
  const database = join(root, 'missing-private-live.sqlite')
  const authority = join(root, 'new-authority')
  const result = run('enroll', database, authority, acknowledgment)
  assert.equal(result.status, 1)
  assert.match(result.stderr, /recovery_database_unavailable:/)
  assert.equal(result.stderr.includes(database), false)
  assert.equal(existsSync(database), false)
  assert.equal(existsSync(authority), false)
})

test('bundled export is metadata only and never overwrites an existing output', async (t) => {
  const { root, database, authority } = await fixture(t)
  assert.equal(run('enroll', database, authority, acknowledgment).status, 0)
  const output = join(root, 'metadata.json')
  const result = run('export', database, output)
  assert.equal(result.status, 0, result.stderr)
  const record = JSON.parse(await readFile(output, 'utf8'))
  assert.equal(record.purpose, 'transport-only-not-freshness-authority')
  assert.deepEqual(record.entries, [])
  assert.equal(JSON.stringify(record).includes(fixtureValue), false)
  await writeFile(output, 'SYNTHETIC_EXISTING_EXPORT')
  assert.equal(run('export', database, output).status, 1)
  assert.equal(await readFile(output, 'utf8'), 'SYNTHETIC_EXISTING_EXPORT')
})
