import { Worker } from 'node:worker_threads'

export type Operation =
  | 'fetch'
  | 'config'
  | 'publication-info'
  | 'publication-begin'
  | 'publication-verify'
  | 'publication-abandon'

/** Fixed operations only. Canceled work is tracked until the worker finishes and
 * never retried. This transport adds no admission, payload, or time limits. */
export class WorkerRpc {
  private next = 0
  private dead = false
  private closing?: Promise<void>
  private closeControl?: { id: number; resolve: () => void; reject: (error: Error) => void }
  private pending = new Map<
    number,
    {
      resolve: (value: any) => void
      reject: (error: Error) => void
      cleanup: () => void
      settled: boolean
      lease: boolean
    }
  >()
  private leases = new WeakMap<object, () => void>()
  readonly ready: Promise<void>
  constructor(
    readonly worker: Worker,
    options: { onUnexpectedExit?: () => void } = {},
  ) {
    let started = false
    let readyResolve!: () => void, readyReject!: (error: Error) => void
    this.ready = new Promise((resolve, reject) => {
      readyResolve = resolve
      readyReject = reject
    })
    const fail = (error = new Error('worker_unavailable')) => {
      const unexpected = started && !this.dead && !this.closing
      readyReject(error)
      this.fail(error)
      if (unexpected) options.onUnexpectedExit?.()
    }
    worker.on('error', () => fail())
    worker.on('exit', () => fail())
    worker.on('message', (message) => {
      if (message.ready === true) {
        if (this.dead) return
        if (!this.closing) started = true
        // Preserve the startup/close handshake: readiness may settle while closing,
        // but call() stays closed and this never re-arms fatal supervision.
        readyResolve()
        return
      }
      if (message.startupError) {
        fail(new Error(message.startupError))
        return
      }
      const closeControl = this.closeControl
      if (closeControl && closeControl.id === message.id) {
        message.error
          ? closeControl.reject(new Error('worker_shutdown_failed'))
          : closeControl.resolve()
        return
      }
      const entry = this.pending.get(message.id)
      if (!entry) return
      const release = () => {
        if (!this.pending.delete(message.id)) return
        entry.cleanup()
        this.worker.postMessage({ release: message.id })
      }
      if (
        !entry.settled &&
        !message.error &&
        entry.lease &&
        message.result &&
        typeof message.result === 'object'
      ) {
        entry.settled = true
        entry.cleanup()
        this.leases.set(message.result, release)
        entry.resolve(message.result)
      } else {
        release()
        if (!entry.settled) {
          entry.settled = true
          message.error
            ? entry.reject(new Error('worker_request_failed'))
            : entry.resolve(message.result)
        }
      }
    })
  }
  releaseResult(value: object) {
    const release = this.leases.get(value)
    this.leases.delete(value)
    release?.()
  }
  private fail(error: Error) {
    this.dead = true
    this.closeControl?.reject(error)
    for (const entry of this.pending.values()) {
      entry.cleanup()
      if (!entry.settled) entry.reject(error)
    }
    this.pending.clear()
  }
  call<T>(
    op: Operation,
    payload: unknown,
    options: { signal?: AbortSignal; lease?: boolean } = {},
  ): Promise<T> {
    if (this.closing) return Promise.reject(new Error('worker_closing'))
    if (this.dead) return Promise.reject(new Error('worker_unavailable'))
    if (options.signal?.aborted) return Promise.reject(new Error('request_aborted'))
    const id = ++this.next
    return new Promise<T>((resolve, reject) => {
      const cancel = () => {
        if (entry.settled) return
        entry.settled = true
        entry.cleanup()
        reject(new Error('request_aborted'))
        // Worker retains and aborts the operation; no replay even if side effects happened.
        this.worker.postMessage({ cancel: id })
      }
      const entry = {
        resolve,
        reject,
        settled: false,
        lease: options.lease === true,
        cleanup: () => {
          options.signal?.removeEventListener('abort', cancel)
        },
      }
      this.pending.set(id, entry)
      options.signal?.addEventListener('abort', cancel, { once: true })
      try {
        // HTTP request buffers are owned allocations. Transfer them instead of
        // retaining a structured-clone copy in the host while the owner runs.
        const body =
          op === 'fetch' && payload && typeof payload === 'object' && 'body' in payload
            ? payload.body
            : undefined
        const transfer =
          body instanceof Uint8Array && body.buffer instanceof ArrayBuffer ? [body.buffer] : []
        this.worker.postMessage({ id, op, payload }, transfer)
      } catch {
        this.pending.delete(id)
        entry.cleanup()
        reject(new Error('worker_dispatch_failed'))
      }
    })
  }
  close(): Promise<void> {
    if (this.closing) return this.closing
    // One dedicated control message, independent of queued data and canceled work.
    // Wait for normal cleanup or worker failure; do not add a shutdown deadline.
    this.closing = Promise.resolve().then(async () => {
      if (!this.dead) {
        try {
          await new Promise<void>((resolve, reject) => {
            const id = ++this.next
            this.closeControl = { id, resolve, reject }
            this.worker.postMessage({ id, op: 'close', payload: null })
          })
        } catch {
          console.error('[HumanEngine] Owner shutdown incomplete; no operations will be replayed')
        } finally {
          this.closeControl = undefined
        }
      }
      this.fail(new Error('worker_closed'))
      await this.worker.terminate()
    })
    return this.closing
  }
}

export async function readBody(
  body: ReadableStream<Uint8Array> | null,
  options: { signal?: AbortSignal } = {},
): Promise<Uint8Array> {
  if (!body) return new Uint8Array()
  const reader = body.getReader(),
    chunks: Uint8Array[] = []
  let length = 0
  const abort = () => {
    void reader.cancel().catch(() => {})
  }
  options.signal?.addEventListener('abort', abort, { once: true })
  try {
    while (true) {
      if (options.signal?.aborted) throw new Error('request_aborted')
      const chunk = await reader.read()
      if (options.signal?.aborted) throw new Error('request_aborted')
      if (chunk.done) break
      length += chunk.value.byteLength
      chunks.push(chunk.value)
    }
    // One owned allocation is transferable and avoids an extra Buffer.concat copy.
    // As before this boundary, callers may exhaust memory with unbounded bodies.
    const result = new Uint8Array(length)
    let offset = 0
    for (const chunk of chunks) {
      result.set(chunk, offset)
      offset += chunk.byteLength
    }
    return result
  } catch (error) {
    await reader.cancel().catch(() => {})
    throw error
  } finally {
    options.signal?.removeEventListener('abort', abort)
    reader.releaseLock()
  }
}
