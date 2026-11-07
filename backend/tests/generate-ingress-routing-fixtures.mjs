/** Actual source ingress routing/identity order, before duplicate and recall checks. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { mock } from 'node:test'
import { Store } from '../../src/store.ts'
import { Runtime } from '../../src/runtime.ts'
import { SourceEventStore } from '../../src/memory/storage/event-revisions.ts'
import { displayName, qqAvatar } from '../../src/plugins/builtin/snowluma/profiles.ts'
assert.equal(process.version, 'v24.22.1')
mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00Z') })
const scene = {
  id: 'scene',
  kind: 'group',
  adapterId: 'onebot',
  accountId: 'bot',
  platformSceneId: '12345',
  displayName: 'Synthetic group',
}
scene.id = `ob:s:${Buffer.from(JSON.stringify([scene.adapterId, scene.accountId, scene.kind, scene.platformSceneId])).toString('base64url')}`
const body = {
  id: 'source',
  sceneId: scene.id,
  senderId: 'user',
  senderPlatformId: '23456',
  senderDisplayName: 'Synthetic user',
  origin: 'live',
  status: 'available',
  sentAt: '2026-10-28T00:00:00.000Z',
  receivedAt: '2026-10-28T00:00:00.000Z',
  parts: [{ type: 'text', text: 'Synthetic.' }],
}
const specs = [
  { name: 'missing-scene' },
  { name: 'existing-scene', existing: true },
  { name: 'legacy-topic-callback-order', existing: true, admission: false },
  { name: 'closed-still-refreshes-identity', existing: true, repeat: true, closed: true },
  {
    name: 'simulation-skips-persistence',
    scene: { id: 'sim:fixture', kind: 'simulation' },
    message: { origin: 'simulation' },
  },
  { name: 'duplicate-identity-refresh', existing: true, repeat: true },
  { name: 'recalled-identity-refresh', existing: true, recalled: true },
  { name: 'bot-identity', existing: true, message: { senderId: 'bot' } },
  { name: 'history-skips-routing', message: { origin: 'history' } },
  { name: 'mismatch-skips-routing', message: { sceneId: 'other' } },
  { name: 'non-onebot', scene: { id: 'synthetic-other' } },
  { name: 'direct-scene', scene: { kind: 'direct' }, canonical: true },
  {
    name: 'missing-display',
    scene: { displayName: undefined },
    message: { senderDisplayName: undefined },
  },
  { name: 'empty-display', scene: { displayName: '' }, message: { senderDisplayName: '' } },
  {
    name: 'whitespace-display',
    scene: { displayName: '  Synthetic group  ' },
    message: { senderDisplayName: '  Synthetic user  ' },
  },
  { name: 'no-sender-platform', message: { senderPlatformId: undefined } },
  { name: 'empty-sender-platform', message: { senderPlatformId: '' } },
  { name: 'mismatched-account', existing: true, scene: { accountId: 'other' } },
  { name: 'mismatched-platform', existing: true, scene: { platformSceneId: '99999' } },
  { name: 'raw-null-scene', rawScene: 'null' },
  { name: 'raw-false-scene', rawScene: 'false' },
  { name: 'raw-zero-scene', rawScene: '0' },
  { name: 'raw-empty-scene', rawScene: '""' },
  { name: 'raw-scalar-scene', rawScene: '"synthetic"' },
  { name: 'raw-null-identity', existing: true, rawIdentity: 'null' },
  {
    name: 'raw-null-identity-fallback',
    existing: true,
    rawIdentity: 'null',
    message: { senderDisplayName: undefined },
  },
  {
    name: 'raw-false-identity-fallback',
    existing: true,
    rawIdentity: 'false',
    message: { senderDisplayName: undefined },
  },
  {
    name: 'raw-prior-name-fallback',
    existing: true,
    rawIdentity: '{"senderDisplayName":"Prior user","obsolete":true}',
    message: { senderDisplayName: undefined },
  },
  {
    name: 'identity-unknown-fields',
    existing: true,
    rawIdentity: '{"senderDisplayName":"Old","obsolete":true}',
  },
  { name: 'save-scene-failure', failure: 'saveScene' },
  { name: 'identity-failure', existing: true, failure: 'observeIdentity' },
  { name: 'recall-read-failure', existing: true, failure: 'recalled' },
]
const cases = []
for (const spec of specs) {
  const store = new Store(':memory:')
  store.set('settings', {
    ...store.settings,
    sourceEventRevisionsEnabled: false,
    topicAdmissionEnabled: spec.admission !== false,
  })
  if (spec.existing)
    store.saveScene({
      ...scene,
      name: 'Prior group',
      participationEnabled: false,
      learningEnabled: false,
      proactiveEnabled: false,
    })
  if (spec.rawScene !== undefined)
    store.db
      .prepare('INSERT OR REPLACE INTO scenes(id,value) VALUES(?,?)')
      .run(scene.id, spec.rawScene)
  if (spec.rawIdentity !== undefined)
    store.db
      .prepare('INSERT OR REPLACE INTO config(key,value) VALUES(?,?)')
      .run(`identity:${scene.id}:user`, spec.rawIdentity)
  const runtime = new Runtime(store, () => {
    throw Error('unexpected-model')
  })
  const currentScene = { ...scene, ...spec.scene }
  if (spec.canonical)
    currentScene.id = `ob:s:${Buffer.from(JSON.stringify([currentScene.adapterId, currentScene.accountId, currentScene.kind, currentScene.platformSceneId])).toString('base64url')}`
  const message = { ...body, sceneId: currentScene.id, ...spec.message }
  if (spec.repeat) runtime.ingest(message, currentScene, false)
  if (spec.recalled) store.recall(scene.id, message.id)
  if (spec.closed) runtime.states.get(currentScene.id).closed = true
  const before = {
    scene:
      store.db.prepare('SELECT value FROM scenes WHERE id=?').get(currentScene.id)?.value ?? null,
    identity:
      store.db
        .prepare('SELECT value FROM config WHERE key=?')
        .get(`identity:${currentScene.id}:${message.senderId}`)?.value ?? null,
  }
  const timeline = []
  for (const method of ['saveScene', 'observeIdentity', 'recalled']) {
    const original = store[method].bind(store)
    store[method] = (...args) => {
      timeline.push(method)
      if (spec.failure === method) throw Error(`synthetic_${method}`)
      return original(...args)
    }
  }
  runtime.addExtension({
    received: () =>
      timeline.push({
        kind: 'received',
        topics: [...(runtime.states.get(currentScene.id)?.topics.values() ?? [])].map((topic) => ({
          messageIds: [...topic.messageIds],
          version: topic.version,
        })),
      }),
  })
  if (spec.repeat) message.senderDisplayName = 'Updated user'
  let error = null
  try {
    runtime.ingest(message, currentScene, false)
  } catch (e) {
    error = e.message
  }
  const persistedScene =
    store.db.prepare('SELECT value FROM scenes WHERE id=?').get(currentScene.id)?.value ?? null
  const persistedIdentity =
    store.db
      .prepare('SELECT value FROM config WHERE key=?')
      .get(`identity:${currentScene.id}:${message.senderId}`)?.value ?? null
  cases.push({
    spec,
    before,
    input: { scene: JSON.stringify(currentScene), message: JSON.stringify(message) },
    expected: {
      error,
      timeline,
      persistedScene,
      persistedIdentity,
      registered: runtime.scenes.has(currentScene.id),
      stateExists: runtime.states.has(currentScene.id),
      messages: runtime.states.get(currentScene.id)?.messages ?? [],
    },
  })
  runtime.dispose()
  store.close()
}
const editCases = []
for (const mode of [
  'unchanged',
  'body-mutation',
  'ref-replacement',
  'ref-mutation',
  'history',
  'scene-mismatch',
  'bot-mutation',
  'mention-normalization',
  'sender-normalization',
]) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, sourceEventRevisionsEnabled: true })
  let serial = 0
  store.sourceEventStore = new SourceEventStore(store.db, {
    id: () => `event-${++serial}`,
    now: () => '2026-10-28T00:00:00.000Z',
  })
  const runtime = new Runtime(store, () => {
    throw Error('unexpected-model')
  })
  runtime.addAdapter(scene.adapterId, { dispose() {}, history: async () => [] }, 'bot-user')
  runtime.ingest(body, scene, false)
  runtime.addExtension({
    edited: (_previous, current) => {
      if (mode === 'body-mutation') current.parts = [{ type: 'text', text: 'Changed by callback.' }]
      if (mode === 'ref-replacement')
        current.sourceEventRef = { eventId: 'forged', revision: 999, deletionEpoch: 0 }
      if (mode === 'ref-mutation') current.sourceEventRef.revision++
      if (mode === 'history') current.origin = 'history'
      if (mode === 'scene-mismatch') current.sceneId = 'other'
      if (mode === 'bot-mutation') {
        current.senderId = 'bot'
        current.parts = [{ type: 'text', text: 'Changed by callback.' }]
      }
    },
  })
  const current = { ...body, parts: [{ type: 'text', text: 'Edited.' }] }
  if (mode === 'mention-normalization') current.parts = [{ type: 'mention', userId: 'bot-user' }]
  if (mode === 'sender-normalization') current.senderId = 'bot-user'
  let error = null,
    status = null
  try {
    status = runtime.edit(
      current,
      scene,
      { editId: 'edit-1', order: { kind: 'platform_sequence', sequence: 1 } },
      false,
    ).status
  } catch (e) {
    error = e.message
  }
  editCases.push({
    mode,
    scene: JSON.stringify(scene),
    initial: JSON.stringify(body),
    current: JSON.stringify(current),
    expected: {
      error,
      status,
      messages: JSON.stringify(runtime.states.get(scene.id)?.messages ?? []),
      head: JSON.stringify(store.sourceEvents.registered(scene.id, body.id)),
    },
  })
  runtime.dispose()
  store.close()
}
const profileNames = [
  null,
  false,
  0,
  {},
  [],
  '',
  'Synthetic',
  '  Synthetic  ',
  '\u0000A\u202eB\u2069\u007f',
  '\ufeff\u00a0A\u3000',
  '\u0085A\u0085',
  'x'.repeat(127) + '😀',
  'x'.repeat(129),
  '\ud800',
  '\udfff',
  '😀'.repeat(65),
].map((value) => ({
  input: JSON.stringify(value),
  expected: JSON.stringify(displayName(value)) ?? null,
}))
const avatars = [
  '',
  '0',
  '01',
  '-1',
  '1',
  '12345',
  '9'.repeat(20),
  '9'.repeat(21),
  '1\n',
  '1.0',
  '１２',
].flatMap((id) =>
  [false, true].map((group) => ({ id, group, expected: qqAvatar(id, group) ?? null })),
)
const rawAvatars = [
  null,
  false,
  true,
  0,
  1,
  12345,
  [],
  [12345],
  [1, 2],
  {},
  { toString: 1 },
].flatMap((value) =>
  [false, true].map((group) => {
    let expected = null,
      error = null
    try {
      expected = qqAvatar(value, group) ?? null
    } catch (e) {
      error = e.message
    }
    return { input: JSON.stringify(value), group, expected, error }
  }),
)
const source = Object.fromEntries(
  [
    'src/runtime.ts',
    'src/store.ts',
    'src/memory/storage/event-revisions.ts',
    'src/plugins/builtin/snowluma/profiles.ts',
  ].map((path) => [path, createHash('sha256').update(readFileSync(path)).digest('hex')]),
)
const path = 'backend/internal/sourceevents/testdata/ingress-routing.json'
const raw =
  JSON.stringify(
    { node: process.version, source, cases, editCases, profileNames, avatars, rawAvatars },
    null,
    2,
  ) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(`Verified ${cases.length} actual ingress routing/identity traces`)
