import { lookup } from 'node:dns/promises'
import { get } from 'node:https'
import { isIP } from 'node:net'

// Incoming platform URLs are untrusted. Limit intake to the QQ media CDN and pin a public DNS result.
export async function fetchQqMedia(value: string, signal?: AbortSignal): Promise<Buffer> {
  const url = new URL(value)
  if (
    url.protocol !== 'https:' ||
    url.username ||
    url.password ||
    (url.port && url.port !== '443') ||
    !['qpic.cn', 'qq.com'].some(
      (host) => url.hostname === host || url.hostname.endsWith('.' + host),
    )
  )
    throw new Error('media_host_not_allowed')
  const addresses = await Promise.race([
    lookup(url.hostname, { all: true }),
    new Promise<never>((_resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('media_dns_timeout')), 5000)
      timer.unref()
    }),
  ])
  if (signal?.aborted) throw new Error('media_cancelled')
  const publicAddress = (ip: string) => {
    if (isIP(ip) !== 4) return false
    const [a, b] = ip.split('.').map(Number)
    return (
      a !== undefined &&
      b !== undefined &&
      a !== 0 &&
      a !== 10 &&
      a !== 127 &&
      a !== 169 &&
      a < 224 &&
      !(a === 172 && b >= 16 && b <= 31) &&
      !(a === 192 && b === 168) &&
      !(a === 100 && b >= 64 && b <= 127) &&
      !(a === 198 && (b === 18 || b === 19))
    )
  }
  const address = addresses.find((a) => publicAddress(a.address))
  if (!address || addresses.some((a) => isIP(a.address) === 4 && !publicAddress(a.address)))
    throw new Error('media_address_not_public')
  return new Promise((resolve, reject) => {
    const request = get(
      {
        hostname: address.address,
        servername: url.hostname,
        path: url.pathname + url.search,
        headers: { Host: url.hostname },
        signal,
        timeout: 15000,
      },
      (response) => {
        if (response.statusCode !== 200) {
          response.resume()
          reject(new Error('media_fetch_failed'))
          return
        }
        const chunks: Buffer[] = []
        let size = 0
        response.on('data', (chunk: Buffer) => {
          size += chunk.length
          if (size > 10 * 1024 * 1024) {
            request.destroy(new Error('media_size_limit'))
            return
          }
          chunks.push(chunk)
        })
        response.on('end', () => resolve(Buffer.concat(chunks)))
        response.on('error', () => reject(new Error('media_fetch_failed')))
      },
    )
    request.on('timeout', () => request.destroy(new Error('media_timeout')))
    request.on('error', () => reject(new Error('media_fetch_failed')))
  })
}
