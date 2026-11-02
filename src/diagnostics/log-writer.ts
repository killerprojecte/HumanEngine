/// <reference lib="es2024.sharedmemory" />
import { Worker } from 'node:worker_threads'
import { realpathSync } from 'node:fs'
import { resolve, dirname, basename } from 'node:path'

// Only this immutable program is executed. Caller data never becomes executable source.
const workerSource = String.raw`
(async () => {
const { parentPort, workerData } = await import('node:worker_threads');
const fs = await import('node:fs');
const path = await import('node:path');
const state = new Int32Array(workerData.state);
const totals = new BigInt64Array(workerData.totals);
let fd, size = 0, failed = false;
function failure() { failed = true; Atomics.store(state, 4, 1); }
function open() {
  if (fd !== undefined) return;
  fs.mkdirSync(workerData.directory, { recursive: true, mode: 0o700 });
  fd = fs.openSync(path.join(workerData.directory, 'runtime.jsonl'), 'a+', 0o600);
  size = fs.fstatSync(fd).size;
  if (size) {
    const tail = Buffer.alloc(1);
    fs.readSync(fd, tail, 0, 1, size - 1);
    if (tail[0] !== 10) rotate(); // Preserve an interrupted record in the rotated file.
  }
}
function rotate() {
  fs.fsyncSync(fd); fs.closeSync(fd); fd = undefined;
  const file = path.join(workerData.directory, 'runtime.jsonl');
  for (let i = 2; i >= 1; i--) {
    const from = i === 1 ? file : file + '.' + (i - 1), to = file + '.' + i;
    try { fs.unlinkSync(to); } catch (e) { if (e.code !== 'ENOENT') throw e; }
    try { fs.renameSync(from, to); } catch (e) { if (e.code !== 'ENOENT') throw e; }
  }
  open();
}
parentPort.on('message', message => {
  if (message.kind === 'records') {
    for (const bytes of message.records) {
    try {
      if (failed) throw new Error('sink_failed');
      if (workerData.directory) { open(); if (size > workerData.rotateBytes) rotate(); }
      let offset = 0, backoff = 2;
      while (offset < bytes.byteLength) {
        if (Atomics.load(state, 7)) throw new Error('delivery_stopped');
        try {
          const n = fs.writeSync(workerData.directory ? fd : 1, bytes, offset, bytes.byteLength - offset);
          if (!n) throw new Error('zero_write');
          offset += n;
          backoff = 2;
          Atomics.store(state, 8, 0);
        } catch (error) {
          if (workerData.directory || !['EAGAIN', 'EWOULDBLOCK'].includes(error.code)) throw error;
          // This syscall wrote zero bytes. Preserve offset: never replay its prior prefix.
          Atomics.store(state, 8, 1);
          Atomics.wait(state, 7, 0, backoff);
          backoff = Math.min(32, backoff * 2);
        }
      }
      size += bytes.byteLength;
      Atomics.add(totals, 0, 1n);
    } catch { failure(); Atomics.add(totals, 1, 1n); }
    finally {
      Atomics.store(state, 8, 0);
      Atomics.sub(state, 0, bytes.byteLength); Atomics.sub(state, 1, 1);
      Atomics.notify(state, 1);
    }
    }
  } else {
    try { if (fd !== undefined) fs.fsyncSync(fd); } catch { failure(); }
    if (message.kind === 'close') {
      try { if (fd !== undefined) fs.closeSync(fd); } catch { failure(); }
      fd = undefined; Atomics.store(state, 5, 1); parentPort.close();
    }
    Atomics.store(state, 6, message.sequence); Atomics.notify(state, 6);
  }
});
})();
`

export interface LogDeliveryStatus {
  state: 'ready' | 'degraded' | 'closed'
  backpressured: boolean
  acceptedRecords: number
  writtenRecords: number
  rejectedRecords: number
  rejectedBytes: number
  failedRecords: number
  unconfirmedRecords: number
  pendingRecords: number
  pendingBytes: number
  maxPendingBytes: number
  maxPendingRecords: number
  lastFailure: string | null
}
export interface LogFlushResult {
  complete: boolean
  durable: boolean
  throughAcceptedRecords: number
  flushedAcceptedRecords: number
  status: LogDeliveryStatus
}
export interface LogChannelOptions {
  directory?: string
  maxBytes?: number
  maxRecords?: number
  rotateBytes?: number
}

/** Bounded best-effort delivery. Admission never waits for a worker or filesystem. */
export class LogChannel {
  private readonly counters = new Int32Array(new SharedArrayBuffer(9 * 4))
  private readonly totals = new BigInt64Array(new SharedArrayBuffer(2 * 8))
  private readonly worker: Worker
  private accepted = 0
  private rejected = 0
  private rejectedBytes = 0
  private failure: string | null = null
  private closing = false
  private abandoned = false
  private exited = false
  private sequence = 0
  private pending: Uint8Array<ArrayBuffer>[] = []
  private dispatchScheduled = false
  private flushedThrough = 0
  private barrier: Promise<void> | undefined
  readonly maxBytes: number
  readonly maxRecords: number
  constructor(private readonly options: LogChannelOptions = {}) {
    this.maxBytes = options.maxBytes ?? (options.directory ? 4 * 1024 * 1024 : 1024 * 1024)
    this.maxRecords = options.maxRecords ?? (options.directory ? 1024 : 256)
    if (
      !Number.isSafeInteger(this.maxBytes) ||
      this.maxBytes < 1024 ||
      this.maxBytes > 64 * 1024 * 1024 ||
      !Number.isSafeInteger(this.maxRecords) ||
      this.maxRecords < 8 ||
      this.maxRecords > 65536
    )
      throw new Error('invalid_log_queue_limits')
    this.worker = new Worker(workerSource, {
      eval: true,
      // Native inheritance retains process permission/resource/preload behavior.
      workerData: {
        directory: options.directory,
        rotateBytes: options.rotateBytes ?? 2 * 1024 * 1024,
        state: this.counters.buffer,
        totals: this.totals.buffer,
      },
    })
    this.worker.on('error', () => this.abandon('worker_failed'))
    this.worker.on('exit', (code) => {
      if (code !== 0 || !this.closed) this.abandon('worker_exited')
      this.exited = true
      Atomics.store(this.counters, 5, 1)
    })
    this.worker.unref()
  }
  onExit(callback: () => void) {
    if (this.exited) queueMicrotask(callback)
    else this.worker.once('exit', callback)
  }
  get closed() {
    return Atomics.load(this.counters, 5) === 1
  }
  private abandon(reason: string) {
    this.failure ??= reason
    this.abandoned = true
    this.pending = []
    this.closing = true
    Atomics.store(this.counters, 7, 1)
    Atomics.notify(this.counters, 7)
    Atomics.notify(this.counters, 6)
    void this.worker.terminate().catch(() => {})
  }
  enqueue(record: string, priority = false): boolean {
    const bytes = Buffer.byteLength(record)
    const reserveBytes = priority ? 0 : Math.floor(this.maxBytes / 8)
    const reserveRecords = priority ? 0 : Math.max(1, Math.floor(this.maxRecords / 8))
    const reason = this.closing
      ? 'sink_closed'
      : Atomics.load(this.counters, 4)
        ? 'sink_failed'
        : bytes > this.maxBytes
          ? 'record_too_large'
          : Atomics.load(this.counters, 0) + bytes > this.maxBytes - reserveBytes ||
              Atomics.load(this.counters, 1) >= this.maxRecords - reserveRecords
            ? 'queue_full'
            : null
    if (reason) {
      this.rejected++
      this.rejectedBytes += bytes
      this.failure = reason
      return false
    }
    Atomics.add(this.counters, 0, bytes)
    Atomics.add(this.counters, 1, 1)
    try {
      // Exact, unpooled storage: reserve before allocation and never expose unwritten bytes.
      const buffer = Buffer.allocUnsafeSlow(bytes)
      if (buffer.write(record, 0, bytes, 'utf8') !== bytes)
        throw new Error('log_encoding_incomplete')
      this.pending.push(buffer)
      this.accepted++
    } catch {
      Atomics.sub(this.counters, 0, bytes)
      Atomics.sub(this.counters, 1, 1)
      this.rejected++
      this.rejectedBytes += bytes
      this.failure = 'encode_failed'
      return false
    }
    if (!this.dispatchScheduled) {
      this.dispatchScheduled = true
      queueMicrotask(() => {
        this.dispatchScheduled = false
        this.dispatch()
      })
    }
    return true
  }
  private dispatch() {
    const records = this.pending
    this.pending = []
    if (!records.length || this.abandoned) return
    try {
      this.worker.postMessage(
        { kind: 'records', records },
        records.map((record) => record.buffer),
      )
    } catch {
      // Admission already succeeded; failed handoff is unconfirmed, never replayed.
      this.abandon('enqueue_failed')
    }
  }
  get status(): LogDeliveryStatus {
    const written = Number(Atomics.load(this.totals, 0)),
      failed = Number(Atomics.load(this.totals, 1))
    return {
      state:
        this.failure || Atomics.load(this.counters, 4)
          ? 'degraded'
          : this.closing
            ? 'closed'
            : 'ready',
      backpressured: !this.abandoned && Atomics.load(this.counters, 8) === 1,
      acceptedRecords: this.accepted,
      writtenRecords: written,
      rejectedRecords: this.rejected,
      rejectedBytes: this.rejectedBytes,
      failedRecords: failed,
      unconfirmedRecords: this.abandoned ? Math.max(0, this.accepted - written - failed) : 0,
      pendingRecords: this.abandoned ? 0 : Atomics.load(this.counters, 1),
      pendingBytes: this.abandoned ? 0 : Atomics.load(this.counters, 0),
      maxPendingBytes: this.maxBytes,
      maxPendingRecords: this.maxRecords,
      lastFailure: this.failure ?? (Atomics.load(this.counters, 4) ? 'sink_write_failed' : null),
    }
  }
  private request(close: boolean) {
    // Every admitted record precedes its flush/close barrier, including this stack.
    this.dispatch()
    const sequence = ++this.sequence
    try {
      this.worker.postMessage({ kind: close ? 'close' : 'flush', sequence })
    } catch {
      this.abandon('flush_failed')
    }
    return sequence
  }
  private result(target: number): LogFlushResult {
    const status = this.status
    const complete = this.flushedThrough >= target && status.state !== 'degraded'
    return {
      complete,
      durable: complete && !!this.options.directory,
      throughAcceptedRecords: target,
      flushedAcceptedRecords: this.flushedThrough,
      status,
    }
  }
  async flush(timeoutMs = 5000, close = false): Promise<LogFlushResult> {
    const target = this.accepted,
      deadline = Date.now() + timeoutMs
    if (close) this.closing = true
    while (!this.abandoned) {
      if (this.barrier) {
        const remaining = deadline - Date.now()
        if (remaining <= 0) {
          this.abandon('flush_timeout')
          break
        }
        let timer: ReturnType<typeof setTimeout> | undefined
        const settled = await Promise.race([
          this.barrier.then(() => true),
          new Promise<boolean>((resolve) => {
            timer = setTimeout(() => resolve(false), remaining)
          }),
        ])
        clearTimeout(timer)
        if (!settled) {
          this.abandon('flush_timeout')
          break
        }
        continue
      }
      if (this.closed || (!close && this.flushedThrough >= target)) return this.result(target)
      const through = this.accepted,
        sequence = this.request(close)
      const pending = (async () => {
        while (
          Atomics.load(this.counters, 6) < sequence &&
          Date.now() < deadline &&
          !this.abandoned
        ) {
          const observed = Atomics.load(this.counters, 6)
          if (observed >= sequence) break
          const remaining = Math.max(0, deadline - Date.now())
          const wait = Atomics.waitAsync(this.counters, 6, observed, remaining)
          if (wait.async) {
            // waitAsync alone does not keep an unref'ed worker/process alive.
            let keepAlive: ReturnType<typeof setTimeout> | undefined
            try {
              await Promise.race([
                wait.value,
                new Promise((resolve) => {
                  keepAlive = setTimeout(resolve, remaining)
                }),
              ])
            } finally {
              clearTimeout(keepAlive)
            }
          }
        }
        if (Atomics.load(this.counters, 6) >= sequence && !Atomics.load(this.counters, 4))
          this.flushedThrough = Math.max(this.flushedThrough, through)
        else if (Atomics.load(this.counters, 6) < sequence) this.abandon('flush_timeout')
      })()
      this.barrier = pending
      try {
        await pending
      } finally {
        if (this.barrier === pending) this.barrier = undefined
      }
      // A write/fsync failure is terminal; it must not launch repeated barriers.
      if (Atomics.load(this.counters, 4)) return this.result(target)
    }
    return this.result(target)
  }
  /** Compatibility barrier for synchronous Store.close; never used by log(). */
  flushSync(timeoutMs = 5000, close = false): LogFlushResult {
    const target = this.accepted
    if (close) this.closing = true
    if (this.closed || this.abandoned) return this.result(target)
    const sequence = this.request(close),
      deadline = Date.now() + timeoutMs
    while (Atomics.load(this.counters, 6) < sequence) {
      const remaining = deadline - Date.now()
      if (remaining <= 0) break
      Atomics.wait(this.counters, 6, Atomics.load(this.counters, 6), remaining)
    }
    if (Atomics.load(this.counters, 6) >= sequence) {
      if (!Atomics.load(this.counters, 4))
        this.flushedThrough = Math.max(this.flushedThrough, target)
    } else this.abandon('flush_timeout')
    return this.result(target)
  }
  closeSync(timeoutMs = 5000) {
    return this.flushSync(timeoutMs, true)
  }
}

interface Owner {
  channel: LogChannel
  references: number
  previous?: LogDeliveryStatus
}
const files = new Map<string, Owner>()
let stdout: Owner | undefined
function releaseExitedOwner(owner: Owner, channel: LogChannel, key?: string) {
  channel.onExit(() => {
    if (owner.references || owner.channel !== channel) return
    if (key && files.get(key) === owner) files.delete(key)
    else if (!key && stdout === owner) stdout = undefined
  })
}
function canonicalDirectory(directory: string): string {
  const path = resolve(directory)
  try {
    return realpathSync(path)
  } catch {
    const parent = dirname(path)
    return parent === path ? path : resolve(canonicalDirectory(parent), basename(path))
  }
}
function combined(current: LogDeliveryStatus, previous?: LogDeliveryStatus): LogDeliveryStatus {
  if (!previous) return current
  const result = { ...current }
  for (const key of [
    'acceptedRecords',
    'writtenRecords',
    'rejectedRecords',
    'rejectedBytes',
    'failedRecords',
    'unconfirmedRecords',
  ] as const)
    result[key] += previous[key]
  result.lastFailure ??= previous.lastFailure
  if (result.lastFailure) result.state = 'degraded'
  return result
}
export function acquireLogChannel(directory?: string) {
  const key = directory ? canonicalDirectory(directory) : undefined
  let owner = key ? files.get(key) : stdout
  if (!owner) {
    owner = { channel: new LogChannel({ directory: key }), references: 0 }
    if (key) files.set(key, owner)
    else stdout = owner
    releaseExitedOwner(owner, owner.channel, key)
  }
  const shared = owner
  shared.references++
  let released = false
  let releasedRejected = 0
  let releasedRejectedBytes = 0
  let retired: LogChannel | undefined
  let retiredPrevious: LogDeliveryStatus | undefined
  const channel = () => {
    if (released) return retired!
    if (shared.channel.closed) {
      shared.previous = combined(shared.channel.status, shared.previous)
      shared.channel = new LogChannel({ directory: key })
      releaseExitedOwner(shared, shared.channel, key)
    }
    return shared.channel
  }
  const status = (): LogDeliveryStatus => {
    const result = combined(
      (released ? retired! : shared.channel).status,
      released ? retiredPrevious : shared.previous,
    )
    if (!releasedRejected) return result
    return {
      ...result,
      state: 'degraded',
      rejectedRecords: result.rejectedRecords + releasedRejected,
      rejectedBytes: result.rejectedBytes + releasedRejectedBytes,
      lastFailure: result.lastFailure ?? 'lease_released',
    }
  }
  const release = () => {
    if (released) return false
    retired = shared.channel
    retiredPrevious = shared.previous
    released = true
    shared.references--
    return shared.references === 0
  }
  const forget = () => {
    // Do not allow overlapping owners while the old worker is draining or rotating.
    if (shared.references || !shared.channel.closed) return
    if (key && files.get(key) === shared) files.delete(key)
    else if (!key && stdout === shared) stdout = undefined
  }
  const decorate = (result: LogFlushResult) => ({
    ...result,
    complete: result.complete && !status().lastFailure,
    durable: result.durable && !status().lastFailure,
    status: status(),
  })
  return {
    enqueue(record: string, priority = false) {
      if (released) {
        releasedRejected++
        releasedRejectedBytes += Buffer.byteLength(record)
        return false
      }
      return channel().enqueue(record, priority)
    },
    get status() {
      return status()
    },
    async close() {
      if (released) return decorate(await retired!.flush())
      const last = release()
      const result = await retired!.flush(5000, last)
      forget()
      return decorate(result)
    },
    closeSync(timeoutMs = 5000) {
      if (released) return undefined
      const last = release()
      const result = retired!.flushSync(timeoutMs, last)
      forget()
      return decorate(result)
    },
  }
}
