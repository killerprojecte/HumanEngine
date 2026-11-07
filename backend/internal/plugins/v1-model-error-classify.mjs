// Generated from src/models.ts; SHA-256 61293616cd4cd978f44bc80ccfafe8ec95a1254d6e575ddb184bd10fcf91c8ca
function isReadAdmissionCode(code) {
  return (
    code === 'memory_read_disabled' ||
    code === 'memory_read_revoked' ||
    code === 'personalization_disabled'
  )
}
function captureProtocolFailure(code, status, retryable, options) {
  return { code, status, retryable, retryAfterMs: options.retryAfterMs }
}
function classifyProtocolFailure(error) {
  try {
    // invokeProtocol reads Error.message before checking the SDK constructor.
    const readCode = error instanceof Error ? error.message : ''
    if (!(error instanceof ModelProtocolError)) return undefined
    if (isReadAdmissionCode(readCode)) return { code: readCode, retryable: false }
    {
      const status = error.status
      return captureProtocolFailure(
        status
          ? `provider_http_${status}`
          : error.kind === 'configuration'
            ? 'protocol_configuration_error'
            : error.kind === 'timeout'
              ? 'provider_timeout'
              : 'provider_request_failed',
        status,
        error.kind !== 'configuration' &&
          error.kind !== 'timeout' &&
          (status ? status === 429 || status >= 500 : true),
        { cause: error, retryAfterMs: error.retryAfterMs },
      )
    }
  } catch {
    // Accessor/coercion failures escape the source catch and never grant retry.
    return undefined
  }
}
