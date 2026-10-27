export const webSearchFields = [
  'provider',
  'fallbackProvider',
  'allowExperimentalHtml',
  'maxResults',
  'timeoutMs',
  'maxResponseBytes',
  'maxConcurrency',
  'tavilyKey',
  'clearTavilyKey',
  'youKey',
  'clearYouKey',
  'deepseekKey',
  'clearDeepseekKey',
] as const
export interface WebSearchIssue {
  field: (typeof webSearchFields)[number]
  reason: 'required' | 'invalid_value'
}
export function isWebSearchIssue(value: unknown): value is WebSearchIssue {
  if (!value || typeof value !== 'object') return false
  const issue = value as Record<string, unknown>
  return (
    webSearchFields.includes(issue.field as WebSearchIssue['field']) &&
    ['required', 'invalid_value'].includes(String(issue.reason))
  )
}
export function getWebSearchIssues(value: Record<string, unknown>): WebSearchIssue[] {
  const issues: WebSearchIssue[] = []
  const html = ['bing', 'google', 'sogou', 'duckduckgo']
  if (
    (html.includes(String(value.provider)) || html.includes(String(value.fallbackProvider))) &&
    value.allowExperimentalHtml !== true
  )
    issues.push({ field: 'allowExperimentalHtml', reason: 'required' })
  for (const [key, clear] of [
    ['tavilyKey', 'clearTavilyKey'],
    ['youKey', 'clearYouKey'],
    ['deepseekKey', 'clearDeepseekKey'],
  ] as const)
    if (value[key] && value[clear]) issues.push({ field: key, reason: 'invalid_value' })
  return issues
}
