import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createChatMediaFixture } from '../../tests/fixtures/chat-media.ts'

assert.equal(process.version, 'v24.22.1')
const f = await createChatMediaFixture()
try {
  const headers = { Authorization: 'Bearer ' + f.token }
  const base = '/api/scenes/' + encodeURIComponent(f.scene.id)
  const routes = {
    history: base + '/history',
    local: base + '/history?source=local',
    media: base + '/messages/' + encodeURIComponent(f.adapter.messageId(f.scene, '90')) + '/media/1',
  }
  const record = {
    post_type: 'message', self_id: 42, message_type: 'group', group_id: 9,
    user_id: 7, message_id: 90, time: 4070908800,
    message: [{ type: 'text', data: { text: 'prefix' } }, { type: 'image', data: { base64: f.png.toString('base64') } }],
  }
  f.events.clear()
  f.events.set(90, record)
  const calls = () => f.requests.filter(x => ['get_group_msg_history','get_msg'].includes(x.action) || x.action === 'get_image' && !['picture','animation','expired','corrupt'].includes(x.params.file))
  const select = () => f.engine.runtime.preview(f.scene.id).some(row => row.id === f.adapter.messageId(f.scene,'90'))
  const before = calls().length
  const local = await f.app.request(routes.local, { headers })
  assert.equal((await local.json()).items.some(row => row.id === f.adapter.messageId(f.scene,'90')), false)
  assert.equal(calls().length, before)
  const history = await f.app.request(routes.history, { headers })
  const row = (await history.json()).items.find(row => row.id === f.adapter.messageId(f.scene,'90'))
  assert.equal(row.parts[1].mediaUrl, routes.media)
  assert.doesNotMatch(JSON.stringify(row), /assetId|ob:media|base64:/)
  const historyCalls = calls().slice(before)
  assert.deepEqual(historyCalls.map(x => x.action), ['get_group_msg_history'])
  const captures = []
  async function capture(name, changes, want, authorize = true) {
    if (changes) f.events.set(90, { ...record, ...changes })
    const initial = calls().length
    const res = await f.app.request(routes.media, { headers: authorize ? headers : { Authorization: 'Bearer fixture-wrong' } })
    assert.equal(res.status, want, name)
    const actions = calls().slice(initial).map(({ action, params }) => ({ action, params }))
    assert.equal(select(), false, 'remote read became retained runtime input')
    if (want === 200) assert.deepEqual(Buffer.from(await res.arrayBuffer()), f.png)
    captures.push({ name, status: res.status, actions })
  }
  await capture('unauthenticated', null, 401, false)
  await capture('exact-message', null, 200)
  await capture('wrong-id', { message_id: 91 }, 404)
  await capture('wrong-scene', { group_id: 88 }, 404)
  f.engine.runtime.recall(f.scene.id, f.adapter.messageId(f.scene, '90'))
  await capture('recalled-before-lookup', {}, 404)
  assert.deepEqual(captures[1].actions.map(a=>a.action), ['get_msg'])
  assert.deepEqual(captures.at(-1).actions, [])
  const file = new URL('../internal/app/testdata/snowluma-remote-browser-image-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, history: row.parts, captures }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(file,'utf8'),output)
  else writeFileSync(file,output)
  console.log('PASS source exact-message remote image and authority cases')
} finally {
  await f.close()
}
