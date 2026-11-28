// Real loopback SnowLuma HTTP action peer and signed synthetic ingress.
const assert = require('node:assert/strict')
const { createHmac } = require('node:crypto')
const { createServer } = require('node:http')
const fixture = JSON.parse(process.env.HE_BROWSER_CHAT)
const { actionPort, eventPort, eventToken, actionToken, timestamp, group, sender, name, card, local, remote, imageURL } = fixture
const origin = port => 'http://127.0.0.1:' + port
const message = (id, text, time) => ({
  post_type: 'message', message_type: 'group', sub_type: 'normal', self_id: 42,
  group_id: group, user_id: sender, message_id: id, time,
  message: [{ type: 'text', data: { text } }],
  sender: { user_id: sender, nickname: 'Synthetic sender' },
})
const historyAction = '/get_group_msg_history'
const stickerFile = 'synthetic-sticker-200'
const stickerPNG = 'iVBORw0KGgoAAAANSUhEUgAAAAMAAAACCAIAAAASFvFNAAAAEElEQVR4nGPQ3LQAghjgLABWHgjjm9uLHwAAAABJRU5ErkJggg=='
const remoteMessage = () => {
  const row = message(200, remote, timestamp + 1)
  if (imageURL) row.message.push({ type: 'mface', data: { url: imageURL, file_id: stickerFile } })
  return row
}

function cdn() {
  const url = new URL(imageURL)
  let reads = 0
  createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/fixture/ready') return res.writeHead(204).end()
    if (req.method === 'GET' && req.url === '/fixture/media') return res.writeHead(reads === 1 ? 204 : 409).end()
    if (req.method === 'GET' && req.url === url.pathname && Object.keys(req.headers).length === 1 &&
        req.headers.host === url.host) {
      reads++
      return res.writeHead(403).end()
    }
    return res.writeHead(400).end()
  }).listen(Number(url.port), url.hostname)
}

async function body(req) {
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  return JSON.parse(Buffer.concat(chunks))
}

function server() {
  let held
  let released = false
  let messages = 0, images = 0
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
      json(held, { messages: [remoteMessage()] })
      return res.writeHead(204).end()
    }
    if (req.method === 'GET' && req.url === '/fixture/media')
      return res.writeHead(messages === 1 && images === 1 ? 204 : 409).end()
    if (req.method !== 'POST') return res.writeHead(405).end()
    if (req.url === '/get_login_info') {
      req.resume()
      return json(res, { user_id: 42 })
    }
    if (req.url === '/get_group_info') {
      req.resume()
      return json(res, { group_id: group, group_name: name })
    }
    if (req.url === historyAction || imageURL && ['/get_msg', '/get_image'].includes(req.url)) {
      let args
      try { args = await body(req) } catch { return res.writeHead(400).end() }
      if (req.url === '/get_msg') {
        messages++
        return String(args.message_id) === '200'
          ? json(res, remoteMessage()) : res.writeHead(409).end()
      }
      if (req.url === '/get_image') {
        images++
        return args.file === stickerFile
          ? json(res, { file: 'base64://' + stickerPNG }) : res.writeHead(409).end()
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
      const row = message(100 + index, index === 31 ? local : 'Synthetic row ' + index, timestamp - 31 + index)
      if (index === 0 && card) {
        row.group_name = name
        row.sender.card = card
      } else if (card) {
        delete row.sender
      }
      const body = JSON.stringify(row)
      const signature = createHmac('sha1', eventToken).update(body).digest('hex')
      const response = await fetch(origin(eventPort) + '/onebot/events', {
        method: 'POST', body, signal: AbortSignal.timeout(5000),
        headers: { 'Content-Type': 'application/json', 'X-Self-ID': '42', 'X-Signature': 'sha1=' + signature },
      })
      assert.equal(response.status, 204)
    }
    return console.log('204')
  }
  if (['cdn-ready', 'cdn-media'].includes(mode)) {
    const response = await fetch(new URL('/fixture/' + mode.slice(4), imageURL), { signal: AbortSignal.timeout(5000) })
    return console.log(response.status)
  }
  assert(['ready', 'held', 'release', 'media'].includes(mode))
  const response = await fetch(origin(actionPort) + '/fixture/' + mode, {
    method: mode === 'release' ? 'POST' : 'GET',
    headers: { Authorization: 'Bearer ' + actionToken }, signal: AbortSignal.timeout(5000),
  })
  console.log(response.status)
}

const mode = process.argv[1]
if (mode === 'peer') server()
else if (mode === 'cdn') cdn()
else probe(mode).catch(() => { process.exitCode = 1 })
