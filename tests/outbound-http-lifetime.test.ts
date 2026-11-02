import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { createServer as createTlsServer } from 'node:https'
import { once } from 'node:events'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { preparedHttpRequest } from '../src/plugins/builtin/snowluma/http-prepared.ts'

async function address(server: ReturnType<typeof createServer>) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const value = server.address()
  assert(value && typeof value !== 'string')
  return value.port
}

test('prepared HTTP slow-reader abort retains the lease until native write completion or close', async () => {
  let accepted!: () => void
  const received = new Promise<void>((resolve) => {
    accepted = resolve
  })
  const server = createServer((request) => {
    request.pause()
    accepted()
  })
  const port = await address(server)
  const signal = new AbortController()
  let releases = 0
  const request = preparedHttpRequest(
    new URL(`http://127.0.0.1:${port}/send_group_msg`),
    { 'Content-Type': 'application/json' },
    {
      bytes: new Uint8Array(8 * 1024 * 1024),
      release: () => {
        releases++
      },
    },
    signal.signal,
  )
  const rejected = assert.rejects(request.response)
  try {
    await received
    request.release() // Simulated early action settlement cannot free a blocked write.
    assert.equal(releases, 0)
    signal.abort()
    await rejected
    for (let i = 0; releases === 0 && i < 100; i++)
      await new Promise((resolve) => setTimeout(resolve, 5))
    assert.equal(releases, 1)
  } finally {
    signal.abort()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('prepared HTTP does not follow redirects and exposes streaming response bytes unchanged', async () => {
  let requests = 0,
    releases = 0
  const server = createServer((request, response) => {
    requests++
    assert.equal(request.headers.authorization, 'Bearer test-token')
    assert.equal(request.headers['content-type'], 'application/json')
    assert.equal(request.headers['content-length'], '2')
    request.resume()
    response.writeHead(302, { Location: '/must-not-follow' })
    response.end('redirect')
  })
  const port = await address(server)
  try {
    const request = preparedHttpRequest(
      new URL(`http://127.0.0.1:${port}/action`),
      { 'Content-Type': 'application/json', Authorization: 'Bearer test-token' },
      {
        bytes: new TextEncoder().encode('{}'),
        release: () => {
          releases++
        },
      },
      new AbortController().signal,
    )
    const response = await request.response
    assert.equal(response.status, 302)
    assert.equal(response.ok, false)
    await response.body!.cancel()
    request.release()
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(requests, 1)
    assert.equal(releases, 1)
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('prepared HTTPS keeps certificate verification enabled', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'outbound-tls-'))
  let server: ReturnType<typeof createTlsServer> | undefined
  try {
    const key = join(dir, 'key.pem'),
      cert = join(dir, 'cert.pem')
    execFileSync(
      'openssl',
      [
        'req',
        '-x509',
        '-newkey',
        'rsa:2048',
        '-nodes',
        '-keyout',
        key,
        '-out',
        cert,
        '-subj',
        '/CN=localhost',
        '-days',
        '1',
      ],
      { stdio: 'ignore', timeout: 10000 },
    )
    let requests = 0,
      releases = 0
    server = createTlsServer(
      { key: await readFile(key), cert: await readFile(cert) },
      (_request, response) => {
        requests++
        response.end('{}')
      },
    )
    server.on('tlsClientError', () => {})
    const port = await address(server)
    const request = preparedHttpRequest(
      new URL(`https://127.0.0.1:${port}/action`),
      {},
      {
        bytes: new TextEncoder().encode('{}'),
        release: () => {
          releases++
        },
      },
      new AbortController().signal,
    )
    await assert.rejects(
      request.response,
      (error: unknown) => error instanceof Error && /certificate|self.signed/i.test(error.message),
    )
    request.release()
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(requests, 0)
    assert.equal(releases, 1)
  } finally {
    server?.closeAllConnections()
    if (server) await new Promise<void>((resolve) => server!.close(() => resolve()))
    await rm(dir, { recursive: true, force: true })
  }
})

test('prepared responses decode gzip, deflate, raw deflate, Brotli and stacked encodings', async () => {
  const { gzipSync, deflateSync, deflateRawSync, brotliCompressSync } = await import('node:zlib')
  const json = Buffer.from(JSON.stringify({ status: 'ok', retcode: 0, data: { message_id: 99 } }))
  const variants = [
    ['gzip', gzipSync(json)],
    ['deflate', deflateSync(json)],
    ['deflate', deflateRawSync(json)],
    ['br', brotliCompressSync(json)],
    ['gzip, br', brotliCompressSync(gzipSync(json))],
  ] as const
  let variant = variants[0] as (typeof variants)[number]
  const server = createServer((request, response) => {
    request.resume()
    response.setHeader('Content-Encoding', variant[0])
    response.end(variant[1])
  })
  const port = await address(server)
  try {
    for (variant of variants) {
      const request = preparedHttpRequest(
        new URL(`http://127.0.0.1:${port}/action`),
        {},
        { bytes: new TextEncoder().encode('{}'), release() {} },
        new AbortController().signal,
      )
      try {
        const response = await request.response
        assert.equal(await new Response(response.body).text(), json.toString(), variant[0])
      } finally {
        request.release()
      }
    }
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('early HTTP terminal responses stop unfinished uploads without changing authoritative status', async (t) => {
  const http = await import('node:http')
  const { syncBuiltinESMExports } = await import('node:module')
  for (const status of [200, 413]) {
    const server = createServer((request, response) => {
      request.pause()
      response.writeHead(status, { 'Content-Type': 'application/json' })
      response.end(
        JSON.stringify({
          status: status === 200 ? 'ok' : 'failed',
          retcode: status === 200 ? 0 : 1400,
          data: { message_id: 99 },
        }),
      )
    })
    const port = await address(server)
    let releases = 0
    let native: import('node:http').ClientRequest | undefined
    const original = http.default.request
    try {
      // Capture the real native ownership boundary, not a paused peer's unrelated message lifecycle.
      http.default.request = function (this: unknown, ...args: unknown[]) {
        native = Reflect.apply(original, this, args) as import('node:http').ClientRequest
        return native
      } as typeof original
      syncBuiltinESMExports()
      const request = preparedHttpRequest(
        new URL(`http://127.0.0.1:${port}/action`),
        {},
        {
          bytes: new Uint8Array(8 * 1024 * 1024),
          release: () => {
            releases++
          },
        },
        new AbortController().signal,
      )
      http.default.request = original
      syncBuiltinESMExports()
      assert(native)
      const closed = once(native, 'close')
      const response = await request.response
      const result = (await new Response(response.body).json()) as { data: { message_id: number } }
      assert.equal(response.status, status)
      assert.equal(result.data.message_id, 99)
      const blocked = !native.writableFinished
      t.diagnostic(`early status ${status}; native write pending at settlement: ${blocked}`)
      request.release()
      let timer: ReturnType<typeof setTimeout> | undefined
      try {
        await Promise.race([
          closed,
          new Promise<never>((_, reject) => {
            timer = setTimeout(() => reject(new Error('unfinished_native_upload_retained')), 1000)
          }),
        ])
      } finally {
        clearTimeout(timer)
      }
      assert.equal(native.destroyed, true)
      assert.equal(native.writableLength, 0)
      if (blocked) assert.equal(native.socket?.destroyed, true)
      assert.equal(releases, 1)
      assert.equal(response.status, status)
    } finally {
      http.default.request = original
      syncBuiltinESMExports()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    }
  }
})
