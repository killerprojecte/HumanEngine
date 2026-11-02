/** Synthetic local-only profile: node --import tsx scripts/outbound-encoding-profile.ts worker|baseline */
import { createHash } from 'node:crypto'
import { mkdtemp, open, rm } from 'node:fs/promises'
import { readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { monitorEventLoopDelay } from 'node:perf_hooks'
import assert from 'node:assert/strict'
import { OutboundEncoder, outboundEncodingUsage } from '../src/plugins/builtin/snowluma/outbound.ts'

const mode = process.argv[2]
if (!['baseline', 'worker'].includes(mode ?? '')) throw new Error('mode_required')
const dir = await mkdtemp(join(tmpdir(), 'outbound-profile-'))
const encoder = new OutboundEncoder()
const delay = monitorEventLoopDelay({ resolution: 5 })
let heartbeat: ReturnType<typeof setInterval> | undefined
try {
  const path = join(dir, 'generated.bin')
  const file = await open(path, 'w')
  const chunk = Buffer.alloc(1024 * 1024, 42)
  const hash = createHash('sha256')
  try {
    for (let i = 0; i < 64; i++) {
      await file.write(chunk)
      hash.update(chunk)
    }
  } finally {
    await file.close()
  }
  const source = { index: 0, path, bytes: 64 * 1024 * 1024, hash: hash.digest('hex') }
  const value = { message: [{ type: 'image', data: { file: '' } }] }
  delay.enable()
  await new Promise((resolve) => setTimeout(resolve, 20))
  delay.reset()
  let heartbeatMaxMs = 0,
    previous = performance.now()
  heartbeat = setInterval(() => {
    const now = performance.now()
    heartbeatMaxMs = Math.max(heartbeatMaxMs, now - previous - 5)
    previous = now
  }, 5)
  const cpu = process.cpuUsage(),
    started = performance.now()
  let encodeElapsedMs = 0,
    encodeCpuMs = 0
  let bytes = 0,
    retained = 0
  if (mode === 'baseline') {
    const original = readFileSync(path)
    value.message[0]!.data.file = `base64://${original.toString('base64')}`
    const wire = JSON.stringify(value)
    bytes = Buffer.byteLength(wire)
    encodeElapsedMs = performance.now() - started
    const used = process.cpuUsage(cpu)
    encodeCpuMs = (used.user + used.system) / 1000
    await new Promise((resolve) => setTimeout(resolve, 30))
  } else {
    const lease = await encoder.encode(value, [source])
    bytes = lease.bytes.length
    encodeElapsedMs = performance.now() - started
    const used = process.cpuUsage(cpu)
    encodeCpuMs = (used.user + used.system) / 1000
    retained = outboundEncodingUsage().reservedBytes
    assert(retained >= source.bytes * 8)
    await assert.rejects(encoder.encode(value, [source]), /outbound_encoding_busy/)
    await new Promise((resolve) => setTimeout(resolve, 30)) // Simulated acknowledgement wait.
    assert.equal(outboundEncodingUsage().reservedBytes, retained)
    lease.release()
    assert.equal(outboundEncodingUsage().reservedBytes, 0)
    const abort = new AbortController()
    const pending = encoder.encode(value, [source], abort.signal)
    const rejected = assert.rejects(pending, /request_cancelled/)
    abort.abort()
    await rejected
    assert(outboundEncodingUsage().reservedBytes >= source.bytes * 8)
    // A worker-backed barrier proves completion and late-result disposal, not merely caller abort.
    const barrier = await encoder.encode({ text: 'x'.repeat(12000) }, [])
    barrier.release()
    assert.equal(outboundEncodingUsage().reservedBytes, 0)
  }
  await new Promise((resolve) => setTimeout(resolve, 20))
  const usage = process.cpuUsage(cpu)
  delay.disable()
  clearInterval(heartbeat)
  console.log(
    JSON.stringify({
      mode,
      inputBytes: source.bytes,
      wireBytes: bytes,
      elapsedMs: performance.now() - started,
      encodeElapsedMs,
      encodeCpuMs,
      heartbeatMaxMs,
      cpuMs: (usage.user + usage.system) / 1000,
      eventLoopMaxMs: delay.max / 1e6,
      eventLoopP99Ms: delay.percentile(99) / 1e6,
      maxRssMiB: process.resourceUsage().maxRSS / 1024,
      retainedReservationBytes: retained,
      finalAdmission: outboundEncodingUsage(),
    }),
  )
} finally {
  clearInterval(heartbeat)
  delay.disable()
  encoder.dispose()
  await rm(dir, { recursive: true, force: true })
}
