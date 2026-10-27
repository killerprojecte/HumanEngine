/** Public diagnostic vocabulary. Never include submitted values or unknown property names. */
export const snowLumaFields = [
  'configuration',
  'transport',
  'url',
  'accountId',
  'token',
  'clearToken',
  'credentialEnv',
  'eventToken',
  'clearEventToken',
  'eventHost',
  'eventPort',
  'eventPath',
  'eventCredentialEnv',
] as const
export const snowLumaReasons = [
  'required',
  'invalid_type',
  'invalid_value',
  'invalid_url',
  'websocket_scheme_required',
  'http_scheme_required',
  'url_credentials_forbidden',
  'url_query_forbidden',
  'url_fragment_forbidden',
  'invalid_event_path',
  'invalid_port',
  'conflicting_credential_change',
] as const
export type SnowLumaIssue = {
  field: (typeof snowLumaFields)[number]
  reason: (typeof snowLumaReasons)[number]
}
export function isSnowLumaIssue(value: unknown): value is SnowLumaIssue {
  if (!value || typeof value !== 'object') return false
  const item = value as Record<string, unknown>
  return (
    snowLumaFields.includes(item.field as SnowLumaIssue['field']) &&
    snowLumaReasons.includes(item.reason as SnowLumaIssue['reason'])
  )
}
export function snowLumaValidationInput(value: Record<string, unknown>): Record<string, unknown> {
  const result = { ...value }
  if (value.transport === undefined || value.transport === 'websocket') {
    for (const key of [
      'eventHost',
      'eventPort',
      'eventPath',
      'eventCredentialEnv',
      'eventToken',
      'clearEventToken',
    ])
      delete result[key]
  }
  return result
}
export function getSnowLumaIssues(value: Record<string, unknown>): SnowLumaIssue[] {
  const issues: SnowLumaIssue[] = [],
    http = value.transport === 'http'
  const add = (field: SnowLumaIssue['field'], reason: SnowLumaIssue['reason']) =>
    issues.push({ field, reason })
  if (typeof value.accountId !== 'string' || !value.accountId.trim()) add('accountId', 'required')
  if (typeof value.url !== 'string' || !value.url.trim()) add('url', 'required')
  else {
    const scheme = http ? 'http_scheme_required' : 'websocket_scheme_required'
    if (!(http ? /^https?:\/\//i : /^wss?:\/\//i).test(value.url)) add('url', scheme)
    else {
      try {
        const url = new URL(value.url)
        if (!url.hostname) add('url', 'invalid_url')
        if (url.username || url.password) add('url', 'url_credentials_forbidden')
        if (url.search) add('url', 'url_query_forbidden')
        if (url.hash) add('url', 'url_fragment_forbidden')
      } catch {
        add('url', 'invalid_url')
      }
    }
  }
  if (value.clearToken && value.token) add('token', 'conflicting_credential_change')
  if (http) {
    if (value.clearEventToken && value.eventToken)
      add('eventToken', 'conflicting_credential_change')
    if (value.eventPort === undefined) add('eventPort', 'required')
    else if (
      typeof value.eventPort !== 'number' ||
      !Number.isInteger(value.eventPort) ||
      value.eventPort < 1 ||
      value.eventPort > 65535
    )
      add('eventPort', 'invalid_port')
    if (
      value.eventPath !== undefined &&
      (typeof value.eventPath !== 'string' ||
        !/^\/[a-zA-Z0-9/_-]+$/.test(value.eventPath) ||
        value.eventPath.includes('//'))
    )
      add('eventPath', 'invalid_event_path')
  }
  return issues
}
