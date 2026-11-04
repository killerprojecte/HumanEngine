import { createHash } from 'node:crypto'

type Entry = { id: string; hash: string }
type Prefix = { authority: string; entries: Entry[] }
type State = { generation: symbol; successful?: Prefix }
const hash = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex')

/** Hints only: retain identities and hashes, never payloads or permission decisions.
 * A new preparation invalidates older completion tickets for this isolated key.
 */
export class PromptCacheBoundaries {
  private readonly states = new Map<string, State>()
  clear() {
    this.states.clear()
  }
  delete(key: string) {
    this.states.delete(key)
  }
  prepare(key: string, current: readonly { id: string; value: unknown }[], authority: unknown) {
    const signature = hash(authority)
    const entries = current.map((entry) => ({ id: entry.id, hash: hash(entry.value) }))
    if (new Set(entries.map((entry) => entry.id)).size !== entries.length)
      throw new Error('prompt_context_duplicate_message')
    const previous = this.states.get(key)?.successful
    const matches =
      previous?.authority === signature &&
      previous.entries.length > 0 &&
      previous.entries.length <= entries.length &&
      previous.entries.every(
        (entry, index) => entry.id === entries[index]?.id && entry.hash === entries[index]?.hash,
      )
    const contextCacheBoundaries = [
      ...new Set([
        ...(matches ? [previous!.entries.length - 1] : []),
        ...(entries.length ? [entries.length - 1] : []),
      ]),
    ]
    const state: State = { generation: Symbol(), successful: matches ? previous : undefined }
    this.states.delete(key)
    this.states.set(key, state)
    while (this.states.size > 256) this.states.delete(this.states.keys().next().value!)
    return {
      contextCacheBoundaries,
      commit: () => {
        if (this.states.get(key)?.generation !== state.generation) return
        state.successful = { authority: signature, entries }
      },
    }
  }
}
