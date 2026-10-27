import {
  SearchError,
  type ProviderId,
  type ProviderRequest,
  type SearchRequest,
  type SearchResult,
} from '../types.ts'
import { result } from './results.ts'
export function buildApiRequest(
  provider: ProviderId,
  input: SearchRequest,
  key: string | undefined,
): ProviderRequest {
  if (!key) throw new SearchError('credentials_missing')
  if (provider === 'tavily')
    return {
      url: new URL('https://api.tavily.com/search'),
      format: 'json',
      init: {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: input.query,
          max_results: input.limit,
          search_depth: 'basic',
          include_answer: false,
          include_raw_content: false,
          auto_parameters: false,
        }),
      },
    }
  if (provider === 'deepseek')
    return {
      url: new URL('https://api.deepseek.com/v1/tools/search'),
      format: 'json',
      init: {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: input.query, count: input.limit, need_content: false }),
      },
    }
  if (provider === 'you' || provider === 'you_news') {
    return {
      url: new URL('https://ydc-index.io/v1/search'),
      format: 'json',
      init: {
        method: 'POST',
        headers: { 'X-API-Key': key, 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: input.query, count: input.limit }),
      },
    }
  }
  throw new SearchError('provider_disabled')
}
const object = (v: unknown): Record<string, unknown> =>
  v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
export function parseApi(provider: ProviderId, body: string): SearchResult[] {
  let data: Record<string, unknown>
  try {
    data = object(JSON.parse(body))
  } catch {
    throw new SearchError('invalid_response')
  }
  const entries =
    provider === 'you'
      ? object(data.results).web
      : provider === 'you_news'
        ? (object(data.results).news ?? (Array.isArray(object(data.results).web) ? [] : undefined))
        : data.results
  if (!Array.isArray(entries)) throw new SearchError('invalid_response')
  const results: SearchResult[] = []
  for (const entry of entries) {
    const row = object(entry)
    const snippet =
      provider === 'tavily'
        ? row.content
        : provider === 'you'
          ? Array.isArray(row.snippets)
            ? row.snippets
                .filter((v) => typeof v === 'string')
                .slice(0, 5)
                .join('\n')
            : row.description
          : provider === 'you_news'
            ? row.description
            : row.snippet
    const parsed = result(provider, row.title, row.url, snippet)
    if (parsed) results.push(parsed)
  }
  if (entries.length && !results.length) throw new SearchError('invalid_response')
  return results
}
