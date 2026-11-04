import type { Store } from '../store.ts'
import { createHash } from 'node:crypto'

export interface PromptWindowOptions {
  enabled: boolean
  maxMessages: number
  retainMessages: number
  maxBytes: number
}
type Window = { signature: string; entries: Array<{ id: string; hash: string }> }
const hash = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex')

/** Membership only, never a cache of message bodies or permission decisions. Every
 * selection resolves against the caller's freshly authorized, ordered messages.
 * Synchronous updates cannot overwrite another turn with an asynchronous result.
 */
export class PromptContextWindows {
  private readonly windows = new Map<string, Window>()
  clear() {
    this.windows.clear()
  }
  delete(key: string) {
    this.windows.delete(key)
  }
  select<T extends { id: string }>(
    key: string,
    current: readonly T[],
    options: PromptWindowOptions,
    authority: unknown,
  ): T[] {
    if (!options.enabled) {
      this.windows.delete(key)
      return current.slice(-24)
    }
    if (!current.length) {
      this.windows.delete(key)
      return []
    }
    // Anything older than this suffix cannot survive a full window or its next
    // rollover. Bound hashing work as well as retained membership, including simulations.
    current = current.slice(-(options.maxMessages + 1))
    const signature = hash([options, authority])
    const entries = current.map((value) => ({ id: value.id, hash: hash(value) }))
    if (new Set(entries.map((value) => value.id)).size !== entries.length)
      throw new Error('prompt_context_duplicate_message')
    const previous = this.windows.get(key)
    let start =
      previous?.signature === signature && previous.entries.length
        ? entries.findIndex((entry) => entry.id === previous.entries[0]!.id)
        : -1
    if (
      start >= 0 &&
      previous!.entries.some(
        (entry, offset) =>
          entry.id !== entries[start + offset]?.id || entry.hash !== entries[start + offset]?.hash,
      )
    )
      start = -1
    let selected = start >= 0 ? current.slice(start) : current.slice(-options.retainMessages)
    const size = (values: readonly T[]) => Buffer.byteLength(JSON.stringify(values))
    if (selected.length > options.maxMessages || size(selected) > options.maxBytes)
      selected = current.slice(-Math.min(options.retainMessages, options.maxMessages))
    while (selected.length > 1 && size(selected) > options.maxBytes) selected.shift()
    if (size(selected) > options.maxBytes) {
      this.windows.delete(key)
      throw new Error('prompt_context_message_budget_exhausted')
    }
    // Refresh insertion order for bounded LRU membership; no content survives here.
    this.windows.delete(key)
    this.windows.set(key, {
      signature,
      entries: selected.map((value) => ({ id: value.id, hash: hash(value) })),
    })
    while (this.windows.size > 256) this.windows.delete(this.windows.keys().next().value!)
    return selected
  }
}

/** One live recovery gate and one SQLite snapshot for all prompt configuration.
 * This compares current raw rows, not cached authorization. Missing/changed rows
 * invalidate the turn even if their parsed defaults might happen to be equal.
 */
export function promptWindowAuthority(
  store: Pick<Store, 'db' | 'assertRecoveryReady'>,
  sceneId: string,
  tools: unknown,
): string {
  store.assertRecoveryReady()
  const rows = store.db
    .prepare(
      `
    SELECT 'config' AS kind, key AS id, value FROM config WHERE key IN ('settings','models','rules')
    UNION ALL SELECT 'scene' AS kind, id, value FROM scenes WHERE id=?
    ORDER BY kind,id
  `,
    )
    .all(sceneId)
  return JSON.stringify([rows, tools])
}
