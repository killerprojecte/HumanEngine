import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtemp, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { once } from 'node:events'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { pathToFileURL } from 'node:url'
import { WebSocketServer } from 'ws'
import { OutboundEncoder } from '../src/plugins/builtin/snowluma/outbound.ts'
import { OneBotAdapter } from '../src/onebot.ts'

async function mediaFixture() {
  const dir = await mkdtemp(join(tmpdir(), 'outbound-'))
  const bytes = Buffer.alloc(32768, 42)
  const path = join(dir, 'original')
  await writeFile(path, bytes)
  return {
    bytes,
    source: {
      index: 0,
      path,
      bytes: bytes.length,
      hash: createHash('sha256').update(bytes).digest('hex'),
    },
    clean: () => rm(dir, { recursive: true, force: true }),
  }
}
const payload = () => ({ message: [{ type: 'image', data: { file: '' } }] })

test('worker encodes original bytes and full envelope; leases retain global job admission until release', async () => {
  const fixture = await mediaFixture()
  const encoder = new OutboundEncoder()
  try {
    const leases = await Promise.all(
      Array.from({ length: 8 }, () => encoder.encode(payload(), [fixture.source])),
    )
    const parsed = JSON.parse(Buffer.from(leases[0]!.bytes).toString())
    assert.equal(parsed.message[0].data.file, `base64://${fixture.bytes.toString('base64')}`)
    await assert.rejects(encoder.encode(payload(), []), /outbound_encoding_busy/)
    leases.forEach((lease) => {
      lease.release()
      lease.release()
    })
    const next = await encoder.encode(payload(), [])
    next.release()
  } finally {
    encoder.dispose()
    await fixture.clean()
  }
})

test('global byte admission rejects a second 64 MiB reservation before reading either source', async () => {
  const fixture = await mediaFixture()
  const encoder = new OutboundEncoder()
  try {
    // Metadata reserves the maximum before dispatch; the small fixture then fails exact file size.
    const source = { ...fixture.source, bytes: 64 * 1024 * 1024 }
    const first = encoder.encode(payload(), [source])
    const failed = assert.rejects(first, /outbound_encoding_failed/)
    await assert.rejects(encoder.encode(payload(), [source]), /outbound_encoding_busy/)
    await failed
  } finally {
    encoder.dispose()
    await fixture.clean()
  }
})

test('admission rejects bytes, inline size, source mutation and hash mismatch before any send', async () => {
  const fixture = await mediaFixture()
  const encoder = new OutboundEncoder()
  try {
    await assert.rejects(
      encoder.encode(payload(), [{ ...fixture.source, bytes: 64 * 1024 * 1024 + 1 }]),
      /outbound_media_invalid/,
    )
    await assert.rejects(
      encoder.encode({ text: 'x'.repeat(1024 * 1024) }, []),
      /outbound_wire_too_large/,
    )
    await assert.rejects(
      encoder.encode(payload(), [{ ...fixture.source, hash: '0'.repeat(64) }]),
      /outbound_encoding_failed/,
    )
    await writeFile(fixture.source.path, 'changed')
    await assert.rejects(encoder.encode(payload(), [fixture.source]), /outbound_encoding_failed/)
  } finally {
    encoder.dispose()
    await fixture.clean()
  }
})

test('queued cancellation is inert; active cancellation discards late completion and disposal settles jobs', async () => {
  const fixture = await mediaFixture()
  const encoder = new OutboundEncoder()
  const active = new AbortController(),
    queued = new AbortController()
  try {
    const first = encoder.encode(payload(), [fixture.source], active.signal)
    const second = encoder.encode(payload(), [fixture.source], queued.signal)
    const firstRejected = assert.rejects(first, /request_cancelled/)
    const secondRejected = assert.rejects(second, /request_cancelled/)
    queued.abort()
    active.abort()
    await Promise.all([firstRejected, secondRejected])
    // FIFO completion is an explicit barrier: the cancelled active job has really ended.
    const barrier = await encoder.encode(payload(), [fixture.source])
    barrier.release()
    const pending = encoder.encode(payload(), [fixture.source])
    const rejected = assert.rejects(pending, /request_cancelled|outbound_encoding_failed/)
    encoder.dispose()
    await rejected
    await assert.rejects(encoder.encode(payload(), []), /request_cancelled/)
  } finally {
    encoder.dispose()
    await fixture.clean()
  }
})

test('unexpected worker exit fails in-flight and queued jobs without replay', async () => {
  const fixture = await mediaFixture()
  const encoder = new OutboundEncoder()
  try {
    const first = encoder.encode(payload(), [fixture.source])
    const second = encoder.encode(payload(), [fixture.source])
    const failures = [
      assert.rejects(first, /outbound_encoding_failed/),
      assert.rejects(second, /outbound_encoding_failed/),
    ]
    // Deliberately crash the real native worker at its lifecycle boundary.
    const worker = (
      encoder as unknown as { owner: { pool: { worker: import('node:worker_threads').Worker } } }
    ).owner.pool.worker
    await worker.terminate()
    await Promise.all(failures)
    const lease = await encoder.encode(payload(), [fixture.source])
    lease.release()
  } finally {
    encoder.dispose()
    await fixture.clean()
  }
})

test('first-party WS sends text frames and revalidates source and core gates after worker await', async () => {
  const fixture = await mediaFixture()
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  let sends = 0,
    valid = true,
    coreValid = true
  server.on('connection', (socket) =>
    socket.on('message', (bytes, binary) => {
      assert.equal(binary, false)
      const request = JSON.parse(bytes.toString())
      if (request.action !== 'get_login_info') {
        sends++
        assert.equal(
          request.params.message[0].data.file,
          `base64://${fixture.bytes.toString('base64')}`,
        )
      }
      socket.send(
        JSON.stringify({
          echo: request.echo,
          status: 'ok',
          retcode: 0,
          data: request.action === 'get_login_info' ? { user_id: 42 } : { message_id: 99 },
        }),
      )
    }),
  )
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address !== 'string')
  const adapter = new OneBotAdapter(
    { id: 'snow', accountId: '42', url: `ws://127.0.0.1:${address.port}`, credentialEnv: '' },
    {
      resolveMediaSource: () => ({
        ...fixture.source,
        type: 'image',
        validate: () => {
          if (!valid) throw new Error('revoked')
        },
      }),
    },
  )
  try {
    await adapter.connect()
    const scene = adapter.scene('group', '9')
    const segment = { parts: [{ type: 'media' as const, assetId: 'fixture', mediaType: 'image' }] }
    const beforeTransmit = () => {
      if (!coreValid) throw new Error('send_invalidated')
    }
    const revoked = adapter.send(scene, segment, undefined, beforeTransmit)
    valid = false
    assert.equal((await revoked).status, 'failed')
    assert.equal(sends, 0)
    valid = true
    const invalidated = adapter.send(scene, segment, undefined, beforeTransmit)
    coreValid = false
    assert.equal((await invalidated).status, 'failed')
    assert.equal(sends, 0)
    coreValid = true
    assert.equal((await adapter.send(scene, segment, undefined, beforeTransmit)).status, 'sent')
    assert.equal(sends, 1)
    const disposed = adapter.send(scene, segment)
    adapter.dispose()
    assert.notEqual((await disposed).status, 'sent')
    assert.equal(sends, 1)
  } finally {
    adapter.dispose()
    server.clients.forEach((socket) => socket.terminate())
    await new Promise<void>((resolve) => server.close(() => resolve()))
    await fixture.clean()
  }
})

test('WS reservation survives timeout and late ACK until send-buffer ownership ends', async () => {
  const fixture = await mediaFixture()
  const { outboundEncodingUsage } = await import('../src/plugins/builtin/snowluma/outbound.ts')
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 })
  let lateAck: (() => void) | undefined
  let sends = 0
  server.on('connection', (socket) =>
    socket.on('message', (bytes) => {
      const request = JSON.parse(bytes.toString())
      const reply = () =>
        socket.send(
          JSON.stringify({
            echo: request.echo,
            status: 'ok',
            retcode: 0,
            data: request.action === 'get_login_info' ? { user_id: 42 } : { message_id: 99 },
          }),
        )
      if (request.action === 'get_login_info') reply()
      else {
        sends++
        lateAck = reply
      }
    }),
  )
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address !== 'string')
  const adapter = new OneBotAdapter(
    {
      id: 'snow',
      accountId: '42',
      url: `ws://127.0.0.1:${address.port}`,
      credentialEnv: '',
      timeoutMs: 200,
    },
    {
      resolveMediaSource: () => ({ ...fixture.source, type: 'image', validate() {} }),
    },
  )
  try {
    await adapter.connect()
    const socket = (adapter as unknown as { socket: import('ws').WebSocket }).socket
    const send = socket.send.bind(socket)
    let drain: (() => void) | undefined
    socket.send = ((
      data: Parameters<typeof socket.send>[0],
      options: object,
      callback: () => void,
    ) => {
      drain = callback
      send(data, options)
    }) as typeof socket.send
    const receipt = await adapter.send(adapter.scene('group', '9'), {
      parts: [{ type: 'media', assetId: 'fixture', mediaType: 'image' }],
    })
    assert.equal(receipt.status, 'unknown')
    assert.equal(sends, 1)
    assert.equal(outboundEncodingUsage().jobs, 1)
    lateAck!()
    await new Promise((resolve) => setTimeout(resolve, 10))
    assert.equal(outboundEncodingUsage().jobs, 1)
    assert.equal(sends, 1)
    drain!()
    assert.equal(outboundEncodingUsage().jobs, 0)
  } finally {
    adapter.dispose()
    server.clients.forEach((socket) => socket.terminate())
    await new Promise<void>((resolve) => server.close(() => resolve()))
    await fixture.clean()
  }
})

test('HTTP original-source body preserves headers and never fetches after final revocation', async () => {
  const { createServer } = await import('node:http')
  const { gzipSync } = await import('node:zlib')
  const fixture = await mediaFixture()
  let sends = 0,
    oversized = false,
    valid = true
  const server = createServer(async (request, response) => {
    assert.equal(request.headers.authorization, 'Bearer fixture-token')
    assert.equal(request.headers['content-type'], 'application/json')
    const chunks: Buffer[] = []
    for await (const chunk of request) chunks.push(chunk)
    const body = JSON.parse(Buffer.concat(chunks).toString())
    if (request.url !== '/get_login_info') {
      sends++
      assert.equal(body.message[0].data.file, `base64://${fixture.bytes.toString('base64')}`)
      assert.equal(body.group_id, '9')
      assert.equal(body.echo, undefined)
    }
    if (oversized) {
      response.setHeader('Content-Encoding', 'gzip')
      response.end(gzipSync(Buffer.alloc(8 * 1024 * 1024 + 1, 32)))
      return
    }
    response.setHeader('Content-Type', 'application/json')
    response.end(
      JSON.stringify({
        status: 'ok',
        retcode: 0,
        data: request.url === '/get_login_info' ? { user_id: 42 } : { message_id: 99 },
      }),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address !== 'string')
  const adapter = new OneBotAdapter(
    {
      id: 'snow',
      accountId: '42',
      transport: 'http',
      url: `http://127.0.0.1:${address.port}`,
      token: 'fixture-token',
      credentialEnv: '',
      eventToken: 'fixture-events',
      eventPort: 0,
    },
    {
      resolveMediaSource: () => ({
        ...fixture.source,
        type: 'image',
        validate: () => {
          if (!valid) throw new Error('revoked')
        },
      }),
    },
  )
  try {
    await adapter.connect()
    const scene = adapter.scene('group', '9')
    const segment = { parts: [{ type: 'media' as const, assetId: 'fixture', mediaType: 'image' }] }
    const revoked = adapter.send(scene, segment)
    valid = false
    assert.equal((await revoked).status, 'failed')
    assert.equal(sends, 0)
    valid = true
    assert.equal((await adapter.send(scene, segment)).status, 'sent')
    assert.equal(sends, 1)
    oversized = true
    const tooLarge = await adapter.send(scene, segment)
    assert.equal(tooLarge.status, 'unknown')
    assert.equal(tooLarge.detail, 'http_response_too_large')
    oversized = false
    assert.equal(sends, 2)
    const disposed = adapter.send(scene, segment, undefined, () => adapter.dispose())
    assert.notEqual((await disposed).status, 'sent')
    assert.equal(sends, 2)
  } finally {
    adapter.dispose()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    await fixture.clean()
  }
})

test('three adapters share reusable worker capacity; disposing one never cancels another', async () => {
  const fixture = await mediaFixture()
  const { outboundEncodingUsage } = await import('../src/plugins/builtin/snowluma/outbound.ts')
  const encoders = Array.from({ length: 3 }, () => new OutboundEncoder())
  try {
    for (const encoder of encoders) {
      const lease = await encoder.encode(payload(), [fixture.source])
      lease.release()
    }
    const cancelled = encoders[0]!.encode(payload(), [fixture.source])
    const rejected = assert.rejects(cancelled, /request_cancelled/)
    const continued = encoders[2]!.encode(payload(), [fixture.source])
    encoders[0]!.dispose()
    await rejected
    const lease = await continued
    lease.release()
    assert.equal(outboundEncodingUsage().jobs, 0)
    assert(outboundEncodingUsage().workers <= 2)
    const again = await encoders[1]!.encode(payload(), [fixture.source])
    again.release()
  } finally {
    encoders.forEach((encoder) => encoder.dispose())
    await fixture.clean()
  }
})

test('direct wire substitution exactly matches JSON serialization for Unicode and multiple originals', async () => {
  const fixture = await mediaFixture()
  const encoder = new OutboundEncoder()
  try {
    const value = {
      action: 'send_group_msg',
      echo: 'echo',
      params: {
        group_id: '9',
        message: [
          {
            type: 'text',
            data: { text: '你好 🎉 "quoted" \\ path\n__humanengine_original_marker__\u0000' },
          },
          { type: 'image', data: { file: '' } },
          { type: 'text', data: { text: 'another "__humanengine_original_marker__"' } },
          { type: 'image', data: { file: '' } },
        ],
      },
    }
    // Intentionally reverse descriptor order: output follows actual JSON token offsets.
    const lease = await encoder.encode(value, [
      { ...fixture.source, index: 3 },
      { ...fixture.source, index: 1 },
    ])
    const expected = structuredClone(value)
    expected.params.message[1]!.data.file = `base64://${fixture.bytes.toString('base64')}`
    expected.params.message[3]!.data.file = `base64://${fixture.bytes.toString('base64')}`
    assert.equal(Buffer.from(lease.bytes).toString(), JSON.stringify(expected))
    lease.release()
    await assert.rejects(
      encoder.encode(payload(), [fixture.source, fixture.source]),
      /outbound_encoding_failed/,
    )
  } finally {
    encoder.dispose()
    await fixture.clean()
  }
})

test('worker uses native flag inheritance in module-input mode with deployment conditions', async () => {
  const fixture = await mediaFixture()
  try {
    const moduleUrl = pathToFileURL(
      join(process.cwd(), 'src/plugins/builtin/snowluma/outbound.ts'),
    ).href
    const { stdout } = await promisify(execFile)(
      process.execPath,
      [
        '--import',
        'tsx',
        '--input-type=module',
        '--conditions=outbound-sentinel',
        '-e',
        `
      import {OutboundEncoder} from ${JSON.stringify(moduleUrl)};
      const encoder=new OutboundEncoder();
      try {
        const lease=await encoder.encode(${JSON.stringify(payload())},[${JSON.stringify(fixture.source)}]);
        console.log(Buffer.from(lease.bytes).toString());
        lease.release();
      } finally {encoder.dispose()}
    `,
      ],
      { timeout: 5000 },
    )
    assert.equal(
      JSON.parse(stdout).message[0].data.file,
      `base64://${fixture.bytes.toString('base64')}`,
    )
  } finally {
    await fixture.clean()
  }
})
