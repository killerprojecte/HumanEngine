import fs from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const scene = {
  id: 'fixture', name: 'Fixture', kind: 'group', adapterId: 'fixture', accountId: 'bot',
  platformSceneId: 'group', participationEnabled: false, learningEnabled: false, proactiveEnabled: false,
}
const message = {
  sceneId: scene.id, senderId: 'person', origin: 'live', status: 'available',
  receivedAt: '2099-01-01T00:00:00.000Z', sentAt: '2099-01-01T00:00:00.000Z',
}
const messages = [
  { ...message, id: 'text', parts: [{ type: 'text', text: 'Synthetic message' }, { type: 'mention', userId: 'other' }] },
  { ...message, id: 'media', replyToId: 'text', parts: [
    { type: 'media', mediaType: 'image', assetId: 'private-reference' },
    { type: 'unsupported', description: 'Untrusted platform image metadata: {"kind":"mface"}' },
    { type: 'unsupported', description: 'Untrusted platform image metadata: {"kind":"unknown"}' },
    { type: 'media', mediaType: 'audio', assetId: 'private-audio-reference' },
  ] },
  { ...message, id: 'recall', parts: [{ type: 'text', text: 'Recalled source' }] },
  { ...message, id: 'expired', receivedAt: '2000-01-01T00:00:00.000Z', parts: [{ type: 'text', text: 'Expired source' }] },
]
const api = createApp(store, { engine, token: 'synthetic-scene-messages' })
try {
  store.saveScene(scene)
  for (const row of messages) engine.runtime.ingest(row, scene, false)
  engine.runtime.recall(scene.id, 'recall')
  const cases = []
  for (const [method, path, authorized] of [
    ['GET', '/api/scenes/fixture/messages', true],
    ['GET', '/api/scenes/missing/messages', true],
    ['GET', '/api/scenes/fixture/messages', false],
    ['HEAD', '/api/scenes/fixture/messages', true],
    ['HEAD', '/api/scenes/fixture/messages', false],
  ]) {
    const request = new Request(`http://fixture.invalid${path}`, {
      method, headers: { Authorization: authorized ? 'Bearer synthetic-scene-messages' : 'Bearer synthetic-invalid-bearer' },
    })
    const response = await api.request(request)
    const body = method === 'HEAD' ? await response.text() : await response.json()
    cases.push({ method, path, authorized, status: response.status, body })
  }
  const output = JSON.stringify({ scene, messages, recall: 'recall', cases }, null, 2)+'\n'
  const target = new URL('../internal/app/testdata/scene-messages-source.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (fs.readFileSync(target, 'utf8') !== output) process.exitCode = 1
  } else fs.writeFileSync(target, output)
} finally {
  await engine.dispose()
  store.close()
}
