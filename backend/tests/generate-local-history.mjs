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
const messages = Array.from({ length: 40 }, (_, i) => ({
  id: `message-${i}`, sceneId: scene.id, senderId: 'person', origin: 'live',
  status: 'available', receivedAt: '2099-01-01T00:00:00.000Z', sentAt: '2099-01-01T00:00:00.000Z',
  parts: [{type: 'text', text: `Synthetic ${i}` }],
}))
messages.push({
  ...messages[39], id: 'expired', receivedAt: '2000-01-01T00:00:00.000Z',
  sentAt: '2000-01-01T00:00:00.000Z',
})
const api = createApp(store, { engine, token: 'synthetic-local-history' })
try {
  store.saveScene(scene)
  for (const message of messages) engine.runtime.ingest(message, scene, false)
  engine.runtime.recall(scene.id, 'message-39')
  const cases = []
  for (const [method, path, authorized] of [
    ['GET','/api/scenes/fixture/history?source=local',true],
    ['GET','/api/scenes/missing/history?source=local',true],
    ['GET','/api/scenes/fixture/history?source=invalid',true],
    ['GET','/api/scenes/fixture/history?source=local;x=1',true],
    ['GET','/api/scenes/fixture/history?source=%ZZ',true],
    ['GET','/api/scenes/fixture/history?source=',true],
    ['GET','/api/scenes/fixture/history?source=local',false],
  ]) {
    const request = new Request(`http://fixture.invalid${path}`, {
      method, headers: authorized ? { Authorization: 'Bearer synthetic-local-history' } : {},
    })
    const response = await api.request(request)
    cases.push({ method, path, authorized, status: response.status, body: await response.json() })
  }
  const output = JSON.stringify({scene, messages, recall: 'message-39', cases})+'\n'
  const target = new URL('../internal/app/testdata/local-history-source.json',import.meta.url)
  if (process.argv.includes('--check')) {
    if (fs.readFileSync(target,'utf8') !== output) process.exitCode = 1
  } else fs.writeFileSync(target,output)
} finally {
  await engine.dispose()
  store.close()
}
