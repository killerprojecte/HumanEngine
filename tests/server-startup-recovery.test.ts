import test from 'node:test'
import assert from 'node:assert/strict'
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  renameSync,
  existsSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import { enrollLegacy } from '../scripts/recovery-authority.ts'
import { startupDiagnostic } from '../src/diagnostics/startup.ts'
import { createSessionGateway } from '../src/auth/session.ts'
import { initializeEngine, handleEngineRequest, closeEngine } from '../server/utils/engine.ts'

const token = 'synthetic-upgrade-admin-token'
async function fixture(run: (database: string, authority: string, root: string) => Promise<void>) {
  const root = mkdtempSync(join(tmpdir(), 'he-startup-upgrade-'))
  const database = join(root, 'data', 'humanengine.sqlite')
  const authority = join(root, 'authority')
  mkdirSync(join(root, 'data'))
  const names = [
    'HUMANENGINE_DB',
    'HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY',
    'HUMANENGINE_ADMIN_TOKEN',
    'HUMANENGINE_PLUGINS',
    'HUMANENGINE_LOG_DIR',
  ]
  const previous = names.map((name) => process.env[name])
  process.env.HUMANENGINE_DB = database
  process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY = authority
  process.env.HUMANENGINE_ADMIN_TOKEN = token
  process.env.HUMANENGINE_PLUGINS = ''
  process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
  try {
    await run(database, authority, root)
  } finally {
    await closeEngine()
    names.forEach((name, i) =>
      previous[i] === undefined ? delete process.env[name] : (process.env[name] = previous[i]),
    )
    rmSync(root, { recursive: true, force: true })
  }
}
const request = (authorization = `Bearer ${token}`) =>
  new Request('http://localhost/api/status', { headers: { Authorization: authorization } })
function legacy(database: string) {
  const db = new Database(database)
  db.exec(
    'CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE legacy_rows(id TEXT PRIMARY KEY,content TEXT NOT NULL)',
  )
  db.prepare('INSERT INTO config VALUES(?,?)').run(
    'settings',
    JSON.stringify({
      name: 'Synthetic legacy installation',
      timezone: 'UTC',
      persona: 'Synthetic local fixture',
      cacheEnabled: true,
      cacheHours: 24,
      debounceMs: 1200,
      maxWaitMs: 6000,
    }),
  )
  db.prepare('INSERT INTO legacy_rows VALUES(?,?)').run(
    'retained',
    'SYNTHETIC_EXISTING_BUSINESS_DATA',
  )
  db.close()
}

test('legacy startup stays closed with authenticated actionable 503, then explicit offline enrollment survives restart', async () => {
  await fixture(async (database, authority, root) => {
    legacy(database)
    const before = readFileSync(database)
    const logs: unknown[][] = []
    const original = console.error
    console.error = (...values) => {
      logs.push(values)
    }
    try {
      await initializeEngine()
      const gateway = createSessionGateway()
      for (const authorization of ['Bearer wrong', 'Basic synthetic-upgrade-admin-token', '']) {
        const response = await gateway(request(authorization), handleEngineRequest, { token })
        assert.equal(response.status, 401)
        assert.doesNotMatch(await response.text(), /recovery_|enroll|authority/)
      }
      const response = await gateway(request(), handleEngineRequest, { token })
      assert.equal(response.status, 503)
      assert.equal(response.headers.get('Cache-Control'), 'no-store')
      assert.match(
        (await response.json()).error,
        /recovery_legacy_enrollment_required.*current live database offline/,
      )
      assert.deepEqual(readFileSync(database), before)
      assert.equal(existsSync(authority), false)
      assert.equal(existsSync(join(root, 'data', '.secrets.key')), false)
      assert.equal(logs.length, 1)
      assert.match(JSON.stringify(logs), /recovery_legacy_enrollment_required/)
      assert.doesNotMatch(JSON.stringify(logs), new RegExp(root))
      // An operator action does not cause requests to retry a failed startup in place.
      enrollLegacy(database, authority)
      assert.equal((await handleEngineRequest(request())).status, 503)
      await closeEngine()
      await initializeEngine()
      const status = await handleEngineRequest(request())
      assert.equal(status.status, 200)
      assert.equal((await status.json()).name, 'Synthetic legacy installation')
      await closeEngine()
      const db = new Database(database)
      const identity = metadata(db)!.head.instanceId
      assert.equal(
        (db.prepare('SELECT content FROM legacy_rows').get() as { content: string }).content,
        'SYNTHETIC_EXISTING_BUSINESS_DATA',
      )
      db.close()
      await initializeEngine()
      assert.equal((await handleEngineRequest(request())).status, 200)
      await closeEngine()
      const reopened = new Database(database)
      assert.equal(metadata(reopened)!.head.instanceId, identity)
      reopened.close()
    } finally {
      console.error = original
    }
  })
})

test('fresh empty installation still automatically enrolls and restarts with the same authority', async () => {
  await fixture(async (database) => {
    await initializeEngine()
    assert.equal((await handleEngineRequest(request())).status, 200)
    await closeEngine()
    const db = new Database(database)
    const first = metadata(db)!
    db.close()
    await initializeEngine()
    assert.equal((await handleEngineRequest(request())).status, 200)
    await closeEngine()
    const second = new Database(database)
    assert.deepEqual(metadata(second), first)
    second.close()
  })
})

test('missing or corrupt enrolled authority remains blocked, never reenrolled or replaced', async () => {
  for (const variant of ['missing-directory', 'missing-file', 'corrupt'])
    await fixture(async (database, authority, root) => {
      const corrupt = variant === 'corrupt'
      const store = new Store(database)
      store.close()
      const before = readFileSync(database)
      if (corrupt)
        writeFileSync(join(authority, 'authority.sqlite'), 'SYNTHETIC_CORRUPT_PRIVATE_SIDECAR')
      else if (variant === 'missing-file')
        renameSync(join(authority, 'authority.sqlite'), join(root, 'retained-authority.sqlite'))
      else renameSync(authority, join(root, 'retained-authority'))
      const sidecar = corrupt ? readFileSync(join(authority, 'authority.sqlite')) : undefined
      const response = await handleEngineRequest(request())
      assert.equal(response.status, 503)
      const body = await response.text()
      assert.match(body, corrupt ? /engine_startup_failed/ : /recovery_authority_missing/)
      assert.doesNotMatch(
        body,
        /SYNTHETIC_CORRUPT_PRIVATE_SIDECAR|recovery_legacy_enrollment_required/,
      )
      assert.deepEqual(readFileSync(database), before)
      if (corrupt) assert.deepEqual(readFileSync(join(authority, 'authority.sqlite')), sidecar)
      else assert.equal(existsSync(join(authority, 'authority.sqlite')), false)
      assert.throws(
        () => enrollLegacy(database, join(root, 'replacement')),
        /recovery_already_enrolled/,
      )
      assert.equal(existsSync(join(root, 'replacement')), false)
    })
})

test('damaged gate and unknown errors produce bounded diagnostics without retry or raw content', async () => {
  await fixture(async (database, authority) => {
    legacy(database)
    const db = new Database(database)
    db.exec('CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT)')
    db.close()
    const before = readFileSync(database)
    const response = await handleEngineRequest(request())
    assert.equal(response.status, 503)
    assert.match(await response.text(), /recovery_gate_invalid/)
    assert.deepEqual(readFileSync(database), before)
    assert.equal(existsSync(authority), false)
  })
  const diagnostic = startupDiagnostic(
    new Error('file /private/key.sqlite secret SYNTHETIC_SECRET'),
  )
  assert.equal(diagnostic.code, 'engine_startup_failed')
  assert.doesNotMatch(JSON.stringify(diagnostic), /key.sqlite|SYNTHETIC_SECRET/)
})
