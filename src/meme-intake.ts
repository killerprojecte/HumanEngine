import type { RuntimeMessage } from './runtime.ts'

/** Platform classification only; never infer admission from arbitrary user text or image captions. */
export function isStickerSource(message: RuntimeMessage, assetId: string): boolean {
  return message.parts.some((part, index) => {
    if (part.type !== 'media' || part.assetId !== assetId) return false
    const metadata = message.parts[index + 1]
    const prefix = 'Untrusted platform image metadata: '
    if (metadata?.type !== 'unsupported' || !metadata.description.startsWith(prefix)) return false
    try {
      const value: unknown = JSON.parse(metadata.description.slice(prefix.length))
      if (!value || typeof value !== 'object' || !('kind' in value)) return false
      if (['mface', 'sticker', 'emoji'].includes(String(value.kind))) return true
      // Conservative subset of the upstream QQ adapter's emoji subtype classification.
      return (
        value.kind === 'image' && 'subtype' in value && ['1', '7'].includes(String(value.subtype))
      )
    } catch {
      return false
    }
  })
}
