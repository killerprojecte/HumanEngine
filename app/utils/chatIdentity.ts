/** Names are presentation only. Never decode a canonical ID into identity metadata. */
export function humanName(value: string | undefined): string {
  const name = value?.replace(/[\u0000-\u001f\u007f]/g, '').trim() ?? ''
  return !name || /^(?:ob:|plugin:|\d{5,}$)/i.test(name) ? '' : name
}
export function avatarSource(value: string | undefined): string | undefined {
  if (!value) return undefined
  if (/^\/(?!\/)/.test(value)) return value
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined
  } catch {
    return undefined
  }
}
export function identityColor(id: string): string {
  let hash = 0
  for (const character of id) hash = (hash * 31 + character.charCodeAt(0)) | 0
  return ['#477b77', '#8066a4', '#a77446', '#5279a8', '#a35f7d'][Math.abs(hash) % 5]!
}

/** Presentation labels describe available transport facts, not successful visual recognition. */
export function mediaLabelKey(part: { mediaType?: string; description?: string }): string {
  const mime = part.mediaType?.trim().toLowerCase() ?? ''
  if (/^(?:sticker|mface|emoji)$/.test(mime)) return 'cwMediaSticker'
  if (mime.startsWith('image/')) return 'cwMediaImage'
  if (mime.startsWith('audio/')) return 'cwMediaAudio'
  if (mime.startsWith('video/')) return 'cwMediaVideo'
  const description = part.description ?? ''
  if (description.startsWith('Image unavailable:')) return 'cwMediaUnavailable'
  if (description.startsWith('Native QQ face metadata (untrusted):')) return 'cwMediaFace'
  const prefix = 'Untrusted platform image metadata: '
  if (description.startsWith(prefix)) {
    try {
      const metadata: unknown = JSON.parse(description.slice(prefix.length))
      if (metadata && typeof metadata === 'object' && 'kind' in metadata) {
        if (['sticker', 'mface', 'emoji'].includes(String(metadata.kind))) return 'cwMediaSticker'
        if (metadata.kind === 'image') return 'cwMediaImageMetadata'
      }
    } catch {
      // Malformed platform hints must not become claims about media contents.
    }
  }
  return 'cwMediaUnknown'
}
