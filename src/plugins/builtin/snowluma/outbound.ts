import { Worker } from 'node:worker_threads'
import { availableMemory } from 'node:process'
import { OneBotError, type Obj } from './protocol.ts'

const MiB = 1024 * 1024
const MAX_SOURCE = 64 * MiB
const MAX_INLINE = MiB
const MAX_WIRE = 96 * MiB
// Shared admission covers all adapters, including encoded bodies waiting for acknowledgements.
const configuredCapacity = process.env.HUMANENGINE_OUTBOUND_MAX_RESERVED_MIB
const capacityMiB = configuredCapacity === undefined ? 768 : Number(configuredCapacity)
if (!Number.isSafeInteger(capacityMiB) || capacityMiB < 16 || capacityMiB > 768)
  throw new Error('invalid_outbound_memory_capacity')
const MAX_RESERVED = capacityMiB * MiB
const MAX_JOBS = 8
let reserved = 0
let jobs = 0
let workers = 0
const MAX_WORKERS = 2

/** Internal diagnostics, with no payload or authorized path exposure. */
export const outboundEncodingUsage = () => ({ jobs, reservedBytes: reserved, workers })

export interface OutboundSource {
  index: number
  path: string
  bytes: number
  hash: string
}
export interface OutboundPreparation {
  sources: OutboundSource[]
  validate(): void
}
export interface OutboundLease {
  bytes: Uint8Array<ArrayBuffer>
  release(): void
}
interface Job {
  id: number
  value: Obj
  sources: OutboundSource[]
  resolve(value: OutboundLease): void
  reject(error: unknown): void
  cleanup(): void
  release(): void
  cancelled: boolean
}

// Plain JS is intentionally embedded: production Nuxt bundles must not resolve a TS worker URL.
const workerCode = String.raw`
(async () => {
const { parentPort } = await import('node:worker_threads')
const { openSync, fstatSync, readSync, closeSync } = await import('node:fs')
const { createHash, randomUUID } = await import('node:crypto')
parentPort.on('message', ({ id, value, sources }) => {
  try {
    const params = value.params || value
    const indices = new Set()
    const markers = sources.map(source => {
      if (indices.has(source.index)) throw new Error('duplicate_source_index')
      indices.add(source.index)
      const marker = '__humanengine_original_' + randomUUID() + '__'
      params.message[source.index].data.file = marker
      return { source, token: JSON.stringify(marker) }
    })
    // Serialize ONLY the bounded envelope, never the multi-megabyte base64 strings.
    const template = JSON.stringify(value)
    const replacements = markers.map(({ source, token }) => {
      const at = template.indexOf(token)
      if (at < 0 || template.indexOf(token, at + token.length) !== -1)
        throw new Error('ambiguous_source_marker')
      return { source, token, at }
    }).sort((a, b) => a.at - b.at)
    const length = Buffer.byteLength(template) + replacements.reduce((total, { source, token }) =>
      total + 4 * Math.ceil(source.bytes / 3) + 11 - Buffer.byteLength(token), 0)
    if (!Number.isSafeInteger(length) || length > 96 * 1024 * 1024)
      throw new Error('outbound_wire_too_large')
    const wire = Buffer.allocUnsafeSlow(length)
    let offset = 0, cursor = 0
    for (const { source, token, at } of replacements) {
      offset += wire.write(template.slice(cursor, at), offset, 'utf8')
      offset += wire.write('"base64://', offset, 'ascii')
      const fd = openSync(source.path, 'r')
      let bytes
      try {
        const stat = fstatSync(fd)
        if (!stat.isFile() || stat.size !== source.bytes || stat.size > 64 * 1024 * 1024)
          throw new Error('outbound_media_changed')
        bytes = Buffer.allocUnsafe(source.bytes)
        let read = 0
        while (read < bytes.length) {
          const count = readSync(fd, bytes, read, bytes.length - read, read)
          if (!count) throw new Error('outbound_media_changed')
          read += count
        }
        const after = fstatSync(fd)
        if (after.size !== stat.size || after.mtimeMs !== stat.mtimeMs || after.ctimeMs !== stat.ctimeMs)
          throw new Error('outbound_media_changed')
      } finally { closeSync(fd) }
      if (createHash('sha256').update(bytes).digest('hex') !== source.hash)
        throw new Error('outbound_media_changed')
      offset += wire.write(bytes.toString('base64'), offset, 'ascii')
      offset += wire.write('"', offset, 'ascii')
      cursor = at + token.length
    }
    offset += wire.write(template.slice(cursor), offset, 'utf8')
    if (offset !== length) throw new Error('outbound_wire_length_mismatch')
    parentPort.postMessage({ id, bytes: wire }, [wire.buffer])
  } catch { parentPort.postMessage({ id, error: 'outbound_encoding_failed' }) }
})
})()
`

/** Bounded transport-owned worker. Cancellation never frees still-running worker memory. */
class OutboundPool {
  private worker?: Worker
  private active?: Job
  private queue: Job[] = []
  private sequence = 0
  private disposed = false
  private idleTimer?: ReturnType<typeof setTimeout>

  encode(value: Obj, sources: OutboundSource[], signal?: AbortSignal): Promise<OutboundLease> {
    if (this.disposed || signal?.aborted)
      return Promise.reject(new OneBotError('request_cancelled'))
    let inline = 0
    let nodes = 0
    const count = (item: unknown, depth = 0): void => {
      if (++nodes > 4096 || depth > 16) throw new OneBotError('outbound_wire_too_large')
      if (typeof item === 'string') inline += item.length * 6 + 2
      else if (item === null || typeof item === 'number' || typeof item === 'boolean') inline += 32
      else if (Array.isArray(item)) for (const child of item) count(child, depth + 1)
      else if (item && typeof item === 'object') {
        for (const [key, child] of Object.entries(item)) {
          inline += key.length * 6 + 4
          count(child, depth + 1)
        }
      } else throw new OneBotError('invalid_payload')
      if (inline > MAX_INLINE) throw new OneBotError('outbound_wire_too_large')
    }
    let charge: number
    try {
      count(value)
      if (sources.length > 64) throw new OneBotError('outbound_media_too_large')
      let total = 0
      for (const source of sources) {
        if (
          !Number.isSafeInteger(source.bytes) ||
          source.bytes <= 0 ||
          source.bytes > MAX_SOURCE ||
          !/^[a-f0-9]{64}$/.test(source.hash) ||
          source.path.length > 4096
        )
          throw new OneBotError('outbound_media_invalid')
        total += source.bytes
      }
      if (total > MAX_SOURCE) throw new OneBotError('outbound_media_too_large')
      // Source + base64 + UTF-16 JSON + transferable output + clone/encoding headroom.
      charge = total * 8 + inline * 4 + MiB
      if (jobs >= MAX_JOBS || reserved + charge > MAX_RESERVED)
        throw new OneBotError('outbound_encoding_busy')
      // Node accounts for cgroup limits. Preserve host headroom instead of admitting a known OOM.
      if (reserved + charge + 128 * MiB > availableMemory())
        throw new OneBotError('outbound_memory_unavailable')
    } catch (error) {
      return Promise.reject(error)
    }
    jobs++
    reserved += charge
    let released = false
    const release = () => {
      if (released) return
      released = true
      reserved -= charge
      jobs--
    }
    // Tiny envelopes retain the low-latency path; its worst-case work is explicitly bounded.
    if (!sources.length && inline <= 64 * 1024) {
      try {
        const bytes = new TextEncoder().encode(JSON.stringify(value))
        return Promise.resolve({ bytes, release })
      } catch (error) {
        release()
        return Promise.reject(error)
      }
    }
    return new Promise((resolve, reject) => {
      const abort = () => {
        job.cancelled = true
        job.cleanup()
        reject(new OneBotError('request_cancelled'))
        if (this.active !== job) {
          this.queue = this.queue.filter((entry) => entry !== job)
          release()
        }
      }
      const job: Job = {
        id: ++this.sequence,
        value,
        sources,
        resolve,
        reject,
        release,
        cancelled: false,
        cleanup: () => signal?.removeEventListener('abort', abort),
      }
      signal?.addEventListener('abort', abort, { once: true })
      this.queue.push(job)
      this.pump()
    })
  }

  dispose(): void {
    this.disposed = true
    this.fail()
  }

  private fail(code = 'outbound_encoding_failed'): void {
    clearTimeout(this.idleTimer)
    const worker = this.worker
    this.worker = undefined
    const active = this.active
    this.active = undefined
    for (const job of this.queue.splice(0)) {
      job.cleanup()
      job.reject(new OneBotError(code))
      job.release()
    }
    if (active) {
      active.cleanup()
      active.reject(new OneBotError(code))
      // Termination is async; ownership ends only when its memory can no longer grow.
      if (worker) void worker.terminate().then(active.release, active.release)
      else active.release()
    } else if (worker) void worker.terminate()
  }

  private pump(): void {
    if (this.disposed || this.active || !this.queue.length) return
    clearTimeout(this.idleTimer)
    try {
      if (!this.worker) {
        if (workers >= MAX_WORKERS) throw new OneBotError('outbound_encoding_busy')
        const worker = new Worker(workerCode, { eval: true })
        workers++
        this.worker = worker
        worker.on(
          'message',
          (result: { id: number; bytes?: Uint8Array<ArrayBuffer>; error?: string }) => {
            if (this.worker !== worker || this.active?.id !== result.id) return
            const job = this.active
            this.active = undefined
            job.cleanup()
            if (
              job.cancelled ||
              result.error ||
              !result.bytes ||
              result.bytes.byteLength > MAX_WIRE
            ) {
              job.release()
              job.reject(new OneBotError('outbound_encoding_failed'))
            } else job.resolve({ bytes: result.bytes, release: job.release })
            worker.unref()
            this.pump()
            if (!this.active) {
              this.idleTimer = setTimeout(() => {
                if (this.worker === worker && !this.active) this.fail()
              }, 30_000)
              this.idleTimer.unref()
            }
          },
        )
        worker.on('error', () => {
          if (this.worker === worker) this.fail()
        })
        worker.on('exit', () => {
          workers--
          if (this.worker === worker) this.fail()
        })
      }
      this.active = this.queue.shift()!
      this.worker.ref()
      this.worker.postMessage({
        id: this.active.id,
        value: this.active.value,
        sources: this.active.sources,
      })
    } catch (error) {
      this.fail(error instanceof OneBotError ? error.code : 'outbound_encoding_failed')
    }
  }
}

let shared: { pool: OutboundPool; owners: number } | undefined
/** Adapter lifetimes share one reusable FIFO worker; disposing one cancels only its jobs. */
export class OutboundEncoder {
  private readonly lifetime = new AbortController()
  private readonly owner: { pool: OutboundPool; owners: number }
  constructor() {
    shared ??= { pool: new OutboundPool(), owners: 0 }
    this.owner = shared
    this.owner.owners++
  }
  encode(value: Obj, sources: OutboundSource[], signal?: AbortSignal): Promise<OutboundLease> {
    return this.owner.pool.encode(
      value,
      sources,
      AbortSignal.any([this.lifetime.signal, ...(signal ? [signal] : [])]),
    )
  }
  dispose(): void {
    if (this.lifetime.signal.aborted) return
    this.lifetime.abort()
    if (--this.owner.owners === 0) {
      this.owner.pool.dispose()
      if (shared === this.owner) shared = undefined
    }
  }
}
