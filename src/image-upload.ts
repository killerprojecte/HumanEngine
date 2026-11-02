import sharp, { type Metadata } from 'sharp'

// Local resource/compatibility policy, not a claim about every compatible API.
export const MAX_IMAGE_SOURCE_BYTES = 64 * 1024 * 1024
export const DEFAULT_IMAGE_UPLOAD_LIMITS = {
  maxImageBytes: 10 * 1024 * 1024,
  maxWidth: 1536,
  maxHeight: 1536,
}
export interface ImageUploadLimits {
  maxImageBytes?: number
  maxWidth?: number
  maxHeight?: number
  /** Entire serialized native JSON body, including text, tools and base64 overhead. */
  maxRequestBytes?: number
}
const options = { pages: 1, limitInputPixels: 40_000_000, failOn: 'warning' as const }
const dataUrl = (bytes: Buffer, format: string) =>
  `data:image/${format};base64,${bytes.toString('base64')}`

/** Never modifies the source. Animation reduction is opt-in for the first-frame vision path. */
export async function compressImage(
  bytes: Buffer,
  limits: ImageUploadLimits,
  firstFrame = false,
  signal?: AbortSignal,
): Promise<string> {
  signal?.throwIfAborted()
  if (!bytes.length || bytes.length > MAX_IMAGE_SOURCE_BYTES) throw new Error('media_size_limit')
  const maxBytes = limits.maxImageBytes ?? DEFAULT_IMAGE_UPLOAD_LIMITS.maxImageBytes
  const maxWidth = limits.maxWidth ?? DEFAULT_IMAGE_UPLOAD_LIMITS.maxWidth
  const maxHeight = limits.maxHeight ?? DEFAULT_IMAGE_UPLOAD_LIMITS.maxHeight
  for (const value of [maxBytes, maxWidth, maxHeight])
    if (!Number.isSafeInteger(value) || value < 1) throw new Error('invalid_image_upload_limits')
  let metadata: Metadata
  try {
    metadata = await sharp(bytes, options).metadata()
    if (
      !['png', 'jpeg', 'gif', 'webp'].includes(metadata.format ?? '') ||
      !metadata.width ||
      !metadata.height
    )
      throw new Error('invalid_image')
    // Decode even an otherwise acceptable header. Pixel limits apply before allocation.
    await sharp(bytes, options).stats()
  } catch {
    throw new Error('invalid_or_unsupported_image')
  }
  const animated = (metadata.pages ?? 1) > 1
  const rotated = (metadata.orientation ?? 1) >= 5
  const sourceWidth = rotated ? (metadata.pageHeight ?? metadata.height!) : metadata.width!
  const height = rotated ? metadata.width! : (metadata.pageHeight ?? metadata.height!)
  if (
    (!animated || !firstFrame) &&
    bytes.length <= maxBytes &&
    sourceWidth <= maxWidth &&
    height <= maxHeight
  )
    return dataUrl(bytes, metadata.format!)
  if (animated && !firstFrame) throw new Error('animated_image_compression_requires_first_frame')
  let scale = Math.min(1, maxWidth / sourceWidth, maxHeight / height)
  // Lossless stripping/resizing first; preserve transparency rather than flatten onto a guessed background.
  while (true) {
    signal?.throwIfAborted()
    const width = Math.max(1, Math.floor(sourceWidth * scale))
    const targetHeight = Math.max(1, Math.floor(height * scale))
    const pipeline = () =>
      sharp(bytes, options)
        .rotate()
        .resize({ width, height: targetHeight, fit: 'inside', withoutEnlargement: true })
    const png = await pipeline().png({ compressionLevel: 9 }).toBuffer()
    if (png.length <= maxBytes) return dataUrl(png, 'png')
    if (!metadata.hasAlpha) {
      let low = 40,
        high = 92
      let best: Buffer | undefined
      while (low <= high) {
        signal?.throwIfAborted()
        const quality = Math.floor((low + high) / 2)
        const jpeg = await pipeline().jpeg({ quality, mozjpeg: true }).toBuffer()
        if (jpeg.length <= maxBytes) {
          best = jpeg
          low = quality + 1
        } else high = quality - 1
      }
      if (best) return dataUrl(best, 'jpeg')
    }
    if (width === 1 && targetHeight === 1) break
    scale *= 0.7
  }
  throw new Error('image_upload_limit_unachievable')
}

export function hasNativeInlineImages(body: unknown): boolean {
  if (!body || typeof body !== 'object') return false
  const root = body as Record<string, unknown>
  const messages = Array.isArray(root.messages)
    ? root.messages
    : Array.isArray(root.input)
      ? root.input
      : []
  return messages.some(
    (message) =>
      message &&
      Array.isArray(message.content) &&
      message.content.some((part: Record<string, unknown>) => {
        const url =
          part.type === 'input_image'
            ? part.image_url
            : part.type === 'image_url' && part.image_url && typeof part.image_url === 'object'
              ? (part.image_url as Record<string, unknown>).url
              : undefined
        return typeof url === 'string' && url.startsWith('data:image/')
      }),
  )
}

/** Operate on native image fields only, never strings embedded in tool arguments/results. */
export async function prepareImageRequest(
  body: unknown,
  configured?: ImageUploadLimits,
  signal?: AbortSignal,
): Promise<string> {
  const copy = structuredClone(body) as Record<string, unknown>
  const slots: Array<{ get: () => string; set: (value: string) => void }> = []
  for (const message of (Array.isArray(copy.messages)
    ? copy.messages
    : Array.isArray(copy.input)
      ? copy.input
      : []) as Array<Record<string, unknown>>) {
    if (!Array.isArray(message.content)) continue
    for (const part of message.content as Array<Record<string, unknown>>) {
      if (part.type === 'input_image' && typeof part.image_url === 'string')
        slots.push({
          get: () => part.image_url as string,
          set: (value) => {
            part.image_url = value
          },
        })
      if (part.type === 'image_url' && part.image_url && typeof part.image_url === 'object') {
        const image = part.image_url as Record<string, unknown>
        if (typeof image.url === 'string')
          slots.push({
            get: () => image.url as string,
            set: (value) => {
              image.url = value
            },
          })
      }
    }
  }
  const inline = slots.filter((slot) => slot.get().startsWith('data:'))
  const limits = { ...DEFAULT_IMAGE_UPLOAD_LIMITS, ...configured }
  const decode = (url: string) => {
    const match = /^data:image\/(png|jpeg|gif|webp);base64,([A-Za-z0-9+/]*={0,2})$/.exec(url)
    if (
      !match ||
      !match[2] ||
      match[2].length % 4 ||
      match[2].length > Math.ceil(MAX_IMAGE_SOURCE_BYTES / 3) * 4
    )
      throw new Error('invalid_image_data')
    return Buffer.from(match[2], 'base64')
  }
  for (const slot of inline) {
    // Existing integrations may use opaque data handled by their provider. Only inspect on configured
    // admission, or when the payload exceeds the established local image byte budget.
    if (configured || slot.get().length > Math.ceil(limits.maxImageBytes / 3) * 4 + 32)
      slot.set(await compressImage(decode(slot.get()), limits, false, signal))
  }
  let serialized = JSON.stringify(copy)
  if (limits.maxRequestBytes !== undefined) {
    if (!Number.isSafeInteger(limits.maxRequestBytes) || limits.maxRequestBytes < 1)
      throw new Error('invalid_image_upload_limits')
    if (Buffer.byteLength(serialized) > limits.maxRequestBytes && inline.length) {
      // Replacing data URL values with empty strings measures exact fixed JSON/text/tool overhead.
      const originals = inline.map((slot) => slot.get())
      inline.forEach((slot) => slot.set(''))
      const fixed = Buffer.byteLength(JSON.stringify(copy))
      const perImageUrl = Math.floor((limits.maxRequestBytes - fixed) / inline.length)
      // JPEG is the longest supported data-URL prefix (23 ASCII bytes).
      const target = Math.min(limits.maxImageBytes, Math.floor((perImageUrl - 23) / 4) * 3)
      if (target < 1) throw new Error('image_request_size_limit')
      for (let index = 0; index < inline.length; index++)
        inline[index]!.set(
          await compressImage(
            decode(originals[index]!),
            { ...limits, maxImageBytes: target },
            false,
            signal,
          ),
        )
      serialized = JSON.stringify(copy)
    }
    if (Buffer.byteLength(serialized) > limits.maxRequestBytes)
      throw new Error('image_request_size_limit')
  }
  return serialized
}
