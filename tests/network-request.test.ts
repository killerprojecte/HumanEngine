import assert from 'node:assert/strict'
import { test } from 'node:test'
import { once } from 'node:events'
import {
  createServer,
  request,
  type Server,
  type RequestOptions,
  type RequestListener,
} from 'node:http'
import { requestPublicResource, type NetworkTransport } from '../src/network-request.ts'

async function fixture(handler: RequestListener) {
  const server = createServer(handler)
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const destinations: RequestOptions[] = []
  const transport: NetworkTransport = {
    lookup: async (hostname) => [
      { address: hostname === 'private.test' ? '127.0.0.1' : '93.184.215.14', family: 4 },
    ],
    request(options, callback) {
      destinations.push(options)
      assert.equal(
        options.hostname,
        '93.184.215.14',
        'production destination remains public and pinned',
      )
      return request(
        { ...options, protocol: 'http:', hostname: '127.0.0.1', port: address.port },
        callback,
      )
    },
  }
  return { transport, destinations, close: () => close(server) }
}
async function close(server: Server) {
  server.closeAllConnections()
  await new Promise<void>((resolve) => server.close(() => resolve()))
}
async function bytes(stream: AsyncIterable<Uint8Array>) {
  const chunks: Uint8Array[] = []
  for await (const chunk of stream) chunks.push(chunk)
  return Buffer.concat(chunks)
}

test('large request and response bodies stream without the former 1 MiB limit; HTTP errors retain their body', async () => {
  const payload = Buffer.alloc(2 * 1024 * 1024 + 7, 0xab)
  const f = await fixture(async (req, res) => {
    assert.equal(req.method, 'POST')
    assert.equal(req.headers.host, 'public.test')
    assert.deepEqual(await bytes(req), payload)
    res.writeHead(422, {
      'content-type': 'application/octet-stream',
      'content-length': payload.length,
    })
    for (let offset = 0; offset < payload.length; offset += 32768) {
      if (!res.write(payload.subarray(offset, offset + 32768))) await once(res, 'drain')
    }
    res.end()
  })
  try {
    const result = await requestPublicResource(
      'http://public.test/',
      { method: 'POST', body: payload },
      f.transport,
    )
    assert.equal(result.statusCode, 422)
    assert.equal(result.contentType, 'application/octet-stream')
    assert.deepEqual(await bytes(result.response), payload)
    assert.equal(result.response.complete, true)
  } finally {
    await f.close()
  }
})

test('redirects revalidate DNS, preserve method/body for 307, and discard user headers across origins', async () => {
  const f = await fixture(async (req, res) => {
    if (req.url === '/start') {
      assert.equal(req.headers.authorization, 'Bearer fixture-only')
      res.writeHead(307, { location: 'http://second.test/end' }).end()
    } else {
      assert.equal(req.method, 'PUT')
      assert.equal(req.headers.authorization, undefined)
      assert.equal(req.headers['x-private'], undefined)
      assert.equal((await bytes(req)).toString(), 'fixture body')
      res.end('done')
    }
  })
  try {
    const result = await requestPublicResource(
      'http://public.test/start',
      {
        method: 'PUT',
        body: 'fixture body',
        headers: { Authorization: 'Bearer fixture-only', 'x-private': 'fixture' },
      },
      f.transport,
    )
    assert.equal(result.finalUrl, 'http://second.test/end')
    assert.equal((await bytes(result.response)).toString(), 'done')
  } finally {
    await f.close()
  }
  const denied = await fixture((_req, res) =>
    res.writeHead(302, { location: 'http://private.test/' }).end(),
  )
  try {
    await assert.rejects(
      requestPublicResource('http://public.test/', {}, denied.transport),
      /address_not_public/,
    )
    assert.equal(denied.destinations.length, 1)
  } finally {
    await denied.close()
  }
})

test('raw encoded bodies are preserved, and cancellation or premature EOF does not look complete', async () => {
  const f = await fixture((req, res) => {
    if (req.url === '/raw') {
      res.writeHead(200, { 'content-encoding': 'gzip' }).end(Buffer.from([31, 139, 8]))
    } else if (req.url === '/broken') {
      res.writeHead(200, { 'content-length': '100' })
      res.write('partial')
      setTimeout(() => res.destroy(), 10)
    } else {
      res.writeHead(200)
      res.write('partial')
    }
  })
  try {
    const raw = await requestPublicResource('http://public.test/raw', {}, f.transport)
    assert.equal(raw.headers['content-encoding'], 'gzip')
    assert.deepEqual(await bytes(raw.response), Buffer.from([31, 139, 8]))
    const broken = await requestPublicResource('http://public.test/broken', {}, f.transport)
    await assert.rejects(bytes(broken.response))
    assert.equal(broken.response.complete, false)
    const controller = new AbortController()
    const cancelled = await requestPublicResource(
      'http://public.test/hang',
      { signal: controller.signal },
      f.transport,
    )
    const reading = bytes(cancelled.response)
    controller.abort()
    await assert.rejects(reading)
    assert.equal(cancelled.response.complete, false)
  } finally {
    await f.close()
  }
})

test('framing overrides and unsupported methods are rejected before any network request', async () => {
  const f = await fixture((_req, res) => res.end('unused'))
  try {
    for (const name of ['Host', 'Content-Length', 'Transfer-Encoding', 'Connection'])
      await assert.rejects(
        requestPublicResource(
          'http://public.test/',
          { headers: { [name]: 'invalid' } },
          f.transport,
        ),
        /invalid_network_header/,
      )
    await assert.rejects(
      requestPublicResource('http://public.test/', { method: 'CONNECT' }, f.transport),
      /invalid_network_request/,
    )
    await assert.rejects(
      requestPublicResource('http://public.test/', { method: 'HEAD', body: 'bad' }, f.transport),
      /invalid_network_request/,
    )
    assert.equal(f.destinations.length, 0)
  } finally {
    await f.close()
  }
})

test('explicit GET data and repeated response header fields preserve curl-like fidelity', async () => {
  const f = await fixture(async (req, res) => {
    assert.equal(req.method, 'GET')
    assert.equal((await bytes(req)).toString(), 'explicit data')
    res.writeHead(200, ['X-Duplicate', 'one', 'X-Duplicate', 'two'])
    res.end('ok')
  })
  try {
    const result = await requestPublicResource(
      'http://public.test/',
      { method: 'GET', body: 'explicit data' },
      f.transport,
    )
    assert.deepEqual(result.rawHeaders.slice(0, 4), ['X-Duplicate', 'one', 'X-Duplicate', 'two'])
    assert.equal((await bytes(result.response)).toString(), 'ok')
  } finally {
    await f.close()
  }
})

test('303 changes POST to GET, chunked responses exceed 1 MiB, and deadline closes stalled bodies', async () => {
  const payload = Buffer.alloc(1048576 + 29, 120)
  const f = await fixture(async (req, res) => {
    if (req.url === '/start') {
      assert.equal(req.method, 'POST')
      await bytes(req)
      res.writeHead(303, { location: '/chunked' }).end()
    } else if (req.url === '/chunked') {
      assert.equal(req.method, 'GET')
      assert.equal(req.headers['content-length'], undefined)
      assert.equal((await bytes(req)).length, 0)
      for (let offset = 0; offset < payload.length; offset += 16384) {
        if (!res.write(payload.subarray(offset, offset + 16384))) await once(res, 'drain')
      }
      res.end()
    } else {
      res.write('stalled')
    }
  })
  try {
    const result = await requestPublicResource(
      'http://public.test/start',
      { method: 'POST', body: 'data' },
      f.transport,
    )
    assert.equal(result.headers['content-length'], undefined)
    assert.deepEqual(await bytes(result.response), payload)
    const stalled = await requestPublicResource(
      'http://public.test/stalled',
      { timeoutMs: 100 },
      f.transport,
    )
    await assert.rejects(bytes(stalled.response))
    assert.equal(stalled.response.complete, false)
  } finally {
    await f.close()
  }
})
