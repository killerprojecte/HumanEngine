import { createHash } from 'node:crypto'
import sharp from 'sharp'
import { WorkScheduler } from './scheduler.ts'
import type { Runtime, RuntimeMessage } from './runtime.ts'
import type { MessagePart } from './onebot.ts'

export const imagePart = (part: MessagePart) =>
  part.type === 'media' &&
  (part.mediaType === 'image' || part.mediaType.toLowerCase().startsWith('image/'))
function platformMetadata(part: MessagePart): { kind: string } | undefined {
  if (
    part.type !== 'unsupported' ||
    !part.description.startsWith('Untrusted platform image metadata: ')
  )
    return
  try {
    const value = JSON.parse(part.description.slice('Untrusted platform image metadata: '.length))
    if (
      value &&
      typeof value === 'object' &&
      ['image', 'mface', 'sticker', 'emoji'].includes(value.kind)
    )
      return value
  } catch {
    /* Old or unfamiliar metadata stays a non-image part; never rewrite user text. */
  }
}

type ChatPart =
  | Exclude<MessagePart, { type: 'media' }>
  | { type: 'media'; mediaType: string; mediaUrl?: string; mediaKind?: 'image' | 'sticker' }

/** API-only projection. LLM/runtime messages retain their original structured source parts. */
export function presentChatMessage(message: RuntimeMessage) {
  const available = message.status === 'available' && message.origin !== 'simulation'
  return {
    ...message,
    parts: message.parts.flatMap<ChatPart>((part, partIndex) => {
      if (platformMetadata(part)) return []
      if (part.type !== 'media') return [part]
      const { assetId: _privateReference, ...publicPart } = part
      if (!available || !imagePart(part)) return [publicPart]
      const metadata = platformMetadata(message.parts[partIndex + 1] ?? { type: 'text', text: '' })
      return [
        {
          ...publicPart,
          mediaUrl: `/api/scenes/${encodeURIComponent(message.sceneId)}/messages/${encodeURIComponent(message.id)}/media/${partIndex}`,
          mediaKind:
            metadata && metadata.kind !== 'image' ? ('sticker' as const) : ('image' as const),
        },
      ]
    }),
  }
}

export class ChatMediaError extends Error {
  constructor(
    public readonly status: 404 | 415 | 503,
    public readonly code: string,
  ) {
    super(code)
  }
}
export async function validateDisplayImage(bytes: Buffer): Promise<string> {
  if (!bytes.length || bytes.length > 10 * 1024 * 1024)
    throw new ChatMediaError(415, 'image_size_limit')
  try {
    const image = sharp(bytes, { limitInputPixels: 40_000_000, failOn: 'warning' })
    const metadata = await image.metadata()
    if (
      !['png', 'jpeg', 'gif', 'webp'].includes(metadata.format ?? '') ||
      !metadata.width ||
      !metadata.height ||
      (metadata.pages ?? 1) > 200 ||
      metadata.width * metadata.height * (metadata.pages ?? 1) > 40_000_000
    )
      throw new Error('invalid_image')
    // Decode the first frame as well as inspecting the header. Original animation bytes are retained.
    await image.stats()
    return `image/${metadata.format}`
  } catch {
    throw new ChatMediaError(415, 'invalid_or_unsupported_image')
  }
}

/** Auth is enforced by the API gateway; references are resolved from scene-bound messages only. */
export class ChatMediaService {
  private readonly work = new WorkScheduler(4, 32)
  private readonly cache = new Map<string, { bytes: Buffer; mime: string; until: number }>()
  constructor(
    private readonly runtime: Runtime,
    private readonly now = Date.now,
    private readonly managed?: (
      sceneId: string,
      assetId: string,
    ) => {
      available: () => boolean
      read: () => Promise<Buffer>
    },
  ) {}
  async read(sceneId: string, messageId: string, partIndex: number, signal: AbortSignal) {
    return this.work.run(1, signal, async () => {
      const source = await this.runtime.resolveDisplayImage(
        sceneId,
        messageId,
        partIndex,
        signal,
        this.managed ? (assetId) => this.managed!(sceneId, assetId) : undefined,
      )
      if (!source) throw new ChatMediaError(404, 'image_unavailable')
      const valid = () => {
        if (signal.aborted || !source.available())
          throw new ChatMediaError(404, 'image_unavailable')
      }
      valid()
      const key = createHash('sha256')
        .update(JSON.stringify([sceneId, messageId, partIndex, source.assetId]))
        .digest('hex')
      for (const [id, entry] of this.cache) if (entry.until <= this.now()) this.cache.delete(id)
      const cached = this.cache.get(key)
      if (cached) {
        valid()
        return { bytes: cached.bytes, mime: cached.mime }
      }
      let bytes: Buffer
      try {
        bytes = await source.read()
      } catch {
        valid()
        throw new ChatMediaError(503, 'image_fetch_unavailable')
      }
      valid()
      const mime = await validateDisplayImage(bytes)
      valid()
      this.cache.set(key, { bytes, mime, until: this.now() + 60_000 })
      let total = [...this.cache.values()].reduce((sum, item) => sum + item.bytes.length, 0)
      while (this.cache.size > 24 || total > 32 * 1024 * 1024) {
        const oldest = this.cache.keys().next().value!
        total -= this.cache.get(oldest)!.bytes.length
        this.cache.delete(oldest)
      }
      return { bytes, mime }
    })
  }
}
