import { createHash } from 'node:crypto'
import type { Runtime, RuntimeAdapter } from '../runtime.ts'
import type { Store } from '../store.ts'
import type { Message, SceneRef, SendResult, HistoryResult } from '../onebot.ts'

export interface AdapterContext {
  id: string
  signal: AbortSignal
  scene(accountId: string, kind: 'group' | 'private', platformSceneId: string): SceneRef
  messageId(scene: SceneRef, platformMessageId: string): string
  receive(message: Message, scene: SceneRef, isBot: boolean): void
  recall(scene: SceneRef, messageId: string): void
  isRecalled(scene: SceneRef, messageId: string): boolean
  status(value: 'connecting' | 'connected' | 'disconnected'): void
  media(assetId: string, scene: SceneRef): { file: string; type: string } | undefined
}
export type AdapterFactory = (context: AdapterContext) => RuntimeAdapter & {
  start?(): Promise<void>
}
export interface AdapterServices {
  adapterStopped?(id: string): void
  media?(assetId: string, sceneId: string): { file: string; type: string } | undefined
}
const hash = (value: string) => createHash('sha256').update(value).digest('hex')
export function untilAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) {
    void promise.catch(() => {})
    return Promise.reject(new Error('plugin_disabled'))
  }
  return new Promise((resolve, reject) => {
    const abort = () => reject(new Error('plugin_disabled'))
    signal.addEventListener('abort', abort, { once: true })
    promise.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
  })
}

/** Trusted transport code receives scoped ingress and media access, never a direct core-send bypass. */
export function installAdapter(
  id: string,
  factory: AdapterFactory,
  runtime: Runtime,
  store: Store,
  signal: AbortSignal,
  services: AdapterServices,
) {
  let installed = false,
    disposed = false
  let status: 'connecting' | 'connected' | 'disconnected' = 'disconnected'
  const scene = (
    accountId: string,
    kind: 'group' | 'private',
    platformSceneId: string,
  ): SceneRef => {
    if (!accountId || !platformSceneId || !['group', 'private'].includes(kind))
      throw new Error('invalid_adapter_scene')
    return {
      id: `${id}:${hash(JSON.stringify([accountId, kind, platformSceneId]))}`,
      adapterId: id,
      accountId,
      kind,
      platformSceneId,
    }
  }
  const checkScene = (value: SceneRef) => {
    if (
      value.adapterId !== id ||
      value.id !== scene(value.accountId, value.kind, value.platformSceneId).id
    )
      throw new Error('adapter_scene_mismatch')
  }
  const checkMessage = (message: Message, scope: SceneRef) => {
    checkScene(scope)
    if (
      message.sceneId !== scope.id ||
      !message.id.startsWith(scope.id + ':') ||
      (message.replyToId !== undefined && !message.replyToId.startsWith(scope.id + ':')) ||
      !message.senderId ||
      !['live', 'history'].includes(message.origin) ||
      !['available', 'recalled', 'unavailable'].includes(message.status) ||
      !Number.isFinite(Date.parse(message.sentAt)) ||
      !Number.isFinite(Date.parse(message.receivedAt)) ||
      !Array.isArray(message.parts) ||
      message.parts.length > 64 ||
      message.parts.some(
        (part) =>
          !part ||
          (part.type === 'text'
            ? typeof part.text !== 'string' || part.text.length > 12000
            : part.type === 'mention'
              ? typeof part.userId !== 'string'
              : part.type === 'media'
                ? typeof part.assetId !== 'string' || typeof part.mediaType !== 'string'
                : part.type === 'unsupported'
                  ? typeof part.description !== 'string'
                  : true),
      )
    )
      throw new Error('invalid_adapter_message')
    const value: Message = {
      id: message.id,
      sceneId: message.sceneId,
      senderId: message.senderId,
      sentAt: message.sentAt,
      receivedAt: message.receivedAt,
      origin: message.origin,
      status: message.status,
      ...(message.replyToId ? { replyToId: message.replyToId } : {}),
      parts: message.parts.map((part) =>
        part.type === 'text'
          ? { type: 'text', text: part.text }
          : part.type === 'mention'
            ? { type: 'mention', userId: part.userId }
            : part.type === 'media'
              ? { type: 'media', assetId: part.assetId, mediaType: part.mediaType }
              : { type: 'unsupported', description: part.description },
      ),
    }
    if (store.recalled(scope.id, message.id) || value.status !== 'available') {
      value.parts = []
      value.status = store.recalled(scope.id, message.id) ? 'recalled' : value.status
    }
    return value
  }
  const active = () => installed && !disposed && !signal.aborted
  const adapter = factory({
    id,
    signal,
    scene,
    messageId: (scope, raw) => {
      checkScene(scope)
      return `${scope.id}:${hash(raw)}`
    },
    receive: (message, scope, isBot) => {
      if (!active()) return
      if (typeof isBot !== 'boolean') throw new Error('adapter_bot_flag_required')
      const normalized = checkMessage(message, scope)
      if (normalized.origin !== 'live') throw new Error('adapter_ingress_requires_live_origin')
      runtime.ingest(
        isBot ? { ...normalized, senderId: 'bot' } : normalized,
        Object.freeze({ ...scope }),
      )
    },
    recall: (scope, messageId) => {
      if (!active()) return
      checkScene(scope)
      if (!messageId.startsWith(scope.id + ':')) throw new Error('adapter_message_scope_mismatch')
      runtime.recall(scope.id, messageId)
    },
    isRecalled: (scope, messageId) => {
      if (!active()) throw new Error('plugin_disabled')
      checkScene(scope)
      return store.recalled(scope.id, messageId)
    },
    status: (value) => {
      if (
        !signal.aborted &&
        !disposed &&
        ['connecting', 'connected', 'disconnected'].includes(value)
      )
        status = value
    },
    media: (assetId, scope) => {
      checkScene(scope)
      if (!active()) throw new Error('plugin_disabled')
      return services.media?.(assetId, scope.id)
    },
  })
  const linked = (other?: AbortSignal) => (other ? AbortSignal.any([signal, other]) : signal)
  const read = async (
    scope: SceneRef,
    action: (signal: AbortSignal) => Promise<HistoryResult>,
    other?: AbortSignal,
  ) => {
    checkScene(scope)
    const combined = linked(other)
    if (!active() || combined.aborted) throw new Error('plugin_disabled')
    const value = await untilAbort(action(combined), combined)
    if (!active() || combined.aborted) throw new Error('plugin_disabled')
    if (
      !['complete', 'partial', 'unavailable'].includes(value.coverage) ||
      !Array.isArray(value.items) ||
      value.items.length > 1000
    )
      throw new Error('invalid_adapter_history')
    return {
      ...value,
      items: value.items
        .map((message) => ({ ...checkMessage(message, scope), origin: 'history' as const }))
        .filter((message) => message.status !== 'recalled'),
    }
  }
  runtime.addAdapter(id, {
    get status() {
      return status
    },
    send: async (scope, segment, other): Promise<SendResult> => {
      checkScene(scope)
      if (segment.replyToId && !segment.replyToId.startsWith(scope.id + ':'))
        return { status: 'failed', detail: 'quote_scope_mismatch' }
      const combined = linked(other)
      if (!active() || combined.aborted)
        return { status: 'cancelled', detail: 'cancelled_before_send' }
      try {
        const result = await untilAbort(
          adapter.send(Object.freeze({ ...scope }), structuredClone(segment), combined),
          combined,
        )
        if (
          !['sent', 'failed', 'unknown', 'cancelled'].includes(result.status) ||
          (result.messageId && !result.messageId.startsWith(scope.id + ':'))
        )
          return { status: 'unknown', detail: 'invalid_send_receipt' }
        return {
          status: result.status,
          ...(result.messageId ? { messageId: result.messageId } : {}),
        }
      } catch {
        return { status: 'unknown', detail: 'adapter_send_outcome_unknown' }
      }
    },
    getMessage: (scope, messageId, other) => {
      if (!messageId.startsWith(scope.id + ':'))
        return Promise.reject(new Error('adapter_message_scope_mismatch'))
      return read(
        scope,
        (signal) => adapter.getMessage(Object.freeze({ ...scope }), messageId, signal),
        other,
      ).then((result) => ({
        ...result,
        items: result.items.filter((message) => message.id === messageId),
      }))
    },
    history: (scope, input, other) =>
      read(
        scope,
        (signal) => adapter.history(Object.freeze({ ...scope }), structuredClone(input), signal),
        other,
      ),
    dispose: () => {
      if (disposed) return
      disposed = true
      installed = false
      status = 'disconnected'
      adapter.dispose()
    },
  })
  installed = true
  return {
    start: () =>
      untilAbort(
        Promise.resolve().then(() => {
          if (!active()) throw new Error('plugin_disabled')
          return adapter.start?.()
        }),
        signal,
      ),
    dispose: () => {
      try {
        runtime.removeAdapter(id)
      } finally {
        services.adapterStopped?.(id)
      }
    },
  }
}
