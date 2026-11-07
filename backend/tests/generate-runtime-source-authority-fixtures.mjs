/** Actual Runtime/Store source-authority oracle. Synthetic data only. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { mock } from 'node:test'
import { Runtime } from '../../src/runtime.ts'
import { Store } from '../../src/store.ts'
import { SourceEventStore } from '../../src/memory/storage/event-revisions.ts'
import {
  sourceBodyHash,
  sourceIdentity,
  sourceMediaReference,
  currentSourceSnapshot,
} from '../../src/runtime/source-events.ts'
import {
  validateSourceSnapshots,
  validateUnrecalledDependencies,
  projectDisplayMessages,
} from '../../src/memory/recovery/ledger.ts'
import { promptWindowAuthority } from '../../src/runtime/prompt-context-window.ts'
assert.equal(process.version, 'v24.22.1')
mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00.000Z') })
const scene = {
  id: 'scene',
  adapterId: 'synthetic',
  accountId: 'account',
  platformSceneId: 'group',
  kind: 'group',
  name: 'Fixture',
  participationEnabled: false,
  learningEnabled: false,
  proactiveEnabled: false,
}
const message = (id = 'message', text = 'A', extra = {}) => ({
  id,
  sceneId: scene.id,
  senderId: 'speaker',
  origin: 'live',
  status: 'available',
  sentAt: '2026-10-28T00:00:00.000Z',
  receivedAt: '2026-10-28T00:00:00.000Z',
  parts: [{ type: 'text', text }],
  ...extra,
})
const edit = (sequence) => ({
  editId: `edit-${sequence}`,
  order: { kind: 'platform_sequence', sequence },
})
const ref = { eventId: 'forged', revision: 99, deletionEpoch: 0 }
const cases = []
function run(name, enabled, ops, options = {}) {
  const store = new Store(':memory:')
  store.set('settings', { ...store.settings, sourceEventRevisionsEnabled: enabled })
  store.saveScene(scene)
  let serial = 0
  store.sourceEventStore = new SourceEventStore(store.db, {
    id: () => `event-${++serial}`,
    now: () => '2026-10-28T00:00:00.000Z',
    ...options,
  })
  const runtime = new Runtime(store, () => {
    throw Error('unexpected-model')
  })
  const timeline = [],
    captures = [],
    steps = []
  runtime.addExtension({
    received: (m) => timeline.push(['received', m.id]),
    edited: (before, after) => timeline.push(['edited', before?.id ?? null, after.id]),
    prepareRecall: (s, id) => {
      timeline.push(['prepare', store.recalled(s, id)])
      return () => timeline.push(['prepared', store.recalled(s, id)])
    },
  })
  const initial = {
    settings: store.db.prepare("SELECT value FROM config WHERE key='settings'").get().value,
    scene: store.db.prepare('SELECT value FROM scenes WHERE id=?').get(scene.id).value,
  }
  for (const op of ops) {
    let result = null,
      error
    try {
      if (op[0] === 'ingest') runtime.ingest(op[1], scene, false)
      else if (op[0] === 'edit') result = runtime.edit(op[1], scene, op[2], false)
      else if (op[0] === 'capture')
        captures.push(
          structuredClone(runtime.states.get(scene.id).messages.find((m) => m.id === op[1])),
        )
      else if (op[0] === 'flag')
        store.set('settings', { ...store.settings, sourceEventRevisionsEnabled: op[1] })
      else if (op[0] === 'recall') runtime.recall(scene.id, op[1])
    } catch (e) {
      error = e.message
    }
    const messages = structuredClone(runtime.states.get(scene.id)?.messages ?? [])
    steps.push({
      op: JSON.stringify(op),
      result: JSON.stringify(result),
      ...(error ? { error } : {}),
      messages: JSON.stringify(messages),
      captures: captures.map((m) => currentSourceSnapshot(store, m)),
      valid: validateSourceSnapshots(store, messages),
      unrecalled: validateUnrecalledDependencies(store, scene.id, ['message']),
      display: JSON.stringify(projectDisplayMessages(store, scene.id, messages, [])),
      timeline: JSON.stringify(timeline.splice(0)),
    })
  }
  cases.push({ name, enabled, options, initial, steps })
  runtime.dispose()
  store.close()
}
run('flag-off-forgery', false, [
  [
    'ingest',
    message('message', 'A', { sourceEventRef: ref, sourceEventRegistrationLimited: true }),
  ],
  ['capture', 'message'],
  ['flag', true],
  ['ingest', message('second')],
  ['capture', 'second'],
  ['recall', 'message'],
])
run('ordered-edits-and-captured-revisions', true, [
  ['ingest', message()],
  ['capture', 'message'],
  ['flag', false],
  ['edit', message('message', 'B'), edit(1)],
  ['capture', 'message'],
  ['edit', message(), edit(2)],
  ['capture', 'message'],
  ['edit', message('message', 'stale'), edit(1)],
  ['recall', 'message'],
  ['ingest', message()],
])
run(
  'capacity-preserves-unbound-body',
  true,
  [
    ['ingest', message()],
    ['ingest', message('second', 'B', { sourceEventRef: ref })],
    ['capture', 'second'],
  ],
  { maxHeadsTotal: 1 },
)
run('edit-admission', false, [
  ['edit', message(), edit(1)],
  ['ingest', message()],
  ['edit', message('message', 'B', { origin: 'history' }), edit(1)],
  ['edit', message('message', 'B', { senderId: 'bot' }), edit(1)],
  ['edit', message('message', 'B', { status: 'missing' }), edit(1)],
])
const bodies = [
  message(),
  message('x', '\ud800', { replyToId: '\udfff' }),
  message('x', '😀', { replyToId: null }),
  message('x', '', {
    parts: [{ type: 'media', reference: 'qualified', mediaType: 'image', z: 1, a: -0 }],
  }),
].map((m) => ({
  message: JSON.stringify(m),
  hash: sourceBodyHash(m),
  identity: JSON.stringify(sourceIdentity(scene, m)),
  media: sourceMediaReference('media\ud800', { eventId: 'event-1', revision: 2, deletionEpoch: 0 }),
}))
const displayStore = new Store(':memory:')
const onebot = {
  ...scene,
  id: `ob:s:${Buffer.from(JSON.stringify([scene.adapterId, scene.accountId, scene.kind, scene.platformSceneId])).toString('base64url')}`,
}
displayStore.saveScene(onebot)
displayStore.set(`identity:${onebot.id}:speaker`, {
  senderDisplayName: 'Saved',
  senderPlatformId: '12345',
})
const displayMessages = [
  message('first', 'A', { sceneId: onebot.id }),
  message('second', 'B', {
    sceneId: onebot.id,
    senderDisplayName: '',
    senderPlatformId: 'invalid',
    senderAvatarUrl: 'untrusted',
  }),
  message('third', 'C', { sceneId: onebot.id, origin: 'simulation' }),
]
const display = {
  scene: JSON.stringify(onebot),
  identity: JSON.stringify(displayStore.get(`identity:${onebot.id}:speaker`, {})),
  messages: JSON.stringify(displayMessages),
  expected: JSON.stringify(
    projectDisplayMessages(displayStore, onebot.id, displayMessages, ['first']),
  ),
  ordinary: JSON.stringify(projectDisplayMessages(displayStore, onebot.id, displayMessages, [])),
  prompt: promptWindowAuthority(displayStore, onebot.id, [
    { name: 'tool', schema: { b: 2, a: 1 } },
  ]),
}
const malformedRows = []
const capture = (fn) => {
  try {
    return { value: fn() }
  } catch (error) {
    return { error: error.message }
  }
}
for (const value of [null, false, true, 0, 1, '', 'row', [], {}, { participationEnabled: 'yes' }]) {
  const raw = JSON.stringify(value)
  displayStore.db.prepare('UPDATE scenes SET value=? WHERE id=?').run(raw, onebot.id)
  const input = message('probe', 'A', { sceneId: onebot.id })
  malformedRows.push({
    kind: 'scene',
    raw,
    message: JSON.stringify(input),
    participation: capture(() => !!displayStore.scene(onebot.id)?.participationEnabled),
    current: capture(() => currentSourceSnapshot(displayStore, input)),
    display: capture(() =>
      JSON.stringify(projectDisplayMessages(displayStore, onebot.id, [input], [])),
    ),
  })
}
displayStore.db
  .prepare('UPDATE scenes SET value=? WHERE id=?')
  .run(JSON.stringify(onebot), onebot.id)
for (const value of [
  null,
  false,
  0,
  1,
  'row',
  [],
  {},
  { senderPlatformId: 123 },
  { senderPlatformId: ['123'] },
  { senderPlatformId: null, senderDisplayName: null },
]) {
  const raw = JSON.stringify(value)
  displayStore.db
    .prepare('UPDATE config SET value=? WHERE key=?')
    .run(raw, `identity:${onebot.id}:speaker`)
  for (const explicit of [false, true]) {
    const input = message('probe', 'A', {
      sceneId: onebot.id,
      ...(explicit ? { senderPlatformId: '123', senderDisplayName: 'Explicit' } : {}),
    })
    malformedRows.push({
      kind: 'identity',
      raw,
      message: JSON.stringify(input),
      display: capture(() =>
        JSON.stringify(projectDisplayMessages(displayStore, onebot.id, [input], [])),
      ),
    })
  }
}
displayStore.close()
const files = [
  'src/runtime.ts',
  'src/runtime/source-events.ts',
  'src/store.ts',
  'src/memory/recovery/ledger.ts',
  'src/runtime/prompt-context-window.ts',
  'src/plugins/builtin/snowluma/profiles.ts',
]
const fixture = {
  node: process.version,
  source: Object.fromEntries(
    files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  scene: JSON.stringify(scene),
  cases,
  bodies,
  display,
  malformedRows,
}
const path = 'backend/internal/sourceevents/testdata/runtime-authority.json',
  raw = JSON.stringify(fixture, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else writeFileSync(path, raw)
console.log(
  `Verified ${cases.length} actual Runtime source traces and exact helper/display/prompt outputs`,
)
