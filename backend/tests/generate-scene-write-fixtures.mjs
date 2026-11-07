/** Actual Store.saveScene oracle. Synthetic SQLite only; no live adapters. */
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../../src/store.ts'
import { metadata } from '../../src/memory/recovery/ledger.ts'
if (process.argv.slice(2).some((argument) => argument !== '--check'))
  throw Error('Only --check is supported')
if (process.version !== 'v24.22.1') throw Error('Pinned Node 24.22.1 required')
for (const [path, expected] of [
  ['src/store.ts', 'a181a5f0c2b5362873662f1f33e79055a284322b2cc7fa28cabbaf6bc6b0ac71'],
  [
    'src/memory/recovery/configuration-contract.ts',
    'eb62278c9b7eda4b17e4812a0f6d3495501c27e39428b343395d94fb755117ce',
  ],
])
  if (
    createHash('sha256')
      .update(readFileSync(new URL('../../' + path, import.meta.url)))
      .digest('hex') !== expected
  )
    throw Error('Scene oracle source changed')
const root = mkdtempSync(join(tmpdir(), 'he-scene-write-'))
process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
const store = new Store(join(root, 'data', 'live.sqlite'))
const ledger = new Database(join(metadata(store.db).authority, 'authority.sqlite'), {
  readonly: true,
})
const base = {
  id: 'scene',
  kind: 'group',
  adapterId: 'adapter',
  accountId: 'bot',
  platformSceneId: 'chat',
  name: 'Fixture',
  participationEnabled: true,
  learningEnabled: true,
  proactiveEnabled: false,
}
const cases = []
const add = (name, previous, input) => cases.push({ name, previous, input })
add('create', null, base)
add('ignore_supplied_revision', null, { ...base, memoryReadRevision: 99 })
add('unchanged', base, base)
add('presentation_only', base, {
  ...base,
  name: 'New name',
  avatarUrl: 'https://example.com/avatar',
})
for (const flag of [
  'participationEnabled',
  'learningEnabled',
  'proactiveEnabled',
  'memoryReadEnabled',
  'personalizationEnabled',
]) {
  add('toggle_' + flag, base, { ...base, [flag]: flag === 'proactiveEnabled' })
  add('explicit_false_' + flag, { ...base, [flag]: false }, { ...base, [flag]: true })
}
for (const field of ['adapterId', 'accountId', 'platformSceneId', 'kind'])
  add('identity_' + field, base, { ...base, [field]: field === 'kind' ? 'private' : 'changed' })
add(
  'retain_omitted_controls',
  { ...base, memoryReadEnabled: false, personalizationEnabled: false, memoryReadRevision: 7 },
  base,
)
add('stable_revision', { ...base, memoryReadRevision: 7 }, { ...base, memoryReadRevision: 100 })
add('lone_surrogate_identity', base, { ...base, accountId: '\ud800', name: '\udfff' })
add('escaped_identity', base, { ...base, accountId: '<>&\u2028\u2029' })
for (const revision of [-1, 0.5, 9007199254740991, 9007199254740992])
  add('revision_' + revision, { ...base, memoryReadRevision: revision }, base)
try {
  for (const entry of cases) {
    store.db.prepare('DELETE FROM scenes').run()
    if (entry.previous)
      store.db
        .prepare('INSERT INTO scenes(id,value) VALUES(?,?)')
        .run('scene', JSON.stringify(entry.previous))
    const start = ledger.prepare('SELECT coalesce(max(sequence),0) n FROM revocations').get().n
    let error
    try {
      store.saveScene(entry.input)
    } catch (e) {
      error = e.message
    }
    entry.output =
      store.db.prepare('SELECT value FROM scenes WHERE id=?').get('scene')?.value ?? null
    entry.events = ledger
      .prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence')
      .all(start)
      .map((row) => row.payload)
    if (error) entry.error = error
  }
  store.db.prepare('DELETE FROM scenes').run()
  const keyCases = []
  for (const id of ['\ud800', '\udfff', '\ufffd', '😀', 'a\0b']) {
    const input = { ...base, id }
    const start = ledger.prepare('SELECT coalesce(max(sequence),0) n FROM revocations').get().n
    store.saveScene(input)
    keyCases.push({
      input,
      rows: store.db.prepare('SELECT hex(id) AS keyHex,value FROM scenes ORDER BY id').all(),
      events: ledger
        .prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence')
        .all(start)
        .map((row) => row.payload),
    })
  }
  const output = JSON.stringify({ cases, keyCases }, null, 2) + '\n'

  const target = new URL('../internal/scenestore/testdata/write.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (readFileSync(target, 'utf8') !== output) throw Error('Scene write fixtures changed')
  } else {
    mkdirSync(new URL('.', target), { recursive: true })
    writeFileSync(target, output)
  }
  console.log(`${cases.length} scene write oracle cases`)
} finally {
  ledger.close()
  await store.closeLogs()
  store.close()
  rmSync(root, { recursive: true, force: true })
}
