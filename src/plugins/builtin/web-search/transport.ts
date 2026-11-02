import { untilAbort } from '../../adapters.ts'
import {
  SearchError,
  type ProviderRequest,
  type ProviderResponse,
  type CacheSearchResult,
} from './types.ts'
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
  parserBudgetBytes: number,
  fetcher: typeof fetch = fetch,
  cacheResult: CacheSearchResult,
): Promise<ProviderResponse> {
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
  if (!response.body) throw new SearchError('invalid_response')
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let bytes = 0
  let parseLimited = false
  async function* source() {
    try {
      while (true) {
        signal.throwIfAborted()
        const next = await untilAbort(reader.read(), signal)
        if (next.done) break
        bytes += next.value.byteLength
        if (bytes > parserBudgetBytes) {
          parseLimited = true
          chunks.length = 0
        } else if (!parseLimited) chunks.push(next.value.slice())
        yield next.value
      }
    } finally {
      void reader.cancel().catch(() => {})
      reader.releaseLock()
    }
  }
  try {
    const body = await cacheResult(source(), {
      contentType:
        response.headers.get('content-type') ??
        (request.format === 'json' ? 'application/json' : 'text/html'),
      encoding: 'utf8',
      previewBytes: 1024,
    })
    return {
      ...(parseLimited ? {} : { text: Buffer.concat(chunks).toString('utf8') }),
      body,
      parseLimited,
      parserBudgetBytes,
      httpStatus: response.status,
    }
  } finally {
    // Also close the response if cache setup fails before the generator is first advanced.
    void reader.cancel().catch(() => {})
    try {
      reader.releaseLock()
    } catch {}
  }
}
