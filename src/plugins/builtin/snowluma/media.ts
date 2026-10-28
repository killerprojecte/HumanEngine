import { fetchQqMedia } from '../../../media-fetch.ts'
import { OneBotError, decode, object, type SceneRef } from './protocol.ts'

export type MediaFetcher = (url: string, signal?: AbortSignal) => Promise<Buffer>
const MAX_BYTES = 10 * 1024 * 1024
const inlineBytes = (value: string): Buffer | undefined => {
  const payload = value.startsWith('base64://')
    ? value.slice(9)
    : /^data:image\/(?:png|jpeg|gif|webp);base64,/i.test(value)
      ? value.slice(value.indexOf(',') + 1)
      : undefined
  if (payload === undefined) return
  if (payload.length > Math.ceil(MAX_BYTES / 3) * 4) throw new OneBotError('media_size_limit')
  if (
    !payload ||
    payload.length % 4 !== 0 ||
    /[^A-Za-z0-9+/=]/.test(payload) ||
    !/^[A-Za-z0-9+/]*={0,2}$/.test(payload)
  )
    throw new OneBotError('invalid_image_data')
  return Buffer.from(payload, 'base64')
}

/** Never read a host-local file or forward adapter credentials to a media URL. */
export async function readOneBotImage(
  scene: SceneRef,
  assetId: string,
  action: (name: string, params: Record<string, unknown>, signal?: AbortSignal) => Promise<unknown>,
  signal?: AbortSignal,
  fetchMedia: MediaFetcher = fetchQqMedia,
): Promise<Buffer> {
  if (signal?.aborted) throw new OneBotError('media_cancelled')
  if (!assetId.startsWith('ob:media:')) throw new OneBotError('invalid_media_reference')
  const media = object(decode(assetId.slice(9)))
  if (media.sceneId !== scene.id) throw new OneBotError('media_scope_mismatch')
  if (media.type !== 'image') throw new OneBotError('unsupported_media_type')
  const file = typeof media.file === 'string' ? media.file : ''
  const token = typeof media.fileId === 'string' ? media.fileId : file
  const download = async (source: string) => {
    if (signal?.aborted) throw new OneBotError('media_cancelled')
    const bytes = inlineBytes(source) ?? (await fetchMedia(source, signal))
    if (bytes.length > MAX_BYTES) throw new OneBotError('media_size_limit')
    if (!bytes.length) throw new OneBotError('invalid_image_data')
    return bytes
  }
  if (/^(?:https?:|base64:\/\/|data:)/i.test(file)) {
    try {
      return await download(file)
    } catch (error) {
      if (signal?.aborted || !token || token === file) throw error
      // Refresh an expired CDN URL once through the authenticated platform action.
    }
  }
  if (!token || token.length > 16384) throw new OneBotError('invalid_media_reference')
  const result = object(await action('get_image', { file: token }, signal))
  const source = typeof result.url === 'string' && result.url ? result.url : result.file
  if (typeof source !== 'string' || !/^(?:https?:|base64:\/\/|data:)/i.test(source))
    throw new OneBotError('image_download_url_unavailable')
  return download(source)
}
