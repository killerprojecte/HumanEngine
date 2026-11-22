// A disposable loopback OneBot fixture for the cross-engine recall rehearsal.
// No application data, credentials, or diagnostics are printed.
const assert = require('node:assert/strict')
const { createHmac } = require('node:crypto')
const { readFileSync } = require('node:fs')
const { createServer } = require('node:http')
const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url')
const scene = 'ob:s:' + encode(['snowluma', '42', 'group', '900'])
const ids = [101, 102].map(id => scene + ':m:' + encode(String(id)))
const messages = timestamp => [101, 102].map(id => ({
  post_type: 'message', message_type: 'group', sub_type: 'normal', self_id: 42,
  group_id: 900, user_id: 7, message_id: id, time: timestamp,
  message: [{ type: 'text', data: { text: 'Synthetic recall source ' + id } }],
  sender: { user_id: 7, nickname: 'Synthetic sender' },
}))
const mock = ({ actionPort, token, timestamp }) => createServer((req, res) => {
  req.resume()
  if (req.method === 'GET' && req.url === '/ready') {
    return res.writeHead(204).end()
  }
  const data = {
    '/get_login_info': { user_id: 42 },
    '/get_group_info': { group_id: 900, group_name: 'Synthetic recall group' },
    '/get_group_msg_history': { messages: messages(timestamp) },
  }[req.url]
  if (req.method !== 'POST' || req.headers.authorization !== 'Bearer ' + token || !data) {
    return res.writeHead(400).end()
  }
  res.writeHead(200, { 'Content-Type': 'application/json' })
    .end(JSON.stringify({ status: 'ok', retcode: 0, data }))
}).listen(actionPort, '127.0.0.1')
async function probe({ phase, eventPort, eventToken, timestamp }) {
  const send = async event => {
    const body = JSON.stringify(event)
    const signature = createHmac('sha1', eventToken).update(body).digest('hex')
    const response = await fetch('http://127.0.0.1:' + eventPort + '/onebot/events', {
      method: 'POST', body, signal: AbortSignal.timeout(5000),
      headers: { 'Content-Type': 'application/json', 'X-Self-ID': '42', 'X-Signature': 'sha1=' + signature },
    })
    assert.equal(response.status, 204)
  }
  if (phase === 'seed') for (const message of messages(timestamp)) await send(message)
  if (phase === 'recall') await send({
    post_type: 'notice', notice_type: 'group_recall', self_id: 42, group_id: 900,
    user_id: 7, operator_id: 7, message_id: 101, time: timestamp,
  })
  const deadline = Date.now() + 10000
  const url = 'http://127.0.0.1:' + process.argv[1] + '/api/scenes/' + scene
  while (Date.now() < deadline) {
    const response = await fetch(url + '/history?source=' + (phase === 'seed' ? 'local' : 'remote'), {
      headers: { Authorization: 'Bearer ' + process.env.HUMANENGINE_ADMIN_TOKEN },
      signal: AbortSignal.timeout(5000),
    })
    if (response.ok) {
      const { items } = await response.json()
      const visible = items.filter(row => row.status === 'available').map(row => row.id).sort()
      const expected = (phase === 'seed' || phase === 'before' ? ids : ids.slice(1)).sort()
      if (JSON.stringify(visible) === JSON.stringify(expected) &&
          (expected.length === 2 || !items.some(row => row.id === ids[0]))) {
        console.log(JSON.stringify({ scene, ids }))
        return
      }
    }
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  throw Error('synthetic recall history unavailable')
}
if (process.argv[1] === 'mock') mock(JSON.parse(process.env.HE_RECALL_MOCK))
else probe(JSON.parse(readFileSync(0, 'utf8'))).catch(() => { process.exitCode = 1 })
