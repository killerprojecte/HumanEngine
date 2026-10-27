import { isIP } from 'node:net'
import type { ProviderId, SearchResult } from '../types.ts'
/** Returned links are never fetched by this plugin. Reject local/active-content links nevertheless. */
export function publicResultUrl(raw: unknown): string | undefined {
  if (typeof raw !== 'string' || raw.length > 4096) return
  try {
    const url = new URL(raw)
    const host = url.hostname.toLowerCase().replace(/\.$/, '')
    if (
      !['https:', 'http:'].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.port ||
      !host.includes('.') ||
      isIP(host.replace(/^\[|\]$/g, '')) ||
      /(?:^|\.)(localhost|local|internal|test|invalid)$/.test(host)
    )
      return
    return url.href
  } catch {
    return
  }
}
export function result(
  provider: ProviderId,
  title: unknown,
  url: unknown,
  snippet: unknown,
): SearchResult | undefined {
  const href = publicResultUrl(url)
  if (!href || typeof title !== 'string' || !title.trim()) return
  return {
    provider,
    title: title.slice(0, 512),
    url: href,
    snippet: typeof snippet === 'string' ? snippet.slice(0, 4000) : '',
  }
}
export function uniqueResults(items: SearchResult[], limit: number): SearchResult[] {
  const seen = new Set<string>()
  return items
    .filter((item) => {
      if (seen.has(item.url)) return false
      seen.add(item.url)
      return true
    })
    .slice(0, limit)
}
