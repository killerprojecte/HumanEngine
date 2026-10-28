import { createHash } from 'node:crypto'

interface Entry {
  adapter: object
  sceneId: string
  bytes: Buffer
  expires: number
}
interface Waiter {
  signal: AbortSignal
  available: () => boolean
}
interface Flight {
  adapter: object
  sceneId: string
  controller: AbortController
  waiters: Set<Waiter>
  promise: Promise<Buffer>
}
/** Exact-source bytes only. No filename guesses, URL rewriting, credentials, or failures are cached. */
export class ImageByteCache {
  private readonly adapters = new WeakMap<object, number>()
  private serial = 0
  private readonly entries = new Map<string, Entry>()
  private readonly flights = new Map<string, Flight>()
  readonly stats = { hits: 0, misses: 0, coalesced: 0 }
  constructor(private readonly now = Date.now) {}
  clear(sceneId?: string, adapter?: object) {
    for (const [key, entry] of this.entries)
      if ((!sceneId || entry.sceneId === sceneId) && (!adapter || entry.adapter === adapter))
        this.entries.delete(key)
    for (const [key, flight] of this.flights)
      if ((!sceneId || flight.sceneId === sceneId) && (!adapter || flight.adapter === adapter)) {
        flight.controller.abort()
        this.flights.delete(key)
      }
  }
  async read(
    adapter: object,
    sceneId: string,
    reference: string,
    signal: AbortSignal,
    available: () => boolean,
    load: (signal: AbortSignal) => Promise<Buffer>,
    onAccess?: (kind: 'image_bytes_cached' | 'image_bytes_shared' | 'image_source_read') => void,
  ): Promise<Buffer> {
    const valid = () => !signal.aborted && available()
    if (!valid()) throw new Error('image_source_unavailable')
    let identity = this.adapters.get(adapter)
    if (!identity) {
      identity = ++this.serial
      this.adapters.set(adapter, identity)
    }
    const key = createHash('sha256')
      .update(JSON.stringify([identity, sceneId, reference]))
      .digest('hex')
    for (const [id, entry] of this.entries) if (entry.expires <= this.now()) this.entries.delete(id)
    const cached = this.entries.get(key)
    if (cached) {
      onAccess?.('image_bytes_cached')
      this.stats.hits++
      this.entries.delete(key)
      this.entries.set(key, cached)
      return Buffer.from(cached.bytes)
    }
    let flight = this.flights.get(key)
    if (flight?.controller.signal.aborted) {
      this.flights.delete(key)
      flight = undefined
    }
    if (!flight) {
      if (this.flights.size >= 32) throw new Error('image_cache_busy')
      onAccess?.('image_source_read')
      this.stats.misses++
      const controller = new AbortController()
      const current: Flight = {
        adapter,
        sceneId,
        controller,
        waiters: new Set(),
        promise: Promise.resolve(Buffer.alloc(0)),
      }
      current.promise = Promise.resolve()
        .then(() => load(controller.signal))
        .then((bytes) => {
          if (!bytes.length || bytes.length > 10 * 1024 * 1024) throw new Error('media_size_limit')
          if (
            controller.signal.aborted ||
            this.flights.get(key) !== current ||
            ![...current.waiters].some((waiter) => !waiter.signal.aborted && waiter.available())
          )
            throw new Error('image_source_unavailable')
          this.entries.set(key, {
            adapter,
            sceneId,
            bytes: Buffer.from(bytes),
            expires: this.now() + 300000,
          })
          let total = [...this.entries.values()].reduce((sum, entry) => sum + entry.bytes.length, 0)
          while (this.entries.size > 24 || total > 32 * 1024 * 1024) {
            const first = this.entries.keys().next().value!
            total -= this.entries.get(first)!.bytes.length
            this.entries.delete(first)
          }
          return bytes
        })
        .finally(() => {
          if (this.flights.get(key) === current) this.flights.delete(key)
        })
      this.flights.set(key, current)
      flight = current
    } else {
      onAccess?.('image_bytes_shared')
      this.stats.coalesced++
    }
    const shared = flight
    const waiter = { signal, available }
    shared.waiters.add(waiter)
    try {
      const bytes = await new Promise<Buffer>((resolve, reject) => {
        const abort = () => reject(new Error('image_context_cancelled'))
        signal.addEventListener('abort', abort, { once: true })
        if (signal.aborted) abort()
        shared.promise
          .then(resolve, reject)
          .finally(() => signal.removeEventListener('abort', abort))
          .catch(() => {})
      })
      if (!valid()) throw new Error('image_source_unavailable')
      return Buffer.from(bytes)
    } finally {
      shared.waiters.delete(waiter)
      if (!shared.waiters.size) shared.controller.abort()
    }
  }
}
