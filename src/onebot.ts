import { randomUUID, createHmac, timingSafeEqual } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import WebSocket from 'ws'

export interface SceneRef {
  id: string
  kind: 'group' | 'private'
  adapterId: string
  accountId: string
  platformSceneId: string
}
export type MessagePart =
  | { type: 'text'; text: string }
  | { type: 'mention'; userId: string }
  | { type: 'media'; assetId: string; mediaType: string }
  | { type: 'unsupported'; description: string }
export interface Message {
  id: string
  sceneId: string
  senderId: string
  sentAt: string
  receivedAt: string
  origin: 'live' | 'history'
  parts: MessagePart[]
  replyToId?: string
  status: 'available' | 'recalled' | 'unavailable'
}
export interface RecallEvent {
  sceneId: string
  messageId: string
  occurredAt: string
}
export interface SendSegment {
  parts: MessagePart[]
  replyToId?: string
}
export interface SendResult {
  status: 'sent' | 'failed' | 'unknown' | 'cancelled'
  messageId?: string
  detail?: string
}
export interface HistoryResult {
  items: Message[]
  coverage: 'complete' | 'partial' | 'unavailable'
  nextCursor?: string
  reason?: string
}
export interface OneBotProfile {
  id: string
  groupHistoryAction?: string
  privateHistoryAction?: string
  capabilities: {
    singleMessage: boolean
    groupHistory: boolean
    privateHistory: boolean
    around: boolean
    byUser: boolean
    animation: 'unverified' | 'unsupported'
  }
}
export const ONEBOT_V11_PROFILE: OneBotProfile = {
  id: 'onebot-v11',
  capabilities: {
    singleMessage: true,
    groupHistory: false,
    privateHistory: false,
    around: false,
    byUser: false,
    animation: 'unverified',
  },
}
export const SNOWLUMA_PROFILE: OneBotProfile = {
  id: 'snowluma',
  groupHistoryAction: 'get_group_msg_history',
  privateHistoryAction: 'get_friend_msg_history',
  capabilities: {
    singleMessage: true,
    groupHistory: true,
    privateHistory: true,
    around: false,
    byUser: false,
    animation: 'unverified',
  },
}
export interface OneBotConfig {
  id: string
  accountId: string
  url: string
  credentialEnv: string
  transport?: 'websocket' | 'http'
  eventHost?: '127.0.0.1' | '::1' | '0.0.0.0' | '::'
  eventPort?: number
  eventPath?: string
  eventCredentialEnv?: string
  timeoutMs?: number
  reconnectMs?: number
}
export interface OneBotCallbacks {
  onMessage?: (message: Message, scene: SceneRef) => void
  onRecall?: (event: RecallEvent, scene: SceneRef) => void
  onStatus?: (status: 'connecting' | 'connected' | 'disconnected' | 'disposed') => void
  onError?: (code: string) => void
  isRecalled?: (sceneId: string, messageId: string) => boolean
  resolveMedia?: (
    assetId: string,
    scene: SceneRef,
  ) => { file: string; type: 'image' | 'record' | 'video' } | undefined
}
type Obj = Record<string, unknown>
const object = (value: unknown): Obj => {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new OneBotError('invalid_payload')
  return value as Obj
}
const rawId = (value: unknown): string => {
  if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value)
  if (typeof value === 'string' && value.length > 0 && value.length <= 256) return value
  throw new OneBotError('invalid_identifier')
}
const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
const decode = (value: string): unknown => {
  try {
    return JSON.parse(Buffer.from(value, 'base64url').toString('utf8'))
  } catch {
    throw new OneBotError('invalid_identifier')
  }
}
const time = (value: unknown): string => {
  if (typeof value !== 'number' || !Number.isFinite(value) || Math.abs(value) > 8_640_000_000_000)
    throw new OneBotError('invalid_timestamp')
  return new Date(value * 1000).toISOString()
}
export class OneBotError extends Error {
  constructor(
    public readonly code: string,
    public readonly transmitted = false,
  ) {
    super(code)
    this.name = 'OneBotError'
  }
}
interface Pending {
  resolve: (value: unknown) => void
  reject: (error: OneBotError) => void
  cleanup: () => void
  transmitted: boolean
}

/** Reusable OneBot transport; implementation-specific actions live in a profile. */
export class OneBotAdapter {
  private eventServer?: Server
  private readonly lifetime = new AbortController()
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

  connect(): Promise<void> {
    if (this.disposed) return Promise.reject(new OneBotError('adapter_disposed'))
    if (this.ready) return Promise.resolve()
    if (this.opening) return this.opening
    clearTimeout(this.reconnectTimer)
    this.opening = (this.config.transport === 'http' ? this.openHttp() : this.open()).finally(
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
    this.lifetime.abort()
    this.eventServer?.close()
    this.eventServer?.closeAllConnections()
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

  private httpUrl(): URL {
    let url: URL
    try {
      url = new URL(this.config.url)
    } catch {
      throw new OneBotError('invalid_http_url')
    }
    if (
      !['http:', 'https:'].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    )
      throw new OneBotError('invalid_http_url')
    return url
  }

  private async openHttp(): Promise<void> {
    this.httpUrl()
    const host = this.config.eventHost ?? '127.0.0.1'
    const port = this.config.eventPort
    const path = this.config.eventPath ?? '/onebot/events'
    if (
      !['127.0.0.1', '::1', '0.0.0.0', '::'].includes(host) ||
      !Number.isInteger(port) ||
      port! < 0 ||
      port! > 65535 ||
      !/^\/[a-zA-Z0-9/_-]*$/.test(path) ||
      path.includes('//') ||
      !/^[A-Z][A-Z0-9_]*$/.test(this.config.eventCredentialEnv ?? '')
    )
      throw new OneBotError('invalid_http_event_configuration')
    const secret = process.env[this.config.eventCredentialEnv!]
    if (!secret) throw new OneBotError('event_credential_not_configured')
    this.setStatus('connecting')
    try {
      const login = object(await this.httpAction('get_login_info', {}, undefined, true))
      if (rawId(login.user_id) !== this.config.accountId) throw new OneBotError('account_mismatch')
      if (this.disposed) throw new OneBotError('adapter_disposed')
      const server = createServer(
        { maxHeaderSize: 16384, requestTimeout: 15000, headersTimeout: 10000 },
        (request, response) => {
          const finish = (status: number) => {
            response.writeHead(status, { Connection: 'close' })
            response.end()
          }
          if (!this.ready || this.disposed) {
            finish(503)
            return
          }
          if (request.url !== path || request.method !== 'POST') {
            finish(404)
            return
          }
          if (request.headers['content-type']?.split(';')[0]?.trim() !== 'application/json') {
            finish(415)
            return
          }
          const signature = request.headers['x-signature']
          if (
            typeof signature !== 'string' ||
            !/^sha1=[a-fA-F0-9]{40}$/.test(signature) ||
            request.headers['x-self-id'] !== this.config.accountId
          ) {
            finish(401)
            return
          }
          const chunks: Buffer[] = []
          let size = 0,
            rejected = false
          request.on('error', () => {
            rejected = true
          })
          request.on('data', (chunk: Buffer) => {
            if (rejected) return
            size += chunk.length
            if (size > 1024 * 1024) {
              rejected = true
              chunks.length = 0
              finish(413)
              return
            }
            chunks.push(chunk)
          })
          request.on('end', () => {
            if (rejected || response.writableEnded) return
            if (!this.ready || this.disposed) {
              finish(503)
              return
            }
            const body = Buffer.concat(chunks)
            const expected = createHmac('sha1', secret).update(body).digest()
            if (!timingSafeEqual(expected, Buffer.from(signature.slice(5), 'hex'))) {
              finish(401)
              return
            }
            try {
              const event = object(JSON.parse(body.toString('utf8')))
              if (rawId(event.self_id) !== this.config.accountId) {
                finish(403)
                return
              }
              this.receive(body.toString('utf8'))
              finish(204) // Never return a quick action that bypasses the core send guards.
            } catch {
              finish(400)
            }
          })
        },
      )
      this.eventServer = server
      server.maxConnections = 64
      server.maxRequestsPerSocket = 1
      server.on('error', () => {
        this.ready = false
        if (!this.disposed) this.setStatus('disconnected')
        this.emitError('http_event_server_error')
      })
      await new Promise<void>((resolve, reject) => {
        const failed = () => {
          cleanup()
          reject(new OneBotError('http_event_listen_failed'))
        }
        const abort = () => {
          server.close()
          failed()
        }
        const cleanup = () => {
          server.off('error', failed)
          this.lifetime.signal.removeEventListener('abort', abort)
        }
        server.once('error', failed)
        this.lifetime.signal.addEventListener('abort', abort, { once: true })
        server.listen(port, host, () => {
          cleanup()
          resolve()
        })
      })
      if (this.disposed) {
        server.close()
        throw new OneBotError('adapter_disposed')
      }
      this.ready = true
      this.setStatus('connected')
    } catch (error) {
      this.eventServer?.close()
      this.eventServer?.closeAllConnections()
      if (!this.disposed) this.setStatus('disconnected')
      throw error
    }
  }

  private async httpAction(
    name: string,
    params: Obj,
    signal?: AbortSignal,
    bootstrap = false,
  ): Promise<unknown> {
    if (signal?.aborted) throw new OneBotError('request_cancelled')
    if (this.disposed || (!bootstrap && !this.ready)) throw new OneBotError('adapter_not_connected')
    const url = this.httpUrl()
    url.pathname = url.pathname.replace(/\/$/, '') + '/' + name
    const token = this.config.credentialEnv ? process.env[this.config.credentialEnv] : undefined
    if (this.config.credentialEnv && !token) throw new OneBotError('credential_not_configured')
    const controller = new AbortController()
    const combined = AbortSignal.any([
      this.lifetime.signal,
      controller.signal,
      ...(signal ? [signal] : []),
    ])
    const timer = setTimeout(() => controller.abort(), this.config.timeoutMs ?? 15000)
    let transmitted = false
    try {
      if (combined.aborted) throw new OneBotError('request_cancelled')
      transmitted = true // Fetch cannot prove that a failing request never reached the peer.
      const response = await fetch(url, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify(params),
        redirect: 'manual',
        signal: combined,
      })
      if (!response.ok) {
        await response.body?.cancel()
        throw new OneBotError('http_action_failed', true)
      }
      const reader = response.body?.getReader()
      if (!reader) throw new OneBotError('invalid_http_response', true)
      const chunks: Uint8Array[] = []
      let length = 0
      try {
        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          length += value.byteLength
          if (length > 8 * 1024 * 1024) {
            await reader.cancel()
            throw new OneBotError('http_response_too_large', true)
          }
          chunks.push(value)
        }
      } finally {
        reader.releaseLock()
      }
      if (combined.aborted) throw new OneBotError('request_cancelled', true)
      let envelope: Obj
      try {
        envelope = object(JSON.parse(Buffer.concat(chunks).toString('utf8')))
      } catch {
        throw new OneBotError('invalid_http_response', true)
      }
      if (typeof envelope.status !== 'string' || typeof envelope.retcode !== 'number')
        throw new OneBotError('invalid_http_response', true)
      if (envelope.status !== 'ok' || envelope.retcode !== 0) {
        if (envelope.status !== 'failed' || envelope.retcode === 0)
          throw new OneBotError('invalid_http_response', true)
        throw new OneBotError(
          envelope.retcode === 1404
            ? 'unsupported'
            : envelope.retcode === 1400
              ? 'invalid_action_parameters'
              : 'platform_action_failed',
        )
      }
      return envelope.data
    } catch (error) {
      if (error instanceof OneBotError) throw error
      throw new OneBotError(
        combined.aborted ? 'request_cancelled' : 'http_request_failed',
        transmitted,
      )
    } finally {
      clearTimeout(timer)
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
    const token = this.config.credentialEnv ? process.env[this.config.credentialEnv] : undefined
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
    socket.on('error', () => this.emitError('websocket_error'))
    socket.on('close', () => {
      if (this.socket !== socket) return
      this.ready = false
      this.failPending('connection_closed')
      if (!this.disposed) {
        this.setStatus('disconnected')
        this.reconnectTimer = setTimeout(
          () => {
            void this.connect().catch(() => this.emitError('reconnect_failed'))
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
      const failed = () => {
        cleanup()
        reject(new OneBotError('connection_failed'))
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
    if (this.config.transport === 'http') return this.httpAction(name, params, signal, bootstrap)
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
    } catch {
      this.emitError('invalid_event_or_callback_failed')
    }
  }

  private normalize(
    event: Obj,
    origin: Message['origin'],
    expected?: SceneRef,
  ): { message: Message; scene: SceneRef } {
    if (event.self_id !== undefined && rawId(event.self_id) !== this.config.accountId)
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
        : sender === this.config.accountId
          ? rawId(event.target_id)
          : sender
    const scene = this.scene(kind, peer)
    if (expected && scene.id !== expected.id) throw new OneBotError('scene_mismatch')
    const id = this.messageId(scene, rawId(event.message_id))
    const message: Message = {
      id,
      sceneId: scene.id,
      senderId: this.userId(sender),
      sentAt: time(event.time),
      receivedAt: new Date().toISOString(),
      origin,
      parts: [],
      status: 'available',
    }
    if (this.isRecalled(scene.id, id)) {
      message.status = 'recalled'
      return { message, scene }
    }
    if (!Array.isArray(event.message)) {
      message.parts = [
        { type: 'unsupported', description: 'Configure OneBot array message format' },
      ]
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
          userId: data.qq === 'all' ? 'all' : this.userId(rawId(data.qq)),
        })
      else if (segment.type === 'reply') message.replyToId = this.messageId(scene, rawId(data.id))
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
    } catch {
      this.emitError('status_callback_failed')
    }
  }
  private emitError(code: string) {
    if (!this.disposed) {
      try {
        this.callbacks.onError?.(code)
      } catch {
        /* Keep transport failures isolated. */
      }
    }
  }
  private errorCode(error: unknown) {
    return error instanceof OneBotError ? error.code : 'request_failed'
  }
}
function rawIdOrUrl(value: unknown): string {
  if (typeof value !== 'string' || !value || value.length > 16_384)
    throw new OneBotError('invalid_media_reference')
  return value
}
