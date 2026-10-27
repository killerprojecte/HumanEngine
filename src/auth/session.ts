import { createHash, randomBytes, timingSafeEqual } from 'node:crypto'

const COOKIE = 'humanengine-session'
export const SESSION_TTL_MS = 7 * 24 * 60 * 60 * 1000
const SESSION_HEADER = 'X-HumanEngine-Session'
const fingerprint = (value: string) => createHash('sha256').update(value).digest('hex')

/** Process-local, opaque sessions. A restart or admin-token change requires login again. */
export class AdminSessions {
  private sessions = new Map<string, { expiresAt: number; credential: string }>()
  constructor(private now = Date.now) {}
  create(token: string) {
    this.prune()
    // Bound memory without extending any existing session's lifetime.
    if (this.sessions.size >= 1024) this.sessions.delete(this.sessions.keys().next().value!)
    const id = randomBytes(32).toString('base64url')
    const expiresAt = this.now() + SESSION_TTL_MS
    this.sessions.set(fingerprint(id), { expiresAt, credential: fingerprint(token) })
    return { id, expiresAt }
  }
  get(id: string, token: string) {
    const session = this.sessions.get(fingerprint(id))
    if (!session || session.expiresAt <= this.now() || session.credential !== fingerprint(token)) {
      this.revoke(id)
      return undefined
    }
    return session
  }
  revoke(id: string) {
    this.sessions.delete(fingerprint(id))
  }
  private prune() {
    for (const [id, session] of this.sessions)
      if (session.expiresAt <= this.now()) this.sessions.delete(id)
  }
}

export function createSessionGateway(sessions = new AdminSessions()) {
  return async function sessionGateway(
    request: Request,
    downstream: (request: Request) => Promise<Response>,
    options: { token?: string; secure?: boolean } = {},
  ): Promise<Response> {
    const url = new URL(request.url)
    const id =
      request.headers
        .get('cookie')
        ?.split(';')
        .map((part) => part.trim())
        .find((part) => part.startsWith(`${COOKIE}=`))
        ?.slice(COOKIE.length + 1) ?? ''
    const secure = options.secure === true || url.protocol === 'https:'
    const cookie = (value: string, age: number) =>
      `${COOKIE}=${value}; Path=/api; HttpOnly; SameSite=Strict; Max-Age=${age}${secure ? '; Secure' : ''}`
    const reply = (status: number, body: object, setCookie?: string) =>
      new Response(JSON.stringify(body), {
        status,
        headers: {
          'Content-Type': 'application/json',
          'Cache-Control': 'no-store',
          'X-Content-Type-Options': 'nosniff',
          ...(setCookie ? { 'Set-Cookie': setCookie } : {}),
        },
      })
    const endpoint = url.pathname === '/api/auth/session'
    // A custom header is required even for reads. Cross-origin forms cannot add it;
    // this gateway never grants CORS. Reject cross-origin browser requests as well.
    const origin = request.headers.get('origin')
    const sameOrigin = !origin || origin === `${secure ? 'https:' : url.protocol}//${url.host}`
    const browserRequest =
      request.headers.get(SESSION_HEADER) === '1' &&
      sameOrigin &&
      request.headers.get('sec-fetch-site') !== 'cross-site'
    if (endpoint) {
      if (!browserRequest) return reply(403, { error: 'Session request rejected' })
      if (request.method === 'DELETE') {
        sessions.revoke(id)
        return reply(200, { authenticated: false }, cookie('', 0))
      }
      if (!options.token) return reply(503, { error: 'Administration token is not configured' })
      if (request.method === 'POST') {
        let input: unknown
        try {
          if (Number(request.headers.get('content-length') ?? 0) > 8192)
            return reply(413, { error: 'Invalid request' })
          const reader = request.body?.getReader()
          const chunks: Uint8Array[] = []
          let length = 0
          if (reader) {
            try {
              while (true) {
                const chunk = await reader.read()
                if (chunk.done) break
                length += chunk.value.byteLength
                if (length > 8192) {
                  await reader.cancel()
                  return reply(413, { error: 'Invalid request' })
                }
                chunks.push(chunk.value)
              }
            } finally {
              reader.releaseLock()
            }
          }
          input = JSON.parse(Buffer.concat(chunks).toString('utf8'))
        } catch {
          return reply(400, { error: 'Invalid request' })
        }
        const supplied =
          typeof input === 'object' && input !== null && 'token' in input ? input.token : undefined
        if (typeof supplied !== 'string' || supplied.length > 4096)
          return reply(400, { error: 'Invalid request' })
        const a = Buffer.from(supplied),
          b = Buffer.from(options.token)
        if (a.length !== b.length || !timingSafeEqual(a, b))
          return reply(401, { error: 'Authentication required' })
        sessions.revoke(id)
        const session = sessions.create(options.token)
        return reply(
          200,
          { authenticated: true, expiresAt: session.expiresAt },
          cookie(session.id, SESSION_TTL_MS / 1000),
        )
      }
      if (request.method !== 'GET') return reply(405, { error: 'Method not allowed' })
      const session = sessions.get(id, options.token)
      return session
        ? reply(200, { authenticated: true, expiresAt: session.expiresAt })
        : reply(401, { error: 'Authentication required' })
    }
    // Preserve the documented bearer API for non-browser clients.
    if (request.headers.has('authorization')) return downstream(request)
    if (!browserRequest || !options.token || !sessions.get(id, options.token)) {
      return reply(401, { error: 'Authentication required' })
    }
    const headers = new Headers(request.headers)
    headers.set('Authorization', `Bearer ${options.token}`)
    return downstream(new Request(request, { headers }))
  }
}
