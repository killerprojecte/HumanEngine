import { sanitizeError } from '../../../diagnostics/errors.ts'
import { readOneBotImage, type MediaFetcher } from './media.ts'
import { normalizeMessage } from './normalization.ts'
import { randomUUID } from 'node:crypto'
import WebSocket from 'ws'
import { safeNetworkDetails } from '../../errors.ts'
import { OneBotHttpTransport } from './http.ts'
import {
  OneBotError,
  SNOWLUMA_PROFILE,
  object,
  rawId,
  rawIdOrUrl,
  encode,
  decode,
  time,
  type SceneRef,
  type Message,
  type SendSegment,
  type SendResult,
  type HistoryResult,
  type OneBotConfig,
  type OneBotCallbacks,
  type OneBotProfile,
  type OneBotErrorDetails,
  type Obj,
  type Pending,
} from './protocol.ts'
export * from './protocol.ts'

/** Reusable OneBot transport; implementation-specific actions live in a profile. */
export class OneBotAdapter {
  private readonly http: OneBotHttpTransport
  private socket?: WebSocket
  private reconnectTimer?: ReturnType<typeof setTimeout>
  private opening?: Promise<void>
  private disposed = false
  private ready = false
  private readonly pending = new Map<string, Pending>()
  private readonly recalled = new Set<string>()
  private readonly config: OneBotConfig
  readonly profile: OneBotProfile
  status: 'connecting' | 'connected' | 'disconnected' | 'disposed' = 'disconnected'

  constructor(
    config: OneBotConfig,
    private readonly callbacks: OneBotCallbacks = {},
    profile = SNOWLUMA_PROFILE,
  ) {
    this.config = structuredClone(config)
    this.profile = structuredClone(profile)
    rawId(config.accountId)
    this.http = new OneBotHttpTransport(
      this.config,
      (raw) => this.receive(raw),
      (status) => {
        this.ready = status === 'connected'
        this.setStatus(status)
      },
      (code, details) => this.emitError(code, details),
    )
  }
  get capabilities() {
    return { ...this.profile.capabilities }
  }
  scene(kind: 'group' | 'private', platformSceneId: string): SceneRef {
    rawId(platformSceneId)
    return {
      id: `ob:s:${encode([this.config.id, this.config.accountId, kind, platformSceneId])}`,
      kind,
      adapterId: this.config.id,
      accountId: this.config.accountId,
      platformSceneId,
    }
  }
  messageId(scene: SceneRef, platformMessageId: string): string {
    this.assertScene(scene)
    return `${scene.id}:m:${encode(rawId(platformMessageId))}`
  }
  userId(platformUserId: string): string {
    return `ob:u:${encode([this.config.id, this.config.accountId, rawId(platformUserId)])}`
  }

  async readImage(
    scene: SceneRef,
    assetId: string,
    signal?: AbortSignal,
    fetchMedia?: MediaFetcher,
  ): Promise<Buffer> {
    this.assertScene(scene)
    return readOneBotImage(
      scene,
      assetId,
      (name, params, abort) => this.action(name, params, abort),
      signal,
      fetchMedia,
    )
  }

  connect(): Promise<void> {
    if (this.disposed) return Promise.reject(new OneBotError('adapter_disposed'))
    if (this.ready) return Promise.resolve()
    if (this.opening) return this.opening
    clearTimeout(this.reconnectTimer)
    this.opening = (this.config.transport === 'http' ? this.http.connect() : this.open()).finally(
      () => {
        this.opening = undefined
      },
    )
    return this.opening
  }
  dispose(): void {
    this.disposed = true
    this.ready = false
    clearTimeout(this.reconnectTimer)
    this.http.dispose()
    this.failPending('adapter_disposed')
    this.socket?.terminate()
    this.setStatus('disposed')
  }

  async send(scene: SceneRef, segment: SendSegment, signal?: AbortSignal): Promise<SendResult> {
    if (signal?.aborted) return { status: 'cancelled', detail: 'cancelled_before_send' }
    let acknowledged = false
    try {
      this.assertScene(scene)
      const message: Obj[] = []
      if (segment.replyToId)
        message.push({
          type: 'reply',
          data: { id: this.platformMessageId(scene, segment.replyToId) },
        })
      for (const part of segment.parts) {
        if (part.type === 'text') message.push({ type: 'text', data: { text: part.text } })
        else if (part.type === 'mention')
          message.push({ type: 'at', data: { qq: this.platformUserId(part.userId) } })
        else if (part.type === 'media') {
          const resolved = this.callbacks.resolveMedia?.(part.assetId, scene)
          const media = resolved
            ? { ...resolved, sceneId: scene.id }
            : object(decode(part.assetId.startsWith('ob:media:') ? part.assetId.slice(9) : ''))
          if (
            media.sceneId !== scene.id ||
            !['image', 'record', 'video'].includes(String(media.type))
          )
            throw new OneBotError('media_scope_or_type_unsupported')
          const file = rawIdOrUrl(media.file)
          message.push({ type: media.type, data: { file } })
        } else throw new OneBotError('unsupported_message_part')
      }
      if (!message.length) throw new OneBotError('empty_message')
      const response = await this.action(
        scene.kind === 'group' ? 'send_group_msg' : 'send_private_msg',
        { [scene.kind === 'group' ? 'group_id' : 'user_id']: scene.platformSceneId, message },
        signal,
      )
      acknowledged = true
      const result = object(response)
      let id: string
      try {
        id = this.messageId(scene, rawId(result.message_id))
      } catch {
        return { status: 'unknown', detail: 'missing_send_receipt' }
      }
      return { status: 'sent', messageId: id }
    } catch (error) {
      const failure = error instanceof OneBotError ? error : new OneBotError('send_failed')
      return {
        status:
          acknowledged || failure.transmitted
            ? 'unknown'
            : signal?.aborted
              ? 'cancelled'
              : 'failed',
        detail: failure.code,
      }
    }
  }

  async getMessage(
    scene: SceneRef,
    messageId: string,
    signal?: AbortSignal,
  ): Promise<HistoryResult> {
    try {
      this.assertScene(scene)
      if (this.isRecalled(scene.id, messageId))
        return { items: [], coverage: 'unavailable', reason: 'recalled' }
      const data = await this.action(
        'get_msg',
        { message_id: this.platformMessageId(scene, messageId) },
        signal,
      )
      const normalized = this.normalize(object(data), 'history', scene)
      if (normalized.message.id !== messageId) throw new OneBotError('message_id_mismatch')
      if (normalized.message.status === 'recalled')
        return { items: [], coverage: 'unavailable', reason: 'recalled' }
      return { items: [normalized.message], coverage: 'complete' }
    } catch (error) {
      return { items: [], coverage: 'unavailable', reason: this.errorCode(error) }
    }
  }

  async history(
    scene: SceneRef,
    input: { limit: number; cursor?: string },
    signal?: AbortSignal,
  ): Promise<HistoryResult> {
    try {
      this.assertScene(scene)
      const action =
        scene.kind === 'group' ? this.profile.groupHistoryAction : this.profile.privateHistoryAction
      if (!action) return { items: [], coverage: 'unavailable', reason: 'unsupported' }
      const limit = Math.max(1, Math.min(100, Math.floor(input.limit)))
      if (!Number.isFinite(limit)) throw new OneBotError('invalid_history_limit')
      const params: Obj = {
        [scene.kind === 'group' ? 'group_id' : 'user_id']: scene.platformSceneId,
        count: limit + (input.cursor ? 1 : 0),
        reverse_order: true,
      }
      if (input.cursor) params.message_id = this.platformMessageId(scene, input.cursor)
      const data = object(await this.action(action, params, signal))
      if (!Array.isArray(data.messages)) throw new OneBotError('invalid_history_response')
      const all = data.messages
        .map((item) => this.normalize(object(item), 'history', scene).message)
        .sort((a, b) => a.sentAt.localeCompare(b.sentAt))
      const unique = [...new Map(all.map((message) => [message.id, message])).values()].filter(
        (message) => message.id !== input.cursor,
      )
      const oldest = unique[0]
      return {
        items: unique.filter((message) => message.status !== 'recalled').slice(-limit),
        coverage: 'partial',
        ...(oldest ? { nextCursor: oldest.id } : {}),
        reason: oldest ? 'platform_history_window' : 'history_exhausted_or_unavailable',
      }
    } catch (error) {
      return { items: [], coverage: 'unavailable', reason: this.errorCode(error) }
    }
  }

  private async open(): Promise<void> {
    let url: URL
    try {
      url = new URL(this.config.url)
    } catch {
      throw new OneBotError('invalid_websocket_url')
    }
    if (
      !['ws:', 'wss:'].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    )
      throw new OneBotError('invalid_websocket_url')
    const token =
      this.config.token ??
      (this.config.credentialEnv ? process.env[this.config.credentialEnv] : undefined)
    if (this.config.credentialEnv && !token) throw new OneBotError('credential_not_configured')
    this.setStatus('connecting')
    const socket = new WebSocket(url, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
      handshakeTimeout: this.config.timeoutMs ?? 15_000,
      maxPayload: 8 * 1024 * 1024,
      followRedirects: false,
    })
    this.socket = socket
    socket.on('message', (data) => {
      if (!this.disposed && this.socket === socket) this.receive(data.toString())
    })
    socket.on('error', (error) => this.emitError('websocket_error', safeNetworkDetails(error)))
    socket.on('close', () => {
      if (this.socket !== socket) return
      this.ready = false
      this.failPending('connection_closed')
      if (!this.disposed) {
        this.setStatus('disconnected')
        this.reconnectTimer = setTimeout(
          () => {
            void this.connect().catch((error) =>
              this.emitError('reconnect_failed', {
                ...sanitizeError(error),
                ...safeNetworkDetails(error),
              }),
            )
          },
          Math.max(100, this.config.reconnectMs ?? 3000),
        )
        this.reconnectTimer.unref()
      }
    })
    await new Promise<void>((resolve, reject) => {
      const opened = () => {
        cleanup()
        resolve()
      }
      const failed = (error?: unknown) => {
        cleanup()
        reject(new OneBotError('connection_failed', false, safeNetworkDetails(error)))
      }
      const cleanup = () => {
        socket.off('open', opened)
        socket.off('error', failed)
        socket.off('close', failed)
      }
      socket.once('open', opened)
      socket.once('error', failed)
      socket.once('close', failed)
    })
    try {
      const login = object(await this.action('get_login_info', {}, undefined, true))
      if (rawId(login.user_id) !== this.config.accountId) throw new OneBotError('account_mismatch')
      if (this.disposed || this.socket !== socket || socket.readyState !== WebSocket.OPEN)
        throw new OneBotError('connection_closed')
      this.ready = true
      this.setStatus('connected')
    } catch (error) {
      socket.terminate()
      throw error
    }
  }

  private action(
    name: string,
    params: Obj,
    signal?: AbortSignal,
    bootstrap = false,
  ): Promise<unknown> {
    if (signal?.aborted) return Promise.reject(new OneBotError('request_cancelled'))
    if (this.config.transport === 'http') return this.http.action(name, params, signal, bootstrap)
    const socket = this.socket
    if (
      this.disposed ||
      !socket ||
      socket.readyState !== WebSocket.OPEN ||
      (!bootstrap && !this.ready)
    )
      return Promise.reject(new OneBotError('adapter_not_connected'))
    return new Promise((resolve, reject) => {
      const echo = randomUUID()
      const fail = (code: string) => {
        const pending = this.pending.get(echo)
        if (pending) {
          this.pending.delete(echo)
          pending.cleanup()
          reject(new OneBotError(code, pending.transmitted))
        }
      }
      const timer = setTimeout(() => fail('request_timeout'), this.config.timeoutMs ?? 15_000)
      const abort = () => fail('request_cancelled')
      const pending: Pending = {
        resolve,
        reject,
        cleanup: () => {
          clearTimeout(timer)
          signal?.removeEventListener('abort', abort)
        },
        transmitted: false,
      }
      this.pending.set(echo, pending)
      signal?.addEventListener('abort', abort, { once: true })
      try {
        socket.send(JSON.stringify({ action: name, params, echo }))
        pending.transmitted = true
      } catch {
        fail('socket_send_failed')
      }
    })
  }

  private receive(raw: string): void {
    try {
      const event = object(JSON.parse(raw))
      if (typeof event.echo === 'string' && this.pending.has(event.echo)) {
        const pending = this.pending.get(event.echo)!
        this.pending.delete(event.echo)
        pending.cleanup()
        if (event.status === 'ok' && event.retcode === 0) pending.resolve(event.data)
        else
          pending.reject(
            new OneBotError(
              event.retcode === 1404
                ? 'unsupported'
                : event.retcode === 1400
                  ? 'invalid_action_parameters'
                  : 'platform_action_failed',
            ),
          )
        return
      }
      if (!this.ready || this.disposed) return
      if (event.self_id === undefined || rawId(event.self_id) !== this.config.accountId) return
      if (event.post_type === 'message' || event.post_type === 'message_sent') {
        const normalized = this.normalize(event, 'live')
        this.callbacks.onMessage?.(normalized.message, normalized.scene)
      } else if (
        event.post_type === 'notice' &&
        ['group_recall', 'friend_recall'].includes(String(event.notice_type))
      ) {
        const scene = this.scene(
          event.notice_type === 'group_recall' ? 'group' : 'private',
          rawId(event.notice_type === 'group_recall' ? event.group_id : event.user_id),
        )
        const messageId = this.messageId(scene, rawId(event.message_id))
        this.recalled.add(messageId)
        this.callbacks.onRecall?.(
          { sceneId: scene.id, messageId, occurredAt: time(event.time) },
          scene,
        )
      }
    } catch (error) {
      this.emitError('invalid_event_or_callback_failed', sanitizeError(error))
    }
  }

  private normalize(event: Obj, origin: Message['origin'], expected?: SceneRef) {
    return normalizeMessage(
      {
        accountId: this.config.accountId,
        scene: (kind, peer) => this.scene(kind, peer),
        messageId: (scene, id) => this.messageId(scene, id),
        userId: (id) => this.userId(id),
        isRecalled: (sceneId, id) => this.isRecalled(sceneId, id),
      },
      event,
      origin,
      expected,
    )
  }
  private isRecalled(sceneId: string, messageId: string) {
    return this.recalled.has(messageId) || this.callbacks.isRecalled?.(sceneId, messageId) === true
  }
  private platformMessageId(scene: SceneRef, messageId: string): string {
    const prefix = `${scene.id}:m:`
    if (!messageId.startsWith(prefix)) throw new OneBotError('message_scope_mismatch')
    return rawId(decode(messageId.slice(prefix.length)))
  }
  private platformUserId(userId: string): string {
    if (userId === 'all') return 'all'
    const value = decode(userId.startsWith('ob:u:') ? userId.slice(5) : '')
    if (!Array.isArray(value) || value[0] !== this.config.id || value[1] !== this.config.accountId)
      throw new OneBotError('user_scope_mismatch')
    return rawId(value[2])
  }
  private assertScene(scene: SceneRef) {
    const expected = `ob:s:${encode([this.config.id, this.config.accountId, scene.kind, scene.platformSceneId])}`
    if (
      !['group', 'private'].includes(scene.kind) ||
      scene.id !== expected ||
      scene.adapterId !== this.config.id ||
      scene.accountId !== this.config.accountId
    )
      throw new OneBotError('scene_scope_mismatch')
  }
  private failPending(code: string) {
    for (const pending of this.pending.values()) {
      pending.cleanup()
      pending.reject(new OneBotError(code, pending.transmitted))
    }
    this.pending.clear()
  }
  private setStatus(status: OneBotAdapter['status']) {
    this.status = status
    try {
      this.callbacks.onStatus?.(status)
    } catch (error) {
      this.emitError('status_callback_failed', sanitizeError(error))
    }
  }
  private emitError(code: string, details: OneBotErrorDetails = {}) {
    if (!this.disposed) {
      try {
        this.callbacks.onError?.(code, details)
      } catch {
        /* Keep transport failures isolated. */
      }
    }
  }
  private errorCode(error: unknown) {
    return error instanceof OneBotError ? error.code : 'request_failed'
  }
}
