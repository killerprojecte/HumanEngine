import { encode, rawIdOrUrl, type MessagePart, type Obj } from './protocol.ts'

const label = (value: unknown) =>
  typeof value === 'string'
    ? value
        .replace(/[\u0000-\u001f\u007f]/g, '')
        .trim()
        .slice(0, 200)
    : ''

/** Preserve platform hints as untrusted metadata, never as a substitute for image bytes. */
export function imageParts(sceneId: string, kind: string, data: Obj): MessagePart[] {
  const parts: MessagePart[] = []
  const inline =
    typeof data.base64 === 'string' && data.base64 ? `base64://${data.base64}` : undefined
  const inlineFile =
    typeof data.file === 'string' && /^(?:base64:\/\/|data:image\/)/i.test(data.file)
      ? data.file
      : undefined
  const source = inline || inlineFile || data.url || data.file || data.file_id
  try {
    const file = rawIdOrUrl(source)
    const fileId =
      typeof (data.file_id ?? data.file) === 'string' ? (data.file_id ?? data.file) : undefined
    parts.push({
      type: 'media',
      mediaType: 'image/unknown',
      assetId: `ob:media:${encode({ sceneId, type: 'image', file, ...(fileId && fileId !== file ? { fileId } : {}) })}`,
    })
  } catch {
    parts.push({
      type: 'unsupported',
      description: `Image unavailable: ${kind} has no usable media reference`,
    })
  }
  const summary = label(data.summary ?? data.name)
  const subtype =
    typeof data.sub_type === 'number' || typeof data.sub_type === 'string'
      ? label(String(data.sub_type))
      : ''
  if (kind !== 'image' || summary || subtype)
    parts.push({
      type: 'unsupported',
      description: `Untrusted platform image metadata: ${JSON.stringify({ kind, ...(summary ? { summary } : {}), ...(subtype ? { subtype } : {}) })}`,
    })
  return parts
}

export function facePart(data: Obj): MessagePart {
  const name = label(data.text ?? data.name ?? data.summary)
  const id = /^(?:\d{1,8})$/.test(String(data.id ?? '')) ? String(data.id) : undefined
  return {
    type: 'unsupported',
    description: `Native QQ face metadata (untrusted): ${JSON.stringify({ ...(id ? { id } : {}), ...(name ? { label: name } : { meaning: 'unavailable' }) })}`,
  }
}
