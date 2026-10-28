/** Per-turn, capability-like references. Only trusted runtime data may register IDs. */
type Kind = 'u' | 'm' | 'a' | 'e'
const omitted = new Set([
  'receivedAt',
  'senderAvatarUrl',
  'avatarUrl',
  'senderPlatformId',
  'platformMetadata',
  'raw',
  'rawEvent',
  'accountId',
  'adapterId',
  'platformSceneId',
])
const messageKeys = new Set([
  'ids',
  'messageId',
  'messageIds',
  'targetMessageIds',
  'contextMessageIds',
  'sourceMessageId',
  'sourceMessageIds',
  'replyToId',
  'originalTarget',
])
const userKeys = new Set(['senderId', 'userId', 'personId', 'botUserId', 'recipientId'])
const assetKeys = new Set(['assetId', 'assetIds'])
const evidenceKeys = new Set([
  'evidenceId',
  'evidenceIds',
  'memoryIds',
  'memoryId',
  'candidateId',
  'habitId',
  'sourceId',
  'supersedesId',
  'mergeIntoId',
  'targetId',
  'cursor',
  'nextCursor',
])
const short = (id: string) => /^[\w.-]{1,32}$/.test(id)
function decode(value: string): unknown {
  try {
    return JSON.parse(Buffer.from(value, 'base64url').toString('utf8'))
  } catch {
    return undefined
  }
}
export class ContextProjection {
  private readonly forward = new Map<string, string>()
  private readonly reverse = new Map<string, string>()
  private readonly counts = { u: 0, m: 0, a: 0, e: 0 }
  constructor(readonly sceneId: string) {}
  private key(kind: Kind, value: string) {
    return `${kind}:${value}`
  }
  private register(kind: Kind, canonical: string): string {
    const key = this.key(kind, canonical)
    const existing = this.forward.get(key)
    if (existing) return existing
    let candidate: unknown = canonical
    if (kind === 'm' && canonical.startsWith('ob:s:')) {
      const prefix = `${this.sceneId}:m:`
      if (!canonical.startsWith(prefix)) throw new Error('context_reference_cross_scene')
      candidate = decode(canonical.slice(prefix.length))
    } else if (kind === 'u' && canonical.startsWith('ob:u:')) {
      const decoded = decode(canonical.slice(5))
      candidate = Array.isArray(decoded) && decoded.length === 3 ? decoded[2] : undefined
    }
    let ref = kind !== 'a' && typeof candidate === 'string' && short(candidate) ? candidate : ''
    if (!ref || this.reverse.has(this.key(kind, ref))) {
      do {
        ref = `${kind}${++this.counts[kind]}`
      } while (this.reverse.has(this.key(kind, ref)))
    }
    this.forward.set(key, ref)
    this.reverse.set(this.key(kind, ref), canonical)
    return ref
  }
  private restore(kind: Kind, ref: string): string {
    const value = this.reverse.get(this.key(kind, ref))
    if (value === undefined) throw new Error('context_reference_unknown')
    return value
  }
  /** No string replacement: text, queries, descriptions and free-form bodies stay byte-for-byte. */
  private walk(
    value: unknown,
    inbound: boolean,
    field = '',
    parent?: Record<string, unknown>,
    idKind?: Kind,
  ): unknown {
    const kind = messageKeys.has(field)
      ? 'm'
      : userKeys.has(field)
        ? 'u'
        : assetKeys.has(field)
          ? 'a'
          : evidenceKeys.has(field)
            ? 'e'
            : field === 'id'
              ? parent && 'parts' in parent && 'senderId' in parent
                ? 'm'
                : parent && 'format' in parent && 'tags' in parent
                  ? 'a'
                  : (idKind ?? 'e')
              : undefined
    if (typeof value === 'string') {
      if (field === 'topicId') {
        if (!inbound) return this.register('e', value)
        const known = this.reverse.get(this.key('e', value))
        if (known) return known
        // Participation may introduce a new short topic label, not a canonical ID.
        if (short(value)) return value
        throw new Error('context_reference_unknown')
      }
      if (field === 'sceneId') {
        if (inbound) {
          if (value !== 'scene') throw new Error('context_reference_cross_scene')
          return this.sceneId
        }
        if (value !== this.sceneId) throw new Error('context_reference_cross_scene')
        return 'scene'
      }
      return kind ? (inbound ? this.restore(kind, value) : this.register(kind, value)) : value
    }
    if (Array.isArray(value))
      return value.map((item) => this.walk(item, inbound, field, parent, idKind))
    if (!value || typeof value !== 'object') return value
    const object = value as Record<string, unknown>
    // Check scope before omitting repetitive per-message scene metadata.
    if (!inbound && typeof object.sceneId === 'string' && object.sceneId !== this.sceneId)
      throw new Error('context_reference_cross_scene')
    return Object.fromEntries(
      Object.entries(object)
        .filter(
          ([key]) =>
            inbound ||
            (!omitted.has(key) &&
              !(key === 'sceneId' && 'parts' in object && 'senderId' in object)),
        )
        .map(([key, entry]) => [
          key,
          this.walk(
            entry,
            inbound,
            key,
            object,
            key === 'value' && object.toolName === 'memes.search'
              ? 'a'
              : key === 'value' &&
                  ['messages.get', 'messages.around', 'messages.byUser'].includes(
                    String(object.toolName),
                  )
                ? 'm'
                : idKind,
          ),
        ]),
    )
  }
  project<T>(value: T): T {
    return this.walk(value, false) as T
  }
  restoreArguments<T>(value: T): T {
    return this.walk(value, true) as T
  }
  stringify(value: unknown): string {
    return JSON.stringify(this.project(value))
  }
}
