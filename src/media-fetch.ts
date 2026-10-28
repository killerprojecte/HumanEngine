import { lookup } from 'node:dns/promises'
import { get as httpsGet } from 'node:https'
import {
  get as httpGet,
  type ClientRequest,
  type IncomingMessage,
  type RequestOptions,
} from 'node:http'
import { isIP, BlockList } from 'node:net'

export interface MediaNetwork {
  lookup(hostname: string): Promise<Array<{ address: string; family: number }>>
  get(
    options: RequestOptions & { servername?: string },
    callback: (response: IncomingMessage) => void,
  ): ClientRequest
}
const network: MediaNetwork = {
  lookup: (hostname) => lookup(hostname, { all: true }),
  get: (options, callback) =>
    options.protocol === 'http:' ? httpGet(options, callback) : httpsGet(options, callback),
}

/** Accept public HTTP(S) sources, not a fixed vendor-domain allowlist. */
export function validateMediaUrl(value: string): URL {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error('invalid_media_reference')
  }
  if (
    !['http:', 'https:'].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.hash ||
    value.length > 16384
  )
    throw new Error('media_url_not_supported')
  return url
}
const globalV6 = new BlockList()
globalV6.addSubnet('2000::', 3, 'ipv6')
const reserved = new BlockList()
// Azure platform virtual IP (WireServer), not an ordinary public internet destination.
reserved.addAddress('168.63.129.16', 'ipv4')
for (const [address, prefix] of [
  ['0.0.0.0', 8],
  ['10.0.0.0', 8],
  ['100.64.0.0', 10],
  ['127.0.0.0', 8],
  ['169.254.0.0', 16],
  ['172.16.0.0', 12],
  ['192.0.0.0', 24],
  ['192.0.2.0', 24],
  ['192.88.99.0', 24],
  ['192.168.0.0', 16],
  ['198.18.0.0', 15],
  ['198.51.100.0', 24],
  ['203.0.113.0', 24],
  ['224.0.0.0', 3],
] as const)
  reserved.addSubnet(address, prefix, 'ipv4')
for (const [address, prefix] of [
  ['2001::', 32],
  ['2001:2::', 48],
  ['2001:10::', 28],
  ['2001:20::', 28],
  ['2001:db8::', 32],
  ['2002::', 16],
  ['3fff::', 20],
] as const)
  reserved.addSubnet(address, prefix, 'ipv6')
export function publicMediaAddress(address: string): boolean {
  const family = isIP(address)
  return family === 4
    ? !reserved.check(address, 'ipv4')
    : family === 6 && globalV6.check(address, 'ipv6') && !reserved.check(address, 'ipv6')
}
async function resolveMediaHost(hostname: string, signal: AbortSignal, transport: MediaNetwork) {
  const host = hostname.replace(/^\[|\]$/g, '')
  const addresses = isIP(host)
    ? [{ address: host, family: isIP(host) }]
    : await new Promise<Array<{ address: string; family: number }>>((resolve, reject) => {
        const finish = () => {
          clearTimeout(timer)
          signal.removeEventListener('abort', abort)
        }
        const abort = () => {
          finish()
          reject(new Error('media_cancelled'))
        }
        const timer = setTimeout(() => {
          finish()
          reject(new Error('media_dns_timeout'))
        }, 5000)
        signal.addEventListener('abort', abort, { once: true })
        if (signal.aborted) {
          abort()
          return
        }
        void transport.lookup(host).then(
          (value) => {
            finish()
            resolve(value)
          },
          () => {
            finish()
            reject(new Error('media_dns_failed'))
          },
        )
      })
  if (signal.aborted) throw new Error('media_cancelled')
  if (!addresses.length || addresses.some((item) => !publicMediaAddress(item.address)))
    throw new Error('media_address_not_public')
  return addresses.find((item) => item.family === 4) ?? addresses[0]!
}

/** Compatibility name retained; all public media hosts are supported with hop-by-hop address pinning. */
export interface PublicResource {
  bytes: Buffer
  contentType: string
  finalUrl: string
}
export async function fetchPublicResource(
  value: string,
  options: { signal?: AbortSignal; maxBytes?: number; timeoutMs?: number } = {},
  transport: MediaNetwork = network,
): Promise<PublicResource> {
  const { signal, maxBytes = 10 * 1024 * 1024, timeoutMs = 15000 } = options
  if (
    !Number.isInteger(maxBytes) ||
    maxBytes < 1 ||
    maxBytes > 10 * 1024 * 1024 ||
    !Number.isInteger(timeoutMs) ||
    timeoutMs < 1 ||
    timeoutMs > 60000
  )
    throw new Error('invalid_fetch_budget')
  const lifetime = AbortSignal.any([...(signal ? [signal] : []), AbortSignal.timeout(timeoutMs)])
  let url = validateMediaUrl(value)
  for (let hop = 0; hop <= 3; hop++) {
    const address = await resolveMediaHost(url.hostname, lifetime, transport)
    const result = await new Promise<PublicResource | URL>((resolve, reject) => {
      const request = transport.get(
        {
          protocol: url.protocol,
          hostname: address.address,
          family: address.family,
          port: url.port || (url.protocol === 'https:' ? 443 : 80),
          ...(url.protocol === 'https:' ? { servername: url.hostname } : {}),
          path: url.pathname + url.search,
          headers: { Host: url.host },
          signal: lifetime,
          timeout: 15000,
        },
        (response) => {
          if ([301, 302, 303, 307, 308].includes(response.statusCode ?? 0)) {
            response.destroy()
            if (!response.headers.location || hop === 3) {
              reject(new Error('media_redirect_limit'))
              return
            }
            try {
              const next = validateMediaUrl(new URL(response.headers.location, url).href)
              if (url.protocol === 'https:' && next.protocol !== 'https:')
                throw new Error('media_redirect_downgrade')
              resolve(next)
            } catch {
              reject(new Error('media_redirect_not_supported'))
            }
            return
          }
          if (response.statusCode !== 200) {
            response.destroy()
            reject(new Error(`public_http_${response.statusCode ?? 0}`))
            return
          }
          if (Number(response.headers['content-length'] ?? 0) > maxBytes) {
            response.destroy()
            reject(new Error('media_size_limit'))
            return
          }
          const chunks: Buffer[] = []
          let size = 0
          response.on('data', (chunk: Buffer) => {
            size += chunk.length
            if (size > maxBytes) {
              request.destroy()
              response.destroy()
              reject(new Error('media_size_limit'))
              return
            }
            chunks.push(chunk)
          })
          response.on('end', () =>
            lifetime.aborted
              ? reject(new Error('media_cancelled'))
              : resolve({
                  bytes: Buffer.concat(chunks),
                  contentType: String(response.headers['content-type'] ?? '')
                    .trim()
                    .toLowerCase()
                    .slice(0, 256),
                  finalUrl: url.href,
                }),
          )
          response.on('aborted', () => reject(new Error('media_fetch_failed')))
          response.on('error', () => reject(new Error('media_fetch_failed')))
        },
      )
      request.on('timeout', () => request.destroy(new Error('media_timeout')))
      request.on('error', () =>
        reject(new Error(lifetime.aborted ? 'media_cancelled' : 'media_fetch_failed')),
      )
    })
    if (result instanceof URL) {
      url = result
      continue
    }
    return result
  }
  throw new Error('media_redirect_limit')
}

export async function fetchPublicBytes(
  value: string,
  signal?: AbortSignal,
  transport: MediaNetwork = network,
): Promise<Buffer> {
  return (await fetchPublicResource(value, { signal }, transport)).bytes
}

/** Existing adapter interface; no domain restriction is applied. */
export const fetchQqMedia = fetchPublicBytes
