// Actual source facade + registry + SQLite + recovery hook; synthetic data only.
// HUMANENGINE_SOURCE=<repository> ORACLE_OUTPUT=<overlay output> node --import
// <repository>/node_modules/tsx/dist/loader.mjs <this file>
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { pathToFileURL } from 'node:url'
assert.equal(process.version, 'v24.22.1')
const root = resolve(process.env.HUMANENGINE_SOURCE ?? process.cwd())
const load = (p) => import(pathToFileURL(`${root}/${p}`))
const { Store } = await load('src/store.ts')
const { IdentityFacade } = await load('src/memory/identities/facade.ts')
const { IdentityRegistry } = await load('src/memory/identities/service.ts')
const { recordIdentityMutation } = await load('src/memory/recovery/identities.ts')
const endpoint = { adapterId: 'adapter', accountId: 'account', personId: 'person' }
const alias = (id, sceneId = 's', messageId = 'm', manual = false) => ({
  id,
  endpoint,
  label: 'Synthetic ' + id,
  scopeSceneId: sceneId,
  source: manual
    ? { kind: 'manual', administratorId: 'admin' }
    : {
        kind: 'observation',
        sceneId,
        messageId,
        senderId: 'person',
        eventRef: { eventId: 'event-' + id, revision: 1, deletionEpoch: 0 },
      },
  validFrom: null,
  validUntil: null,
})
const base = () => ({
  format: 1,
  revision: 0,
  edges: [],
  aliases: [
    alias('match'),
    alias('manual', 's', 'm', true),
    alias('other-message', 's', 'other'),
    alias('other-scene', 'other', 'm'),
  ],
  operations: [],
})
const cases = []
function run(name, input, scene = 's', message = 'm') {
  const store = new Store(':memory:'),
    events = []
  const facade = new IdentityFacade(store, { list: () => [] })
  // Record the same real mutation hook without changing registry business logic.
  facade.registry = new IdentityRegistry(store.db, {
    assertReady: () => store.assertRecoveryReady(),
    beforeWrite: (m) => {
      recordIdentityMutation(store.db, m)
      events.push(structuredClone(m))
    },
  })
  if (input !== null) {
    store.db.exec(
      'CREATE TABLE memory_identity_registry(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)',
    )
    store.db.prepare('INSERT INTO memory_identity_registry VALUES(1,?)').run(JSON.stringify(input))
  }
  let error
  try {
    store.db.transaction(() => facade.sourceRecalled(scene, message))()
  } catch (e) {
    error = e.message
  }
  const exists = store.db
    .prepare("SELECT 1 FROM sqlite_master WHERE name='memory_identity_registry'")
    .get()
  const after = exists
    ? JSON.parse(
        store.db.prepare('SELECT value FROM memory_identity_registry WHERE id=1').get().value,
      )
    : null
  cases.push({ name, input, scene, message, after, events, ...(error ? { error } : {}) })
  store.close()
}
run('mixed-aliases', base())
run('missing-table', null)
run('unmatched', base(), 'missing', 'm')
const reordered = base()
reordered.aliases = reordered.aliases.map((a) => Object.fromEntries(Object.entries(a).reverse()))
run('schema-key-order', Object.fromEntries(Object.entries(reordered).reverse()))
const after = cases[0].after
run('repeat-noop', after)
const invalid = base()
invalid.aliases.push(invalid.aliases[0])
run('duplicate-alias-corrupt', invalid)
const extra = base()
extra.extra = true
run('extra-field-corrupt', extra)
const expired = base()
expired.aliases[0].validFrom = '2020-01-01T00:00:00Z'
expired.aliases[0].validUntil = '2021-01-01T00:00:00Z'
run('expired-observation-still-purged', expired)
const invalidDate = base()
invalidDate.aliases[0].validFrom = '2026-02-30T00:00:00Z'
run('invalid-date-corrupt', invalidDate)
const sources = Object.fromEntries(
  [
    'src/memory/identities/facade.ts',
    'src/memory/identities/service.ts',
    'src/memory/identities/storage.ts',
    'src/memory/identities/contracts.ts',
    'src/memory/recovery/identities.ts',
    'package-lock.json',
  ].map((p) => [
    p,
    createHash('sha256')
      .update(readFileSync(`${root}/${p}`))
      .digest('hex'),
  ]),
)
const output = resolve(
  process.env.ORACLE_OUTPUT ??
    `${root}/backend/internal/identityprivacy/testdata/source-recall.json`,
)
const raw = JSON.stringify({ node: process.version, sources, cases }, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(output, 'utf8'), raw)
else {
  mkdirSync(dirname(output), { recursive: true })
  writeFileSync(output, raw)
}
console.log(JSON.stringify({ cases: cases.length }))
