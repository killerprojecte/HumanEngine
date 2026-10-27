/** Public diagnostic vocabulary. Never expose arbitrary exception messages. */
export const pluginStartupErrorCodes = [
  'secret_key_unavailable',
  'secret_key_missing',
  'secret_key_permissions',
  'secret_key_invalid',
  'secret_decryption_failed',
  'plugin_setup_failed',
  'plugin_disabled',
  'invalid_plugin_configuration',
  'credential_not_configured',
  'event_credential_not_configured',
  'account_mismatch',
  'connection_failed',
  'connection_closed',
  'request_timeout',
  'request_cancelled',
  'adapter_not_connected',
  'adapter_disposed',
  'invalid_websocket_url',
  'invalid_http_url',
  'invalid_http_event_configuration',
  'http_event_listen_failed',
  'http_action_failed',
  'http_request_failed',
  'invalid_http_response',
  'http_response_too_large',
  'invalid_payload',
  'invalid_identifier',
  'unsupported',
  'invalid_action_parameters',
  'platform_action_failed',
] as const
export type PluginStartupErrorCode = (typeof pluginStartupErrorCodes)[number]
export function isPluginStartupErrorCode(value: unknown): value is PluginStartupErrorCode {
  return typeof value === 'string' && (pluginStartupErrorCodes as readonly string[]).includes(value)
}

export interface SafeNetworkDetails {
  systemCode?: string
  httpStatus?: number
}
const systemCodes = new Set([
  'ECONNREFUSED',
  'ECONNRESET',
  'ENOTFOUND',
  'EAI_AGAIN',
  'EHOSTUNREACH',
  'ENETUNREACH',
  'ETIMEDOUT',
  'EADDRINUSE',
  'EADDRNOTAVAIL',
  'EACCES',
  'EPERM',
  'EPIPE',
  'CERT_HAS_EXPIRED',
  'DEPTH_ZERO_SELF_SIGNED_CERT',
  'ERR_TLS_CERT_ALTNAME_INVALID',
  'UNABLE_TO_VERIFY_LEAF_SIGNATURE',
  'SELF_SIGNED_CERT_IN_CHAIN',
  'UND_ERR_CONNECT_TIMEOUT',
])
export function safeNetworkDetails(value: unknown): SafeNetworkDetails {
  if (!value || typeof value !== 'object') return {}
  const item = value as Record<string, unknown>
  const cause =
    item.cause && typeof item.cause === 'object' ? (item.cause as Record<string, unknown>) : {}
  const code = item.systemCode ?? item.code ?? cause.code
  return {
    ...(typeof code === 'string' && systemCodes.has(code) ? { systemCode: code } : {}),
    ...(typeof item.httpStatus === 'number' &&
    Number.isInteger(item.httpStatus) &&
    item.httpStatus >= 100 &&
    item.httpStatus <= 599
      ? { httpStatus: item.httpStatus }
      : {}),
  }
}
