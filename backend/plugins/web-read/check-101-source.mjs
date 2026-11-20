import { deepStrictEqual, equal } from 'node:assert/strict'
import { once } from 'node:events'
import { Agent, request } from 'node:http'
import { createServer } from 'node:net'
import { requestPublicResource } from '../../../src/network-request.ts'

const final = 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok'
const upgrade = 'HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: fixture\r\n\r\n'
for (const [name, wire, peerEnds, informational] of [
  ['ordinary', final, true, []],
  ['informational', 'HTTP/1.1 103 Early Hints\r\nLink: </fixture>\r\n\r\nHTTP/1.1 100 Continue\r\n\r\n' + final, true, [103, 100]],
  ['101-peer-fin', upgrade + 'fixture', true, []],
  ['101-quiet-timeout', upgrade, false, []],
  ['101-quiet-caller', upgrade, false, []],
]) {
  const caller = new AbortController()
  const sockets = new Set()
  let settled = false
  let peerSawPending = false
  let responseEvents = 0
  const info = []
  const agent = new Agent({ keepAlive: true })
  const server = createServer((socket) => {
    sockets.add(socket)
    socket.once('close', () => {
      sockets.delete(socket)
      peerSawPending = !settled
      if (name === '101-quiet-caller') caller.abort()
    })
    socket.once('data', () => peerEnds ? socket.end(wire) : socket.write(wire))
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  try {
    const outcome = await requestPublicResource('http://synthetic.fixture.invalid/trace', {
      signal: caller.signal,
      timeoutMs: 250,
    }, {
      lookup: async () => [{ address: '8.8.8.8', family: 4 }],
      request(options, callback) {
        const req = request({ ...options, hostname: '127.0.0.1', port: server.address().port, agent }, callback)
        // Observe ordinary callbacks, never install an upgrade listener.
        req.on('information', (value) => info.push(value.statusCode))
        req.on('response', () => responseEvents++)
        return req
      },
    }).then(async ({ response, statusCode }) => {
      const chunks = []
      for await (const chunk of response) chunks.push(chunk)
      settled = true
      return [statusCode, Buffer.concat(chunks).toString()]
    }, (error) => {
      settled = true
      return error.message
    })
    deepStrictEqual(info, informational)
    if (name.startsWith('101-')) {
      equal(outcome, 'network_cancelled', name)
      equal(responseEvents, 0, name)
      equal(peerSawPending, true, name)
      if (name === '101-quiet-caller') equal(caller.signal.aborted, true, name)
    } else {
      deepStrictEqual(outcome, [200, 'ok'], name)
      equal(responseEvents, 1, name)
    }
  } finally {
    agent.destroy()
    for (const socket of sockets) socket.destroy()
    await new Promise((resolve) => server.close(resolve))
  }
}
console.log('PASS 5 pinned WebRead response/upgrade lifecycle cases')
