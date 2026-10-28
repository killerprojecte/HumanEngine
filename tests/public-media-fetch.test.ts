import assert from 'node:assert/strict'
import test from 'node:test'
import { EventEmitter } from 'node:events'
import { PassThrough } from 'node:stream'
import { fetchPublicBytes, publicMediaAddress, type MediaNetwork } from '../src/media-fetch.ts'
function transport(
  responses: Array<{ status?: number; location?: string; bytes?: Buffer; length?: string }>,
  addresses = [{ address: '8.8.8.8', family: 4 }],
) {
  const requests: Array<Record<string, unknown>> = []
  const dns: string[] = []
  const fake: MediaNetwork = {
    lookup: async (host) => {
      dns.push(host)
      return addresses
    },
    get: ((options: Record<string, unknown>, callback: (response: unknown) => void) => {
      requests.push(options)
      const request = new EventEmitter() as EventEmitter & { destroy(error?: Error): void }
      request.destroy = (error) => {
        if (error) queueMicrotask(() => request.emit('error', error))
      }
      const input = responses.shift() ?? {}
      const response = Object.assign(new PassThrough(), {
        statusCode: input.status ?? 200,
        headers: {
          ...(input.location ? { location: input.location } : {}),
          ...(input.length ? { 'content-length': input.length } : {}),
        },
      })
      queueMicrotask(() => {
        callback(response)
        response.end(input.bytes ?? Buffer.from('fixture'))
      })
      return request
    }) as MediaNetwork['get'],
  }
  return { fake, requests, dns }
}

test('public HTTP(S) media has no domain restriction and every redirected hostname is DNS pinned', async () => {
  const f = transport([
    { status: 302, location: 'https://arbitrary-public.example/path?fixture=signed' },
    { bytes: Buffer.from('verified-bytes') },
  ])
  const result = await fetchPublicBytes(
    'https://multimedia.nt.qq.com.cn/download?fixture=not-a-real-key',
    undefined,
    f.fake,
  )
  assert.equal(result.toString(), 'verified-bytes')
  assert.deepEqual(f.dns, ['multimedia.nt.qq.com.cn', 'arbitrary-public.example'])
  assert.ok(f.requests.every((request) => request.hostname === '8.8.8.8'))
  assert.equal(f.requests[0]!.servername, 'multimedia.nt.qq.com.cn')
  assert.equal(f.requests[1]!.servername, 'arbitrary-public.example')
  assert.deepEqual(f.requests[1]!.headers, { Host: 'arbitrary-public.example' })
  const http = transport([{}])
  await fetchPublicBytes('http://public.example:8080/image', undefined, http.fake)
  assert.equal(http.requests[0]!.protocol, 'http:')
  assert.equal(http.requests[0]!.port, '8080')
})

test('private, metadata, reserved and mixed-family DNS addresses remain unreachable', async () => {
  for (const address of [
    '127.0.0.1',
    '169.254.169.254',
    '168.63.129.16',
    '192.168.1.1',
    '192.0.0.9',
    '192.0.2.1',
    '100.64.0.1',
    '198.18.0.1',
    '198.51.100.1',
    '203.0.113.9',
    '::1',
    'fe80::1',
    'fc00::1',
    '::ffff:127.0.0.1',
    '2001:0db8:0:0:0:0:0:1',
    '2001:0000::1',
    '2002:7f00::1',
  ]) {
    assert.equal(publicMediaAddress(address), false, address)
    const f = transport(
      [{}],
      [
        { address: '8.8.8.8', family: 4 },
        { address, family: address.includes(':') ? 6 : 4 },
      ],
    )
    await assert.rejects(
      fetchPublicBytes('https://normal.example/image', undefined, f.fake),
      /media_address_not_public/,
    )
    assert.equal(f.requests.length, 0)
  }
  assert.equal(publicMediaAddress('2606:4700:4700::1111'), true)
})

test('redirect destinations are revalidated; credentials, downgrade, loops, size and cancellation are bounded', async () => {
  for (const location of [
    'http://127.0.0.1/private',
    'https://127.0.0.1/private',
    'https://secret@public.example/image',
    'file:///private/image',
  ]) {
    const f = transport([{ status: 302, location }, {}])
    await assert.rejects(
      fetchPublicBytes('https://normal.example/image', undefined, f.fake),
      /media_redirect_not_supported|media_address_not_public/,
    )
    assert.equal(f.requests.length, 1)
  }
  const loop = transport(Array.from({ length: 5 }, () => ({ status: 302, location: '/again' })))
  await assert.rejects(
    fetchPublicBytes('https://normal.example/image', undefined, loop.fake),
    /media_redirect_limit/,
  )
  assert.equal(loop.requests.length, 4)
  const large = transport([{ length: String(10 * 1024 * 1024 + 1) }])
  await assert.rejects(
    fetchPublicBytes('https://normal.example/image', undefined, large.fake),
    /media_size_limit/,
  )
  const cancelled = transport([{}])
  await assert.rejects(
    fetchPublicBytes('https://normal.example/image', AbortSignal.abort(), cancelled.fake),
    /media_cancelled/,
  )
  assert.equal(cancelled.requests.length, 0)
})

test('absolute timeout defeats slow-drip responses and byte budget applies without Content-Length', async () => {
  const { fetchPublicResource } = await import('../src/media-fetch.ts')
  const stream = new PassThrough()
  const slow: MediaNetwork = {
    lookup: async () => [{ address: '8.8.8.8', family: 4 }],
    get: ((options: { signal: AbortSignal }, callback: (response: unknown) => void) => {
      const request = new EventEmitter() as EventEmitter & { destroy(error?: Error): void }
      const timer = setInterval(() => stream.write(Buffer.from('x')), 2)
      request.destroy = (error) => {
        clearInterval(timer)
        stream.destroy()
        queueMicrotask(() => request.emit('error', error ?? new Error('closed')))
      }
      options.signal.addEventListener('abort', () => request.destroy(new Error('aborted')), {
        once: true,
      })
      queueMicrotask(() =>
        callback(
          Object.assign(stream, {
            statusCode: 200,
            headers: { 'content-type': 'text/html; charset=gbk' },
          }),
        ),
      )
      return request
    }) as MediaNetwork['get'],
  }
  const started = Date.now()
  await assert.rejects(
    fetchPublicResource('https://normal.example/image', { timeoutMs: 25 }, slow),
    /media_cancelled/,
  )
  assert.ok(Date.now() - started < 1000)
  const chunked = transport([{ bytes: Buffer.alloc(9) }])
  await assert.rejects(
    fetchPublicResource('https://normal.example/image', { maxBytes: 8 }, chunked.fake),
    /media_size_limit/,
  )
})

test('cloud platform virtual IP is blocked directly and after redirects', async () => {
  const direct = transport([{}])
  await assert.rejects(
    fetchPublicBytes('http://168.63.129.16/', undefined, direct.fake),
    /media_address_not_public/,
  )
  assert.equal(direct.requests.length, 0)
  const redirected = transport([{ status: 302, location: 'https://168.63.129.16/' }])
  await assert.rejects(
    fetchPublicBytes('https://public.example/', undefined, redirected.fake),
    /media_address_not_public/,
  )
  assert.equal(redirected.requests.length, 1)
})
