import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const scene = {
  id: 'scene-a', adapterId: 'fixture', accountId: 'bot', kind: 'group', platformSceneId: 'group',
  name: 'Synthetic', participationEnabled: false, learningEnabled: false, proactiveEnabled: false,
}
const source = {
  id: 'source', sceneId: scene.id, senderId: 'synthetic-person', receivedAt: '2099-01-02T00:00:00Z',
  sentAt: '2099-01-02T00:00:00Z', origin: 'live', status: 'available', parts: [{ type: 'text', text: 'Synthetic' }],
}
const seeds = {
  ordinary: { scope: { kind: 'scene', sceneId: scene.id, personId: 'synthetic-person' },
    content: 'original text', kind: 'fact', validFrom: '2024-01-01T00:00:00Z' },
  recalled: { scope: { kind: 'scene', sceneId: scene.id }, content: 'sourced text',
    kind: 'fact', sources: [{ sceneId: scene.id, messageId: source.id, senderId: source.senderId }] },
}
const indexed = []
const index = engine.indexMemory.bind(engine)
engine.indexMemory = (id, content) => {
  indexed.push({ id, content })
  return index(id, content)
}
const guard = (key, version = 1, epoch = 0) => ({
  idempotencyKey: key, expectedVersion: version, expectedDeletionEpoch: epoch,
})
const check = []
try {
  store.saveScene(scene)
  engine.runtime.ingest(source, scene, false)
  const ids = Object.fromEntries(Object.entries(seeds).map(([key, seed]) => [key, engine.memory.create(seed).id]))
  ids.missing = 'synthetic-missing'
  for (const [name, target, input, options = {}] of [
    ['guarded', 'ordinary', { status: 'disputed', guard: guard('ordinary-1'), locked: true, content: '  edited text  ' }],
    ['exact-replay-reordered', 'ordinary', { locked: true, content: '  edited text  ', status: 'disputed', guard: guard('ordinary-1') }],
    ['receipt-conflict', 'ordinary', { content: 'changed payload', guard: guard('ordinary-1') }],
    ['stale-version', 'ordinary', { locked: false, guard: guard('stale') }],
    ['wrong-epoch', 'ordinary', { locked: false, guard: guard('epoch', 2, 1) }],
    ['lock-only', 'ordinary', { locked: false }],
    ['superseded-replay', 'ordinary', { status: 'disputed', guard: guard('ordinary-1'), locked: true, content: '  edited text  ' }],
    ['source-backed', 'recalled', { locked: true, guard: guard('source-first') }],
    ['source-replay-before-recall', 'recalled', { locked: true, guard: guard('source-first') }],
    ['source-recalled', 'recalled', { locked: true, guard: guard('source-first') }, { recall: true }],
    ['schema-scope', 'ordinary', { scope: { kind: 'sharedPublicKnowledge' } }],
    ['schema-time', 'ordinary', { validFrom: '2026-01-01T00:00:00Z' }],
    ['bad-guard', 'ordinary', { guard: guard('invalid', Number.MAX_SAFE_INTEGER + 1) }],
    ['missing', 'missing', { locked: true }],
    ['unauthorized', 'ordinary', { locked: true }, { authorized: false }],
    ['whitespace-preserved', 'ordinary', { content: '   ' }],
    ['empty-content', 'ordinary', { content: '' }],
    ['malformed', 'ordinary', '{'],
    ['status-invalidated', 'ordinary', { status: 'invalidated' }],
    ['astral-content-within-both-limits', 'ordinary', { content: '😀'.repeat(5001) }],
    ['astral-content-over-route-limit', 'ordinary', { content: '😀'.repeat(10001) }],
    ['astral-guard-over-domain-limit', 'ordinary', { guard: guard('😀'.repeat(81), 4) }],
    ['astral-guard-over-route-limit', 'ordinary', { guard: guard('😀'.repeat(161), 4) }],

  ]) {
    if (options.recall) engine.runtime.recall(scene.id, source.id)
    const raw = typeof input === 'string' ? input : JSON.stringify(input)
    const response = await app.request(`http://fixture.invalid/api/memories/${encodeURIComponent(ids[target])}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json',
        ...(options.authorized !== false ? { Authorization: `Bearer ${token}` } : {}) }, body: raw,
    })
    const value = await response.json()
    delete value.requestId
    if (response.ok) {
      assert.equal(ids[target], value.id)
      assert.deepEqual(indexed.at(-1), { id: value.id, content: value.content })
      delete value.id
      delete value.learnedAt
    }
    check.push({ name, target, raw, ...options, status: response.status, body: value })
  }
  assert.equal(check.filter((entry) => entry.status === 200).length, 8)
  assert.equal(indexed.length, 8, 'Source replays successfully acknowledged requests into the same indexer')
  const initialSourceEdit = check.find((entry) => entry.name === 'source-backed')
  const replayedSourceEdit = check.find((entry) => entry.name === 'source-replay-before-recall')
  assert.equal(replayedSourceEdit.status, 200)
  assert.deepEqual(replayedSourceEdit.body, initialSourceEdit.body)
  assert.equal(check.find((entry) => entry.name === 'source-recalled').status, 409)
  assert.deepEqual(check.filter((entry) => entry.name.startsWith('astral-')).map((entry) => entry.status), [200, 400, 503, 400])

  const target = new URL('../internal/app/testdata/memory-patch-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, scene, source, seeds, cases: check }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
} finally {
  await engine.dispose()
  store.close()
}
