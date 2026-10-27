import test from 'node:test'
import assert from 'node:assert/strict'
import { AdminSessions, createSessionGateway, SESSION_TTL_MS } from '../src/auth/session.ts'

const origin = 'http://192.168.1.20:3000'
const token = 'local-fixture-only'
const headers = { 'X-HumanEngine-Session': '1', Origin: origin, 'Content-Type': 'application/json' }
function fixture() {
  let now = 1000
  const sessions = new AdminSessions(() => now)
  const gateway = createSessionGateway(sessions)
  const downstream = async (request: Request) => new Response(request.headers.get('authorization'))
  const request = (path: string, init: RequestInit = {}, options = { token }) =>
    gateway(new Request(origin + path, init), downstream, options)
  const login = (cookie = '') =>
    request('/api/auth/session', {
      method: 'POST',
      headers: { ...headers, Cookie: cookie },
      body: JSON.stringify({ token }),
    })
  return {
    request,
    login,
    expire: () => {
      now += SESSION_TTL_MS
    },
  }
}
const cookieOf = (response: Response) => response.headers.get('set-cookie')!.split(';')[0]!

test('HTTP LAN session survives fresh requests, rotates on login, logs out across tabs and expires absolutely', async () => {
  const f = fixture()
  const login = await f.login()
  assert.equal(login.status, 200)
  const setCookie = login.headers.get('set-cookie')!
  assert.match(setCookie, /HttpOnly; SameSite=Strict; Max-Age=604800/)
  assert.doesNotMatch(setCookie, /Secure|local-fixture-only/)
  const cookie = cookieOf(login)
  const browser = { ...headers, Cookie: cookie }
  assert.equal((await f.request('/api/auth/session', { headers: browser })).status, 200)
  assert.equal(
    await (await f.request('/api/models', { headers: browser })).text(),
    `Bearer ${token}`,
  )
  const rotated = cookieOf(await f.login(cookie))
  assert.notEqual(rotated, cookie)
  assert.equal((await f.request('/api/models', { headers: browser })).status, 401)
  const next = { ...headers, Cookie: rotated }
  assert.equal(
    (await f.request('/api/auth/session', { method: 'DELETE', headers: next })).status,
    200,
  )
  assert.equal((await f.request('/api/models', { headers: next })).status, 401)
  const expiry = { ...headers, Cookie: cookieOf(await f.login()) }
  f.expire()
  assert.equal((await f.request('/api/auth/session', { headers: expiry })).status, 401)
})

test('Cookie sessions reject CSRF, wrong credentials, token rotation and restarted server; bearer API remains independent', async () => {
  const f = fixture()
  const cookie = cookieOf(await f.login())
  for (const bad of [
    { Cookie: cookie },
    { ...headers, Cookie: cookie, Origin: 'http://evil.test' },
    { ...headers, Cookie: cookie, 'sec-fetch-site': 'cross-site' },
  ]) {
    assert.equal((await f.request('/api/models', { method: 'PUT', headers: bad })).status, 401)
    assert.equal(
      (await f.request('/api/auth/session', { method: 'DELETE', headers: bad })).status,
      403,
    )
  }
  assert.equal(
    (
      await f.request('/api/auth/session', {
        method: 'POST',
        headers,
        body: JSON.stringify({ token: 'wrong' }),
      })
    ).status,
    401,
  )
  assert.equal(
    (
      await f.request(
        '/api/auth/session',
        { headers: { ...headers, Cookie: cookie } },
        { token: 'changed' },
      )
    ).status,
    401,
  )
  assert.equal(
    (await fixture().request('/api/auth/session', { headers: { ...headers, Cookie: cookie } }))
      .status,
    401,
  )
  assert.equal(
    await (
      await f.request('/api/models', { headers: { Authorization: 'Bearer external' } })
    ).text(),
    'Bearer external',
  )
})

test('TLS cookie security is explicit, not enabled by spoofed forwarded headers', async () => {
  const gateway = createSessionGateway()
  for (const [url, secure, expected] of [
    [origin, false, false],
    ['https://admin.example', false, true],
    ['http://admin.example', true, true],
  ] as const) {
    const response = await gateway(
      new Request(url + '/api/auth/session', {
        method: 'POST',
        headers: {
          ...headers,
          Origin: expected ? 'https://admin.example' : origin,
          'X-Forwarded-Proto': 'https',
        },
        body: JSON.stringify({ token }),
      }),
      async () => new Response(),
      { token, secure },
    )
    assert.equal(response.status, 200)
    assert.equal(response.headers.get('set-cookie')!.includes('; Secure'), expected)
  }
})

test('Login bounds the actual body even with missing or misleading Content-Length', async () => {
  const f = fixture()
  for (const extra of [{}, { 'Content-Length': '1' }] as Record<string, string>[]) {
    const response = await f.request('/api/auth/session', {
      method: 'POST',
      headers: { ...headers, ...extra },
      body: JSON.stringify({ token, padding: 'x'.repeat(9000) }),
    })
    assert.equal(response.status, 413)
    assert.equal(response.headers.has('set-cookie'), false)
  }
  const staleBootstrap = await f.request('/api/auth/session', { headers })
  assert.equal(staleBootstrap.status, 401)
  assert.equal(
    staleBootstrap.headers.has('set-cookie'),
    false,
    'late bootstrap cannot clear a newer login cookie',
  )
})
