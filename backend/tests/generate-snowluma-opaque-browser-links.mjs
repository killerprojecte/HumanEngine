import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createChatMediaFixture } from '../../tests/fixtures/chat-media.ts'

assert.equal(process.version,'v24.22.1')
const f = await createChatMediaFixture()
try {
  const headers = { Authorization: 'Bearer ' + f.token }
  const base = '/api/scenes/' + encodeURIComponent(f.scene.id)
  const byId = n => f.adapter.messageId(f.scene,String(n))
  const route = n => base + '/messages/' + encodeURIComponent(byId(n)) + '/media/1'
  const remote = { post_type: 'message', self_id: 42, message_type: 'group', group_id: 9,
    user_id: 7, message_id: 95, time: 4070908800, message: [
      { type: 'text', data: { text: 'remote prefix' } },
      { type: 'mface', data: { file: 'synthetic-file', file_id: 'opaque-browser-remote' } },
    ] }
  f.events.clear()
  f.events.set(95,remote)
  const tracked = () => f.requests.filter(({action,params}) =>
    action === 'get_msg' && Number(params.message_id) === 95 ||
    action === 'get_image' && ['synthetic-file','opaque-browser-remote'].includes(params.file))
  const entries = []
  for (const surface of ['/messages','/history?source=local','/history']) {
    const response = await f.app.request(base + surface,{headers})
    assert.equal(response.status,200)
    const body = await response.json()
    const rows = Array.isArray(body) ? body : body.items
    const id = surface === '/history' ? 95 : 1
    const row = rows.find(row => row.id === byId(id))
    assert.ok(row)
    const part = row.parts.find(part => part.mediaUrl)
    assert.equal(part.mediaUrl,route(id))
    assert.equal(part.mediaKind,id === 95 ? 'sticker' : 'image')
    assert.doesNotMatch(JSON.stringify(row.parts.filter(part=>part.type==='media')),/assetId|ob:media:|synthetic-file|opaque-browser-remote/u)
    assert.deepEqual(tracked(),[])
    entries.push({surface,id,part})
  }
  const image = await f.app.request(route(95),{headers})
  assert.equal(image.status,200)
  assert.deepEqual(Buffer.from(await image.arrayBuffer()),f.png)
  const actions = tracked().map(({action,params})=>({action,params}))
  assert.deepEqual(actions.map(({action})=>action),['get_msg','get_image'])
  assert.equal(actions[1].params.file,'opaque-browser-remote')
  assert.equal(f.engine.runtime.preview(f.scene.id).some(row=>row.id === byId(95)),false)
  const file = new URL('../internal/app/testdata/snowluma-opaque-browser-links-source.json',import.meta.url)
  const output = JSON.stringify({entries,actions},null,2)+'\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(file,'utf8'),output)
  else writeFileSync(file,output)
  console.log('PASS actual retained and remote opaque browser link source')
} finally {
  await f.close()
}
