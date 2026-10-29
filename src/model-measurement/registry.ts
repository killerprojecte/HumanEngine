import type { TokenizerTarget, TrustedTargetTokenizer } from './contracts.ts'
const key = (target: TokenizerTarget) =>
  JSON.stringify([target.providerId, target.protocol, target.apiModelName, target.codecId])
const label = (value: string) => typeof value === 'string' && /^[a-zA-Z0-9._-]{1,128}$/.test(value)
/** Server-code injection only. No config/UI registration and no default provider tokenizer. */
export class TargetTokenizerRegistry {
  private readonly entries = new Map<string, TrustedTargetTokenizer>()
  register(input: TrustedTargetTokenizer): () => void {
    if (
      !label(input.id) ||
      !label(input.revision) ||
      typeof input.count !== 'function' ||
      Object.values(input.target).some(
        (value) => typeof value !== 'string' || !value || value.length > 512,
      )
    )
      throw Error('invalid_tokenizer_contract')
    const k = key(input.target)
    if (this.entries.has(k)) throw Error('duplicate_tokenizer_target')
    const entry = Object.freeze({ ...input, target: Object.freeze({ ...input.target }) })
    this.entries.set(k, entry)
    return () => {
      if (this.entries.get(k) === entry) this.entries.delete(k)
    }
  }
  lookup(target: TokenizerTarget): TrustedTargetTokenizer | undefined {
    return this.entries.get(key(target))
  }
}
