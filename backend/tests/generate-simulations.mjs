import fs from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const api = createApp(store, { engine, token: 'synthetic-simulations' })
const scene = {
  id: 'real', name: 'Synthetic Real', kind: 'group', adapterId: 'fixture', accountId: 'bot',
  platformSceneId: 'group', participationEnabled: false, learningEnabled: false, proactiveEnabled: false,
}
let simulation
try {
  store.saveScene(scene)
  const cases = []
  for (const [method, path, authorized, input] of [
    ['POST','/api/simulations',false],
    ['POST','/api/simulations',true],
    ['GET','/api/simulations/{simulation}',true],
    ['HEAD','/api/simulations/{simulation}',true],
    ['HEAD','/api/simulations/{simulation}',false],
    ['GET','/api/simulations/real',true],
    ['DELETE','/api/simulations/real',true],
    ['GET','/api/simulations/missing',true],
    ['POST','/api/simulations/missing/messages',true,{text:'Synthetic missing'}],
    ['POST','/api/simulations/{simulation}/messages',true,{text:''}],
    ['POST','/api/simulations/{simulation}/messages',true,{text:'short',extra:true}],
    ['POST','/api/simulations/{simulation}/messages',true,{text:42}],
    ['POST','/api/simulations/{simulation}/messages',true],
    ['POST','/api/simulations/{simulation}/messages',true,{text:'😀'.repeat(12001)}],
    ['POST','/api/simulations/{simulation}/messages',true,{text:'😀'.repeat(6001)}],
    ['POST','/api/simulations/{simulation}/messages',true,{text:'Synthetic isolated input'}],
    ['GET','/api/simulations/{simulation}',true],
    ['DELETE','/api/simulations/{simulation}',true],
    ['DELETE','/api/simulations/{simulation}',true],
    ['GET','/api/simulations/{simulation}',true],
  ]) {
    const headers = { Authorization: authorized ? 'Bearer synthetic-simulations' : 'Bearer synthetic-invalid-bearer' }
    if (input !== undefined) headers['Content-Type'] = 'application/json'
    const response = await api.request(new Request(`http://fixture.invalid${path.replace('{simulation}', encodeURIComponent(simulation ?? ''))}`, {
      method, headers, body: input === undefined ? undefined : JSON.stringify(input),
    }))
    const raw = await response.text()
    const output = raw === '' ? null : JSON.parse(raw)
    if (method === 'POST' && path === '/api/simulations' && response.ok) {
      simulation = output.id
      if (!/^sim:[0-9a-f-]{36}$/.test(simulation)) throw Error('invalid simulation ID')
      output.id = '{simulation}'
    }
    if (Array.isArray(output)) for (const row of output) {
      if (!row.id || row.sceneId !== simulation || Number.isNaN(Date.parse(row.sentAt)) || Number.isNaN(Date.parse(row.receivedAt))) throw Error('invalid simulation message')
      row.id = '{message}'
      row.sceneId = '{simulation}'
      row.sentAt = row.receivedAt = '{timestamp}'
      for (const part of row.parts)
        if (part.text === '😀'.repeat(6001)) part.text = '{astral}'
    }
    if (output?.requestId) output.requestId = '{requestId}'
    const fixtureInput = input?.text === '😀'.repeat(12001) ? { text: '{overflow}' }
      : input?.text === '😀'.repeat(6001) ? { text: '{astral}' } : input
    cases.push({ method, path, authorized, input: fixtureInput, status: response.status, cacheControl: response.headers.get('Cache-Control'), body: output })
  }
  const authorized = { Authorization: 'Bearer synthetic-simulations' }
  const created = await api.request('http://fixture.invalid/api/simulations', { method: 'POST', headers: authorized })
  const active = (await created.json()).id
  const updated = await api.request(`http://fixture.invalid/api/scenes/${encodeURIComponent(active)}`, {
    method: 'PUT', headers: { ...authorized, 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...scene, id: active }),
  })
  const transcript = await api.request(`http://fixture.invalid/api/simulations/${encodeURIComponent(active)}`, { headers: authorized })
  if (!updated.ok || !transcript.ok || (await transcript.json()).length !== 0) throw Error('real config replaced active simulation')
  const output = JSON.stringify({ scene, cases })+'\n'
  const target = new URL('../internal/app/testdata/simulations-source.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (fs.readFileSync(target, 'utf8') !== output) process.exitCode = 1
  } else fs.writeFileSync(target, output)
} finally {
  await engine.dispose()
  store.close()
}
