import {
  OneBotError,
  object,
  rawId,
  rawIdOrUrl,
  encode,
  time,
  type Obj,
  type Message,
  type SceneRef,
} from './protocol.ts'

interface NormalizationContext {
  accountId: string
  scene: (kind: 'group' | 'private', peer: string) => SceneRef
  messageId: (scene: SceneRef, platformId: string) => string
  userId: (platformId: string) => string
  isRecalled: (sceneId: string, messageId: string) => boolean
}

/** Normalize platform messages without network access or cross-scene fallback. */
export function normalizeMessage(
  context: NormalizationContext,
  event: Obj,
  origin: Message['origin'],
  expected?: SceneRef,
): { message: Message; scene: SceneRef } {
  if (event.self_id !== undefined && rawId(event.self_id) !== context.accountId)
    throw new OneBotError('account_mismatch')
  if (!['group', 'private'].includes(String(event.message_type)))
    throw new OneBotError('unsupported_scene_type')
  const kind = event.message_type as 'group' | 'private'
  if (kind === 'private' && event.sub_type === 'group')
    throw new OneBotError('temporary_private_session_unsupported')
  const sender = rawId(event.user_id ?? object(event.sender).user_id)
  const peer =
    kind === 'group'
      ? rawId(event.group_id)
      : sender === context.accountId
        ? rawId(event.target_id)
        : sender
  const scene = context.scene(kind, peer)
  if (expected && scene.id !== expected.id) throw new OneBotError('scene_mismatch')
  const id = context.messageId(scene, rawId(event.message_id))
  const message: Message = {
    id,
    sceneId: scene.id,
    senderId: context.userId(sender),
    sentAt: time(event.time),
    receivedAt: new Date().toISOString(),
    origin,
    parts: [],
    status: 'available',
  }
  if (context.isRecalled(scene.id, id)) {
    message.status = 'recalled'
    return { message, scene }
  }
  if (!Array.isArray(event.message)) {
    message.parts = [{ type: 'unsupported', description: 'Configure OneBot array message format' }]
    return { message, scene }
  }
  for (const item of event.message) {
    const segment = object(item)
    const data = object(segment.data)
    if (segment.type === 'text' && typeof data.text === 'string')
      message.parts.push({ type: 'text', text: data.text })
    else if (segment.type === 'at')
      message.parts.push({
        type: 'mention',
        userId: data.qq === 'all' ? 'all' : context.userId(rawId(data.qq)),
      })
    else if (segment.type === 'reply') message.replyToId = context.messageId(scene, rawId(data.id))
    else if (['image', 'record', 'video'].includes(String(segment.type)))
      message.parts.push({
        type: 'media',
        assetId: `ob:media:${encode({ sceneId: scene.id, type: segment.type, file: rawIdOrUrl(data.url ?? data.file) })}`,
        mediaType:
          segment.type === 'image'
            ? 'image/unknown'
            : segment.type === 'record'
              ? 'audio/unknown'
              : 'video/unknown',
      })
    else
      message.parts.push({
        type: 'unsupported',
        description: `OneBot segment: ${String(segment.type).slice(0, 40)}`,
      })
  }
  return { message, scene }
}
