import type { PluginToolContext } from '../../types.ts'
export const providerIds = [
  'bing',
  'google',
  'sogou',
  'duckduckgo',
  'tavily',
  'you',
  'you_news',
  'deepseek',
] as const
export type ProviderId = (typeof providerIds)[number]
export interface SearchResult {
  title: string
  url: string
  snippet: string
  provider: ProviderId
}
export interface SearchRequest {
  query: string
  limit: number
}
export interface ProviderRequest {
  url: URL
  init: RequestInit
  format: 'json' | 'html'
}
export type CacheSearchResult = PluginToolContext['cacheResult']
export interface ProviderResponse {
  text?: string
  body: Awaited<ReturnType<CacheSearchResult>>
  parseLimited: boolean
  parserBudgetBytes: number
  httpStatus: number
}
export interface SearchResponse {
  trust: 'untrusted_web_content'
  instruction: string
  results: SearchResult[]
  attemptedProviders: ProviderId[]
  status: 'ok' | 'no_results' | 'unavailable' | 'partial'
  body?: ProviderResponse['body']
  bodyProvider?: ProviderId
  parseLimited?: boolean
  parserBudgetBytes?: number
  httpStatus?: number
}
export class SearchError extends Error {
  constructor(
    readonly code:
      | 'cancelled'
      | 'timeout'
      | 'busy'
      | 'credentials_missing'
      | 'http_error'
      | 'authentication_failed'
      | 'rate_limited'
      | 'parse_limit'
      | 'invalid_response'
      | 'network_error'
      | 'provider_disabled',
  ) {
    super(code)
  }
}
