import { createHmac, timingSafeEqual } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import { safeNetworkDetails } from '../../errors.ts'
import {
  OneBotError,
  object,
  rawId,
  type OneBotConfig,
  type OneBotErrorDetails,
  type Obj,
} from './protocol.ts'

/** HTTP actions and authenticated inbound events share one abortable lifetime. */
export class OneBotHttpTransport {
  private eventServer?: Server
  private readonly lifetime = new AbortController()
  private disposed = false
  private ready = false
  constructor(
    private readonly config: OneBotConfig,
    private readonly receive: (raw: string) => void,
    private readonly setStatus: (status: 'connecting' | 'connected' | 'disconnected') => void,
    private readonly emitError: (code: string, details?: OneBotErrorDetails) => void,
  ) {}
  dispose(): void {
    this.disposed = true
    this.ready = false
    this.lifetime.abort()
    this.eventServer?.close()
    this.eventServer?.closeAllConnections()
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

  async connect(): Promise<void> {
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
      (this.config.eventToken === undefined &&
        !/^[A-Z][A-Z0-9_]*$/.test(this.config.eventCredentialEnv ?? ''))
    )
      throw new OneBotError('invalid_http_event_configuration')
    const secret =
      this.config.eventToken ??
      (this.config.eventCredentialEnv ? process.env[this.config.eventCredentialEnv] : undefined)
    if (!secret) throw new OneBotError('event_credential_not_configured')
    this.setStatus('connecting')
    try {
      const login = object(await this.action('get_login_info', {}, undefined, true))
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
      server.on('error', (error) => {
        this.ready = false
        if (!this.disposed) this.setStatus('disconnected')
        this.emitError('http_event_server_error', safeNetworkDetails(error))
      })
      await new Promise<void>((resolve, reject) => {
        const failed = (error?: unknown) => {
          cleanup()
          reject(new OneBotError('http_event_listen_failed', false, safeNetworkDetails(error)))
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

  async action(
    name: string,
    params: Obj,
    signal?: AbortSignal,
    bootstrap = false,
  ): Promise<unknown> {
    if (signal?.aborted) throw new OneBotError('request_cancelled')
    if (this.disposed || (!bootstrap && !this.ready)) throw new OneBotError('adapter_not_connected')
    const url = this.httpUrl()
    url.pathname = url.pathname.replace(/\/$/, '') + '/' + name
    const token =
      this.config.token ??
      (this.config.credentialEnv ? process.env[this.config.credentialEnv] : undefined)
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
        throw new OneBotError('http_action_failed', true, { httpStatus: response.status })
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
        safeNetworkDetails(error),
      )
    } finally {
      clearTimeout(timer)
    }
  }
}
