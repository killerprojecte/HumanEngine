import assert from 'node:assert/strict'
import { accessSync, constants, chmodSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { register } from 'tsx/esm/api'
import Database from 'better-sqlite3'

register()
const { Store } = await import('../../src/store.ts')
const { metadata } = await import('../../src/memory/recovery/ledger.ts')
const { ConfigReadConnection } = await import('../../src/server/config-read.ts')
// Docker builds run as root. The parent drops identity before this process starts,
// so chmod must exercise real OS denial rather than root's DAC override.
assert.notEqual(process.getuid?.(), 0, 'readonly proof must run without root privileges')
const root = mkdtempSync(join(tmpdir(), 'he-readonly-native-proof-'))
const store = new Store(join(root, 'data', 'live.sqlite'))
const file = join(metadata(store.db).authority, 'authority.sqlite')
const closed = []
const original = Database.prototype.close
try {
  chmodSync(file, 0o444)
  assert.throws(() => accessSync(file, constants.W_OK), { code: 'EACCES' })
  Database.prototype.close = function () {
    closed.push(this.readonly)
    return original.call(this)
  }
  assert.throws(() => new ConfigReadConnection(store.db.name), { code: 'SQLITE_READONLY' })
  assert.equal(closed.length, 3, 'failed native proof closes reservation and both readers')
  assert.equal(closed.filter(Boolean).length, 2)
  assert.equal(closed.filter((readonly) => !readonly).length, 1)
} finally {
  Database.prototype.close = original
  chmodSync(file, 0o600)
  await store.closeLogs()
  store.close()
  rmSync(root, { recursive: true, force: true })
}
console.log(JSON.stringify({ nativeReadonlyProof: true, uid: process.getuid?.(), closed }))
