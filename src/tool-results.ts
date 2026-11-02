import { randomUUID } from 'node:crypto'
import { mkdtemp, open, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

export type ResultSource = string | Uint8Array | AsyncIterable<Uint8Array>
export interface ResultOptions {
  contentType?: string
  encoding?: 'utf8' | 'base64'
  previewBytes?: number
}
export interface ResultOwner {
  scope: string
  sceneId: string
  pluginId: string
  dependencies: readonly string[]
  validations: readonly (() => boolean)[]
}
export interface ResultRead {
  resultId: string
  offset?: number
  length?: number
  full?: boolean
  encoding?: 'utf8' | 'base64'
}
interface WriteLease {
  owner: ResultOwner
  invalidated: boolean
  controller: AbortController
}
interface Entry extends ResultOwner {
  path: string
  totalBytes: number
  contentType: string
  encoding: 'utf8' | 'base64'
  expiresAt: number
}
// This is a model-view budget, not a response/download/storage limit.
const VIEW_BYTES = 32768
const validInteger = (n: number) => Number.isSafeInteger(n) && n >= 0
export class ToolResultCache {
  private directory?: Promise<string>
  private entries = new Map<string, Entry>()
  private closed = false
  private controller = new AbortController()
  private pending = new Set<Promise<unknown>>()
  private writes = new Set<WriteLease>()
  private sweep: ReturnType<typeof setInterval>
  constructor(
    private ttlMs = 30 * 60_000,
    private now = Date.now,
  ) {
    this.sweep = setInterval(() => void this.expire(), Math.min(ttlMs, 60_000))
    this.sweep.unref()
  }
  private async expire() {
    for (const [id, entry] of this.entries) if (entry.expiresAt <= this.now()) await this.remove(id)
  }
  private async remove(id: string) {
    const entry = this.entries.get(id)
    this.entries.delete(id)
    // Revocation is immediate even if an OS-level unlink fails. Shutdown retries directory cleanup.
    if (entry) await rm(entry.path, { force: true }).catch(() => {})
  }
  async invalidate(sceneId: string, messageId: string) {
    for (const lease of this.writes) {
      if (lease.owner.sceneId === sceneId && lease.owner.dependencies.includes(messageId)) {
        lease.invalidated = true
        lease.controller.abort()
      }
    }
    await Promise.all(
      [...this.entries]
        .filter(([, entry]) => entry.sceneId === sceneId && entry.dependencies.includes(messageId))
        .map(([id]) => this.remove(id)),
    )
  }
  async invalidatePlugin(pluginId: string) {
    for (const lease of this.writes) {
      if (lease.owner.pluginId === pluginId) {
        lease.invalidated = true
        lease.controller.abort()
      }
    }
    await Promise.all(
      [...this.entries]
        .filter(([, entry]) => entry.pluginId === pluginId)
        .map(([id]) => this.remove(id)),
    )
  }
  async store(
    source: ResultSource,
    owner: ResultOwner,
    signal: AbortSignal,
    options: ResultOptions = {},
  ) {
    const lease: WriteLease = { owner, invalidated: false, controller: new AbortController() }
    this.writes.add(lease)
    const task = this.write(source, owner, signal, options, lease)
    this.pending.add(task)
    try {
      return await task
    } catch (error) {
      if (
        typeof source === 'object' &&
        'destroy' in source &&
        typeof source.destroy === 'function'
      ) {
        try {
          source.destroy()
        } catch {}
      }
      if (error instanceof Error && error.message.startsWith('tool_result_')) throw error
      if (signal.aborted || this.controller.signal.aborted) throw new Error('tool_result_cancelled')
      throw new Error('tool_result_storage_failed')
    } finally {
      this.pending.delete(task)
      this.writes.delete(lease)
    }
  }
  private async write(
    source: ResultSource,
    owner: ResultOwner,
    signal: AbortSignal,
    options: ResultOptions,
    lease: WriteLease,
  ) {
    if (this.closed) throw new Error('tool_result_cache_closed')
    const lifetime = AbortSignal.any([signal, this.controller.signal, lease.controller.signal])
    lifetime.throwIfAborted()
    const directory = await (this.directory ??= mkdtemp(
      join(tmpdir(), 'humanengine-results-'),
    )).catch(() => {
      throw new Error('tool_result_storage_failed')
    })
    const resultId = randomUUID()
    const path = join(directory, resultId)
    const file = await open(path, 'wx', 0o600).catch(() => {
      throw new Error('tool_result_storage_failed')
    })
    let totalBytes = 0
    const chunks =
      typeof source === 'string' || source instanceof Uint8Array
        ? [typeof source === 'string' ? Buffer.from(source) : source]
        : source
    const iterator =
      Symbol.asyncIterator in chunks ? chunks[Symbol.asyncIterator]() : chunks[Symbol.iterator]()
    let completed = false
    const next = () =>
      new Promise<IteratorResult<Uint8Array>>((resolve, reject) => {
        const abort = () => reject(new Error('tool_result_cancelled'))
        lifetime.addEventListener('abort', abort, { once: true })
        if (lifetime.aborted) {
          lifetime.removeEventListener('abort', abort)
          abort()
          return
        }
        Promise.resolve()
          .then(() => {
            lifetime.throwIfAborted()
            return iterator.next()
          })
          .then(resolve, reject)
          .finally(() => {
            lifetime.removeEventListener('abort', abort)
          })
          .catch(() => {})
      })
    try {
      for (;;) {
        const item = await next()
        if (item.done) {
          completed = true
          break
        }
        const chunk = item.value
        lifetime.throwIfAborted()
        if (!(chunk instanceof Uint8Array)) throw new Error('invalid_tool_result_chunk')
        let offset = 0
        while (offset < chunk.byteLength) {
          const { bytesWritten } = await file.write(chunk, offset, chunk.byteLength - offset)
          if (!bytesWritten) throw new Error('tool_result_storage_failed')
          offset += bytesWritten
        }
        totalBytes += chunk.byteLength
        if (!Number.isSafeInteger(totalBytes)) throw new Error('tool_result_storage_failed')
      }
      lifetime.throwIfAborted()
      if (!owner.validations.every((valid) => valid())) throw new Error('tool_result_invalidated')
      await file.close()
      if (lease.invalidated) throw new Error('tool_result_invalidated')
      lifetime.throwIfAborted()
      const entry: Entry = {
        ...owner,
        dependencies: [...owner.dependencies],
        validations: [...owner.validations],
        path,
        totalBytes,
        contentType: options.contentType ?? 'application/octet-stream',
        encoding: options.encoding ?? (typeof source === 'string' ? 'utf8' : 'base64'),
        expiresAt: this.now() + this.ttlMs,
      }
      this.entries.set(resultId, entry)
      return await this.read(
        { resultId, length: options.previewBytes ?? 4096 },
        owner.scope,
        lifetime,
      )
    } catch (error) {
      await file.close().catch(() => {})
      this.entries.delete(resultId)
      await rm(path, { force: true }).catch(() => {})
      if (lease.invalidated) throw new Error('tool_result_invalidated')
      if (lifetime.aborted) throw new Error('tool_result_cancelled')
      if (error instanceof Error && error.message.startsWith('tool_result_')) throw error
      throw new Error('tool_result_storage_failed')
    } finally {
      if (!completed) {
        // Streams release their socket immediately; arbitrary iterators may have a stalled next().
        // Never await return() behind that pending next, but handle its eventual rejection.
        if (
          typeof source === 'object' &&
          'destroy' in source &&
          typeof source.destroy === 'function'
        ) {
          try {
            source.destroy()
          } catch {}
        }
        try {
          void Promise.resolve(iterator.return?.()).catch(() => {})
        } catch {}
      }
    }
  }
  async read(
    input: ResultRead,
    scope: string,
    signal: AbortSignal,
    adopt?: (entry: ResultOwner) => void,
  ) {
    signal.throwIfAborted()
    const { offset = 0, length, full = false } = input
    if (
      !validInteger(offset) ||
      (length !== undefined && !validInteger(length)) ||
      (full && length !== undefined)
    )
      throw new Error('tool_result_invalid_range')
    const entry = this.entries.get(input.resultId)
    if (!entry || entry.scope !== scope) throw new Error('tool_result_not_found_or_expired')
    if (entry.expiresAt <= this.now()) {
      await this.remove(input.resultId)
      throw new Error('tool_result_expired')
    }
    if (!entry.validations.every((valid) => valid())) {
      await this.remove(input.resultId)
      throw new Error('tool_result_invalidated')
    }
    if (offset > entry.totalBytes) throw new Error('tool_result_invalid_range')
    const requestedBytes = full ? entry.totalBytes - offset : (length ?? 4096)
    let count = Math.min(requestedBytes, entry.totalBytes - offset, VIEW_BYTES)
    const file = await open(entry.path, 'r').catch(() => {
      throw new Error('tool_result_not_found_or_expired')
    })
    let bytes: Buffer
    try {
      bytes = Buffer.alloc(count)
      let read = 0
      while (read < count) {
        const { bytesRead } = await file.read(bytes, read, count - read, offset + read)
        if (!bytesRead) throw new Error('tool_result_storage_failed')
        read += bytesRead
      }
    } finally {
      await file.close()
    }
    let encoding = input.encoding ?? entry.encoding
    const encode = (buffer: Buffer) => {
      if (encoding === 'utf8') {
        try {
          return new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(buffer)
        } catch {
          encoding = 'base64'
        }
      }
      return buffer.toString('base64')
    }
    let content = encode(bytes)
    // Account for JSON escaping and base64 expansion, including nested receipt encoding.
    while (Buffer.byteLength(JSON.stringify(JSON.stringify(content))) > VIEW_BYTES && count > 0) {
      count = Math.floor(count * 0.8)
      bytes = bytes.subarray(0, count)
      content = encode(bytes)
    }
    signal.throwIfAborted()
    if (!entry.validations.every((valid) => valid()) || !this.entries.has(input.resultId))
      throw new Error('tool_result_invalidated')
    adopt?.(entry)
    const end = offset + count
    return {
      resultId: input.resultId,
      totalBytes: entry.totalBytes,
      contentType: entry.contentType,
      complete: true,
      expiresAt: new Date(entry.expiresAt).toISOString(),
      retention:
        'Until expiry, source invalidation, plugin disable, or runtime shutdown; no restart persistence.',
      rangeUnit: 'bytes',
      requestedRange: { offset, length: requestedBytes },
      returnedRange: { offset, length: count, end },
      encoding,
      content,
      truncated: end < entry.totalBytes || offset !== 0,
      requestComplete: count === Math.min(requestedBytes, entry.totalBytes - offset),
      nextOffset: end < entry.totalBytes ? end : null,
      viewBudgetBytes: VIEW_BYTES,
      retrievalTool: 'tool-results.read',
      trust: 'untrusted_tool_content',
    }
  }
  async dispose() {
    this.closed = true
    this.controller.abort()
    clearInterval(this.sweep)
    await Promise.allSettled([...this.pending])
    this.entries.clear()
    if (this.directory) await rm(await this.directory, { recursive: true, force: true })
  }
}
