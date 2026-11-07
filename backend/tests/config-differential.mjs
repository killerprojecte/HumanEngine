/** Real TCP + SQLite differential against the frozen TypeScript runtime; synthetic data only. */
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { Store } from '../../src/store.ts'
import { ensureAssertionEligibilityBaseline } from '../../src/memory/recovery/eligibility.ts'
import { metadata, RecoveryAuthority } from '../../src/memory/recovery/ledger.ts'
const root = mkdtempSync(join(tmpdir(), 'humanengine-go-diff-'))
process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
const database = join(root, 'data', 'live.sqlite'),
  store = new Store(database)
ensureAssertionEligibilityBaseline(store.db)
store.saveModels(
  {
    providers: [
      {
        id: 'present',
        name: 'Synthetic provider',
        protocol: 'openai-chat',
        baseUrl: 'https://example.com/v1',
      },
      {
        id: 'absent',
        name: 'No credential',
        protocol: 'openai-responses',
        baseUrl: 'https://example.org',
      },
    ],
    models: [
      {
        id: 'fixture-model',
        displayName: 'Fixture model',
        providerId: 'present',
        apiModelName: 'synthetic',
        capabilities: ['text', 'tools'],
      },
    ],
    routes: [{ role: 'thinker', orderedModelIds: ['fixture-model'], timeoutMs: 1000 }],
  },
  { present: { action: 'set', value: 'synthetic-test-only' } },
)
// Old persisted routes must normalize on read, without rewriting the user's database.
const legacyModels = JSON.parse(
  store.db.prepare("SELECT value FROM config WHERE key='models'").get().value,
)
legacyModels.routes[0].role = 'thinker'
store.db.prepare("UPDATE config SET value=? WHERE key='models'").run(JSON.stringify(legacyModels))
store.saveScene({
  id: 'fixture',
  name: '中文 fixture',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'fixture',
  participationEnabled: false,
  learningEnabled: false,
  proactiveEnabled: false,
})
const expected = {
  '/api/settings': store.settings,
  '/api/models': store.models,
  '/api/scenes': store.scenes(),
}
const authorityPath = metadata(store.db).authority
await store.closeLogs()
store.close()
let child
try {
  child = spawn(
    process.argv[2] ?? '/tmp/humanengine-go-preview',
    ['--read-only-preview', '--database', database, '--listen', '127.0.0.1:0'],
    {
      env: { ...process.env, HUMANENGINE_ADMIN_TOKEN: 'synthetic-test-token' },
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  )
  let stderr = ''
  child.stderr.on('data', (b) => (stderr += b))
  const address = await new Promise((resolve, reject) => {
    let text = ''
    child.stdout.on('data', (b) => {
      text += b
      for (const line of text.split('\n')) {
        try {
          const e = JSON.parse(line)
          if (e.code === 'server.listening') resolve(e.fields.address)
        } catch {}
      }
    })
    child.once('exit', (code) => reject(new Error(`preview exited ${code}: ${stderr}`)))
  })
  const base = 'http://' + address,
    headers = { Authorization: 'Bearer synthetic-test-token' }
  assert.equal((await fetch(base + '/api/settings')).status, 401)
  for (const [path, value] of Object.entries(expected)) {
    const response = await fetch(base + path, { headers })
    assert.equal(response.status, 200, await response.clone().text())
    assert.deepEqual(await response.json(), value, path)
    const head = await fetch(base + path, { method: 'HEAD', headers })
    assert.equal(head.status, 200)
    assert.equal(await head.text(), '')
  }
  assert.equal((await fetch(base + '/api/settings', { method: 'PUT', headers })).status, 503)
  const authority = new RecoveryAuthority(authorityPath)
  const before = authority.head()
  authority.append({ kind: 'source', sceneId: 'fixture', messageId: 'unacknowledged' }, before)
  authority.close()
  assert.equal(
    (await fetch(base + '/api/settings', { headers })).status,
    503,
    'durable authority/main ACK gap must refuse stale projection',
  )
  child.kill('SIGTERM')
  await once(child, 'exit')
  child = undefined
  console.log(
    'PASS: real TCP settings/models/scenes match Node; HEAD/auth preserved; unsupported writes and durable ACK gap rejected',
  )
} finally {
  if (child) {
    child.kill('SIGTERM')
    await once(child, 'exit')
  }
  rmSync(root, { recursive: true, force: true })
}
