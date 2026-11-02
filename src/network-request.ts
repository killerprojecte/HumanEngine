import { lookup } from 'node:dns/promises'
import {
  request as httpRequest,
  type ClientRequest,
  type IncomingHttpHeaders,
  type IncomingMessage,
  type RequestOptions,
  validateHeaderName,
  validateHeaderValue,
} from 'node:http'
import { request as httpsRequest } from 'node:https'
import { resolveMediaHost, validateMediaUrl } from './media-fetch.ts'

export interface NetworkTransport {
  lookup(hostname: string): Promise<Array<{ address: string; family: number }>>
  request(
    options: RequestOptions & { servername?: string },
    callback: (response: IncomingMessage) => void,
  ): ClientRequest
}
const network: NetworkTransport = {
  lookup: (hostname) => lookup(hostname, { all: true }),
  request: (options, callback) =>
    options.protocol === 'https:'
      ? httpsRequest(options, callback)
      : httpRequest(options, callback),
}
export interface NetworkRequestOptions {
  method?: string
  headers?: Record<string, string>
  body?: string | Uint8Array
  signal?: AbortSignal
  timeoutMs?: number
  followRedirects?: boolean
  maxRedirects?: number
}
export interface NetworkResponse {
  /** Raw response bytes, including content-encoding. Consume or destroy this stream. */
  response: IncomingMessage
  statusCode: number
  headers: IncomingHttpHeaders
  rawHeaders: string[]
  finalUrl: string
  contentType: string
}
const methods = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'])
const forbiddenHeaders = new Set([
  'host',
  'connection',
  'proxy-connection',
  'proxy-authorization',
  'proxy-authenticate',
  'transfer-encoding',
  'content-length',
  'upgrade',
  'te',
  'trailer',
  'expect',
])

/** Stream an HTTP request without a body-size ceiling; DNS/TLS and redirect checks stay enforced. */
export async function requestPublicResource(
  value: string,
  options: NetworkRequestOptions = {},
  transport: NetworkTransport = network,
): Promise<NetworkResponse> {
  let method = (options.method ?? 'GET').toUpperCase()
  let body = options.body
  let headers: Record<string, string> = {}
  const timeoutMs = options.timeoutMs ?? 60000
  const maxRedirects = options.maxRedirects ?? 10
  if (
    !methods.has(method) ||
    (body !== undefined && method === 'HEAD') ||
    !Number.isSafeInteger(timeoutMs) ||
    timeoutMs < 1 ||
    timeoutMs > 2147483647 ||
    !Number.isSafeInteger(maxRedirects) ||
    maxRedirects < 0
  )
    throw new Error('invalid_network_request')
  for (const [name, header] of Object.entries(options.headers ?? {})) {
    const lower = name.toLowerCase()
    try {
      validateHeaderName(name)
      validateHeaderValue(name, header)
      if (forbiddenHeaders.has(lower)) throw new Error()
    } catch {
      throw new Error('invalid_network_header')
    }
    headers[lower] = header
  }
  const lifetime = AbortSignal.any([
    ...(options.signal ? [options.signal] : []),
    AbortSignal.timeout(timeoutMs),
  ])
  const initial = new URL(value)
  initial.hash = ''
  let url = validateMediaUrl(initial.href)
  for (let hop = 0; ; hop++) {
    const address = await resolveMediaHost(url.hostname, lifetime, transport)
    lifetime.throwIfAborted()
    const response = await new Promise<IncomingMessage>((resolve, reject) => {
      let incoming: IncomingMessage | undefined
      const req = transport.request(
        {
          protocol: url.protocol,
          hostname: address.address,
          family: address.family,
          port: url.port || (url.protocol === 'https:' ? 443 : 80),
          ...(url.protocol === 'https:'
            ? { servername: url.hostname.replace(/^\[|\]$/g, '') }
            : {}),
          method,
          path: url.pathname + url.search,
          headers: {
            'accept-encoding': 'identity',
            ...headers,
            Host: url.host,
            ...(body !== undefined ? { 'content-length': Buffer.byteLength(body) } : {}),
          },
          signal: lifetime,
        },
        (res) => {
          incoming = res
          // Install a listener before handing off so early stream failures are never unhandled.
          res.on('error', () => {})
          res.once('close', () => lifetime.removeEventListener('abort', abort))
          resolve(res)
        },
      )
      const abort = () => {
        const error = new Error('network_cancelled')
        incoming?.destroy(error)
        req.destroy(error)
        reject(error)
      }
      lifetime.addEventListener('abort', abort, { once: true })
      req.on('error', () => {
        lifetime.removeEventListener('abort', abort)
        const error = new Error(lifetime.aborted ? 'network_cancelled' : 'network_request_failed')
        incoming?.destroy(error)
        reject(error)
      })
      if (lifetime.aborted) abort()
      else req.end(body)
    })
    const statusCode = response.statusCode ?? 0
    if (
      options.followRedirects !== false &&
      [301, 302, 303, 307, 308].includes(statusCode) &&
      response.headers.location
    ) {
      response.destroy()
      if (hop >= maxRedirects) throw new Error('network_redirect_limit')
      const nextValue = new URL(response.headers.location, url)
      nextValue.hash = ''
      const next = validateMediaUrl(nextValue.href)
      if (url.protocol === 'https:' && next.protocol !== 'https:')
        throw new Error('network_redirect_downgrade')
      if (next.origin !== url.origin) headers = {}
      if (
        (statusCode === 303 && method !== 'HEAD') ||
        ([301, 302].includes(statusCode) && method === 'POST')
      ) {
        method = 'GET'
        body = undefined
        delete headers['content-type']
        delete headers['content-encoding']
      }
      url = next
      continue
    }
    if (lifetime.aborted) {
      response.destroy()
      throw new Error('network_cancelled')
    }
    return {
      response,
      statusCode,
      headers: response.headers,
      rawHeaders: [...response.rawHeaders],
      finalUrl: url.href,
      contentType: String(response.headers['content-type'] ?? ''),
    }
  }
}
