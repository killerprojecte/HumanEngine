// Real loopback SnowLuma HTTP action peer and signed synthetic ingress.
const assert = require('node:assert/strict')
const { createHmac } = require('node:crypto')
const { createServer } = require('node:http')
const fixture = JSON.parse(process.env.HE_BROWSER_CHAT)
const { actionPort, eventPort, eventToken, actionToken, timestamp, group, sender, name, local, remote } = fixture
const origin = port => 'http://127.0.0.1:' + port
const message = (id, text, time) => ({
  post_type: 'message', message_type: 'group', sub_type: 'normal', self_id: 42,
  group_id: group, user_id: sender, message_id: id, time,
  message: [{ type: 'text', data: { text } }],
  sender: { user_id: sender, nickname: 'Synthetic sender' },
})
const historyAction = '/get_group_msg_history'

function server() {
  let held
  let released = false
  const json = (res, data) => res.writeHead(200, { 'Content-Type': 'application/json' })
    .end(JSON.stringify({ status: 'ok', retcode: 0, data }))
  return createServer(async (req, res) => {
    if (req.headers.authorization !== 'Bearer ' + actionToken)
      return res.writeHead(401).end()
    if (req.method === 'GET' && req.url === '/fixture/ready') return res.writeHead(204).end()
    if (req.method === 'GET' && req.url === '/fixture/held')
      return res.writeHead(held && !released && !held.destroyed ? 204 : 409).end()
    if (req.method === 'POST' && req.url === '/fixture/release') {
      if (!held || released || held.destroyed) return res.writeHead(409).end()
      released = true
      json(held, { messages: [message(200, remote, timestamp + 1)] })
      return res.writeHead(204).end()
    }
    if (req.method !== 'POST') return res.writeHead(405).end()
    if (req.url === '/get_login_info') {
      req.resume()
      return json(res, { user_id: 42 })
    }
    if (req.url === '/get_group_info') {
      req.resume()
      return json(res, { group_id: group, group_name: name })
    }
    if (req.url === historyAction) {
      const chunks = []
      for await (const chunk of req) chunks.push(chunk)
      let args
      try { args = JSON.parse(Buffer.concat(chunks)) } catch {
        return res.writeHead(400).end()
      }
      if (held || String(args.group_id) !== String(group) || args.count !== 30 || !args.reverse_order)
        return res.writeHead(409).end()
      held = res
      return
    }
    return res.writeHead(404).end()
  }).listen(actionPort, '127.0.0.1')
}

async function probe(mode) {
  if (mode === 'seed') {
    for (let index = 0; index < 32; index++) {
      const body = JSON.stringify(message(100 + index, index === 31 ? local : 'Synthetic row ' + index,
        timestamp - 31 + index))
      const signature = createHmac('sha1', eventToken).update(body).digest('hex')
      const response = await fetch(origin(eventPort) + '/onebot/events', {
        method: 'POST', body, signal: AbortSignal.timeout(5000),
        headers: { 'Content-Type': 'application/json', 'X-Self-ID': '42', 'X-Signature': 'sha1=' + signature },
      })
      assert.equal(response.status, 204)
    }
    return console.log('204')
  }
  assert(['ready', 'held', 'release'].includes(mode))
  const response = await fetch(origin(actionPort) + '/fixture/' + mode, {
    method: mode === 'release' ? 'POST' : 'GET',
    headers: { Authorization: 'Bearer ' + actionToken }, signal: AbortSignal.timeout(5000),
  })
  console.log(response.status)
}

const mode = process.argv[1]
if (mode === 'peer') server()
else probe(mode).catch(() => { process.exitCode = 1 })
