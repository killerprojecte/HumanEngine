import { timingSafeEqual } from 'node:crypto'

const legacy =
  'Stop all writers, preserve a private cold backup, then explicitly enroll only the current live database offline. See docs/recovery-authority.md. Never enroll an old backup.'
const custody =
  'Keep the service stopped and retain the database and original independent authority. Check authority mounts and the documented recovery procedure; do not delete, replace, or re-enroll recovery state.'
const recoveryCodes = new Set([
  'recovery_authority_missing',
  'recovery_authority_invalid',
  'recovery_authority_head_changed',
  'recovery_gate_invalid',
  'recovery_quarantined_or_unsupported',
  'recovery_format_unsupported',
  'recovery_chain_invalid',
  'recovery_authority_not_independent',
  'recovery_authority_path_required',
  'recovery_symlink_denied',
  'recovery_existing_authority_requires_reconciliation',
])

/** Startup can fail before Store-backed diagnostics exist. Never emit raw errors or paths. */
export function startupDiagnostic(error: unknown) {
  const code = error instanceof Error ? error.message : ''
  if (code === 'recovery_legacy_enrollment_required') return { code, action: legacy }
  if (recoveryCodes.has(code)) return { code, action: custody }
  return {
    code: 'engine_startup_failed',
    action:
      'Keep the service stopped and inspect database, authority and filesystem configuration locally. Do not disclose credentials or bypass recovery checks.',
  }
}

export function startupUnavailable(request: Request, error: unknown, token?: string): Response {
  const json = (status: number, body: object) =>
    new Response(JSON.stringify(body), {
      status,
      headers: {
        'Content-Type': 'application/json',
        'Cache-Control': 'no-store',
        'X-Content-Type-Options': 'nosniff',
      },
    })
  // The session gateway forwards bearer requests for downstream verification. It does
  // not authenticate them itself, so startup errors must enforce the same boundary.
  if (!token) return json(503, { error: 'Administration token is not configured' })
  const supplied = Buffer.from(request.headers.get('Authorization')?.replace(/^Bearer /, '') ?? '')
  const expected = Buffer.from(token)
  if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected))
    return json(401, { error: 'Authentication required' })
  const diagnostic = startupDiagnostic(error)
  return json(503, { error: `${diagnostic.code}: ${diagnostic.action}` })
}
