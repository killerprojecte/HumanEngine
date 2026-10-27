import { untilAbort } from '../../adapters.ts'
import { SearchError, type ProviderRequest } from './types.ts'
// Only bundled endpoints are allowed. There is deliberately no custom URL configuration.
const endpoints = new Set([
  'https://api.tavily.com/search',
  'https://api.deepseek.com/v1/tools/search',
  'https://ydc-index.io/v1/search',
  'https://www.bing.com/search',
  'https://cn.bing.com/search',
  'https://www.google.com/search',
  'https://www.sogou.com/web',
  'https://html.duckduckgo.com/html/',
])
export async function readProvider(
  request: ProviderRequest,
  signal: AbortSignal,
  maxBytes: number,
  fetcher: typeof fetch = fetch,
): Promise<string> {
  const url = request.url
  if (!endpoints.has(url.origin + url.pathname) || url.username || url.password || url.hash)
    throw new SearchError('provider_disabled')
  const response = await untilAbort(
    fetcher(url, { ...request.init, redirect: 'error', signal }),
    signal,
  )
  if (!response.ok) {
    void response.body?.cancel().catch(() => {})
    throw new SearchError(
      response.status === 401 || response.status === 403
        ? 'authentication_failed'
        : response.status === 429
          ? 'rate_limited'
          : 'http_error',
    )
  }
  if (Number(response.headers.get('content-length')) > maxBytes) {
    void response.body?.cancel().catch(() => {})
    throw new SearchError('response_too_large')
  }
  if (!response.body) throw new SearchError('invalid_response')
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let bytes = 0
  try {
    while (true) {
      signal.throwIfAborted()
      const next = await untilAbort(reader.read(), signal)
      if (next.done) break
      bytes += next.value.byteLength
      if (bytes > maxBytes) throw new SearchError('response_too_large')
      chunks.push(next.value)
    }
    return Buffer.concat(chunks).toString('utf8')
  } finally {
    void reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
