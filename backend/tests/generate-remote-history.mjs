import assert from 'node:assert/strict'
import fs from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const scene = {
  id: 'fixture', name: 'Fixture', avatarUrl: 'https://synthetic.invalid/avatar',
  kind: 'group', adapterId: 'fixture', accountId: 'bot',
  platformSceneId: 'group', participationEnabled: false, learningEnabled: false, proactiveEnabled: false,
}
const message = (id, text, sentAt, origin = 'live') => ({
  id, sceneId: scene.id, senderId: 'person', origin, status: 'available',
  receivedAt: '2099-01-01T00:00:00.000Z', sentAt,
  parts: [{ type: 'text', text }],
})
const timestamp = (second) => new Date(Date.UTC(2099, 0, 1, 0, 0, second)).toISOString()
const retained = [
  message('local-first', 'Fresh local body', timestamp(15)),
  message('pending-recall', 'Retained sensitive synthetic text', timestamp(1)),
]
const live = message('live-arrival', 'New local arrival', timestamp(9))
const recall = 'pending-recall'
const bot = 'platform-bot'
const remote = [
  ...Array.from({ length: 30 }, (_, i) =>
    message(`remote-${i}`, `Remote ${i}`, timestamp(i), 'history'),
  ),
  message('locale-later', 'Noncanonical date string', 'Dec 31 2098', 'history'),
  message('locale-earlier', 'Noncanonical date string', 'dec 30 2098', 'history'),
  { ...retained[0], parts: [{ type: 'text', text: 'Stale duplicate' }], origin: 'history' },
  { ...retained[1], origin: 'history' },
  { ...message('foreign', 'Foreign synthetic', timestamp(33), 'history'), sceneId: 'other' },
  {
    ...message('bot-row', 'Synthetic platform bot', timestamp(15), 'history'),
    senderId: bot, parts: [{ type: 'mention', userId: bot }],
  },
]
const success = { items: remote, coverage: 'complete', nextCursor: 'synthetic-cursor' }
const invalidTimestamp = { items: [{ ...remote.at(-1), sentAt: 1 }], coverage: 'complete' }
const timeout = { items: remote.slice(-1), coverage: 'unavailable', reason: 'request_timeout' }
const api = createApp(store, { engine, token: 'synthetic-history' })
const path = '/api/scenes/fixture/history'
const cases = []
let pending
async function capture(mode, method, url, mutate) {
  const result = api.request(`http://fixture.invalid${url}`, {
    method, headers: { Authorization: 'Bearer synthetic-history' },
  })
  const platform = { success, timeout, 'invalid-timestamp': invalidTimestamp }[mode]
  if (platform) {
    await new Promise((resolve) => setImmediate(resolve))
    assert.ok(pending)
    mutate?.()
    const settle = pending
    pending = undefined
    settle(platform)
  }
  const response = await result
  const body = method === 'HEAD' ? await response.text() : await response.json()
  if (mode === 'invalid-timestamp') delete body.requestId
  cases.push({ mode, method, path: url, status: response.status, body })
}
try {
  store.saveScene(scene)
  for (const row of retained) engine.runtime.ingest(row, { ...scene, displayName: scene.name }, false)
  engine.runtime.addAdapter('fixture', {
    send: async () => ({ status: 'sent' }),
    getMessage: async () => ({ items: [], coverage: 'unavailable' }),
    history: async (scope, { limit }, signal) => {
      assert.equal(scope.displayName, scene.name)
      assert.equal(scope.avatarUrl, scene.avatarUrl)
      assert.equal(limit, 30)
      assert.ok(signal)
      return await new Promise((resolve) => { pending = resolve })
    },
    dispose() {},
  }, bot)
  await capture('success', 'GET', path, () => {
    engine.runtime.recall(scene.id, recall)
    engine.runtime.ingest(live, { ...scene, displayName: scene.name }, false)
  })
  await capture('timeout', 'GET', path + '?source=remote')
  await capture('invalid-timestamp', 'GET', path + '?source=remote')
  engine.runtime.removeAdapter('fixture')
  await capture('no-adapter', 'GET', path)
  await capture('missing-scene', 'GET', '/api/scenes/missing/history')
  await capture('head', 'HEAD', path)
  const { items } = cases[0].body
  assert.equal(items.length, 31)
  assert.ok(items.some((m) => m.id === 'live-arrival'))
  assert.ok(!items.some((m) => ['pending-recall', 'foreign', 'remote-0', 'remote-3'].includes(m.id)))
  assert.deepEqual(items.slice(-2).map((m) => m.id), ['locale-earlier', 'locale-later'])
  assert.equal(cases[2].status, 503)
  assert.equal(items.find((m) => m.id === 'local-first').parts[0].text, 'Fresh local body')
  assert.equal(items.find((m) => m.id === 'bot-row').parts[0].userId, 'bot')
  const output = JSON.stringify({ scene, retained, live, recall, bot, success, timeout, invalidTimestamp, cases }) + '\n'
  const target = new URL('../internal/app/testdata/remote-history-source.json', import.meta.url)
  if (process.argv.includes('--check')) {
    assert.equal(fs.readFileSync(target, 'utf8'), output)
  } else {
    fs.writeFileSync(target, output)
  }
} finally {
  if (pending) pending(timeout)
  await engine.dispose()
  store.close()
}
