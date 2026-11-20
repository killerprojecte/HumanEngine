import fs from 'node:fs'
import { AdminSessions, createSessionGateway, SESSION_TTL_MS } from '../../src/auth/session.ts'

const origin = 'http://fixture.invalid:3000'
const token = 'synthetic-browser-secret'
const endpoint = '/api/auth/session'
const target = new URL('../internal/httpapi/testdata/browser-session-source.json', import.meta.url)
let now = 1000
let gateway = createSessionGateway(new AdminSessions(() => now))
const browser = { 'X-HumanEngine-Session': '1', Origin: origin }
const names = new Map()
const downstream = async (request) => new Response(JSON.stringify({ bearer: request.headers.get('authorization') }))
const cases = [
  { name: 'bootstrap-no-cookie', browser: true },
  { name: 'head-method', browser: true, method: 'HEAD' },
  { name: 'missing-marker', method: 'POST', input: { token } },
  { name: 'origin-mismatch-login', browser: true, method: 'POST', input: { token }, headers: { Origin: 'https://other.invalid' } },
  { name: 'forwarded-origin-not-trusted', browser: true, method: 'POST', input: { token }, headers: { Origin: 'https://fixture.invalid:3000', 'X-Forwarded-Proto': 'https' } },
  { name: 'cross-site-login', browser: true, method: 'POST', input: { token }, headers: { 'Sec-Fetch-Site': 'cross-site' } },
  { name: 'wrong-token', browser: true, method: 'POST', input: { token: 'wrong' } },
  { name: 'bad-body', browser: true, method: 'POST', raw: '{' },
  { name: 'wrong-type', browser: true, method: 'POST', input: { token: [] } },
  { name: 'token-too-long', browser: true, method: 'POST', input: { token: '{astral-too-long}' } },
  { name: 'body-too-large', browser: true, method: 'POST', input: { token, pad: '{oversize}' }, headers: { 'Content-Length': '1' } },
  { name: 'length-too-large', browser: true, method: 'POST', input: { token }, headers: { 'Content-Length': '8193' } },
  { name: 'login', browser: true, method: 'POST', input: { token, ignored: 42 }, save: 'first' },
  { name: 'bootstrap-valid', browser: true, cookie: 'first' },
  { name: 'cookie-protected', browser: true, path: '/api/logs', cookie: 'first' },
  { name: 'no-credentials', path: '/api/logs' },
  { name: 'cross-site-protected', browser: true, path: '/api/logs', cookie: 'first', headers: { 'Sec-Fetch-Site': 'cross-site' } },
  { name: 'origin-mismatch-protected', browser: true, path: '/api/logs', cookie: 'first', headers: { Origin: 'https://other.invalid' } },
  { name: 'bearer-precedence', browser: true, path: '/api/logs', cookie: 'first', headers: { Authorization: 'Bearer synthetic-external' } },
  { name: 'empty-bearer-precedence', browser: true, path: '/api/logs', cookie: 'first', headers: { Authorization: '' } },
  { name: 'bad-logout-does-not-revoke', browser: true, method: 'DELETE', cookie: 'first', headers: { Origin: 'https://other.invalid' } },
  { name: 'still-valid', browser: true, cookie: 'first' },
  { name: 'rotation', browser: true, method: 'POST', cookie: 'first', input: { token }, save: 'second' },
  { name: 'old-cookie-revoked', browser: true, path: '/api/logs', cookie: 'first' },
  { name: 'logout', browser: true, method: 'DELETE', cookie: 'second' },
  { name: 'other-client-revoked', browser: true, path: '/api/logs', cookie: 'second' },
  { name: 'secure-flag-login', browser: true, secure: true, method: 'POST', input: { token }, headers: { Origin: 'https://fixture.invalid:3000' }, save: 'third' },
  { name: 'configured-token-changed', browser: true, cookie: 'third', configured: 'synthetic-rotation' },
  { name: 'tls-login', browser: true, tls: true, method: 'POST', input: { token }, headers: { Origin: 'https://fixture.invalid:3000' }, save: 'fourth' },
  { name: 'restart-invalidates', restart: true, browser: true, cookie: 'fourth' },
  { name: 'new-absolute-age', browser: true, method: 'POST', input: { token }, save: 'fifth' },
  { name: 'not-sliding-but-valid', browser: true, cookie: 'fifth', advance: SESSION_TTL_MS - 1 },
  { name: 'expires-at-original-deadline', browser: true, cookie: 'fifth', advance: 1 },
  { name: 'missing-token-bootstrap', browser: true, configured: '' },
  { name: 'missing-token-login', browser: true, method: 'POST', input: { token }, configured: '' },
  { name: 'missing-token-protected', browser: true, path: '/api/logs', configured: '' },
  { name: 'missing-token-logout', browser: true, method: 'DELETE', configured: '' },
  { name: 'noncanonical-endpoint', browser: true, path: '/api/auth/%73ession' },
]
for (const entry of cases) {
  if (entry.restart) gateway = createSessionGateway(new AdminSessions(() => now))
  now += entry.advance ?? 0
  const input = entry.input && {
    ...entry.input,
    ...(entry.input.token === '{astral-too-long}' ? { token: '😀'.repeat(1500) + 'x'.repeat(1097) } : {}),
    ...(entry.input.pad === '{oversize}' ? { pad: 'x'.repeat(9000) } : {}),
  }
  const response = await gateway(
    new Request((entry.tls ? origin.replace('http:', 'https:') : origin) + (entry.path ?? endpoint), {
      method: entry.method ?? 'GET',
      headers: { ...(entry.browser ? browser : {}), ...(entry.cookie ? { Cookie: names.get(entry.cookie) } : {}), ...entry.headers },
      body: entry.raw ?? (input === undefined ? undefined : JSON.stringify(input)),
    }),
    downstream,
    { token: entry.configured ?? token, secure: entry.secure },
  )
  let cookie = response.headers.get('set-cookie')
  if (entry.save) {
    if (!cookie) throw Error('login missing cookie')
    const first = cookie.split(';')[0]
    if (!/^humanengine-session=[A-Za-z0-9_-]{43}$/.test(first)) throw Error('invalid opaque cookie')
    names.set(entry.save, first)
    cookie = cookie.replace(first, 'humanengine-session={cookie}')
  }
  entry.want = {
    status: response.status, body: await response.json(),
    cookie, cache: response.headers.get('cache-control'),
    nosniff: response.headers.get('x-content-type-options'),
    contentType: response.headers.get('content-type'),
  }
}
const output = JSON.stringify({ origin, token, cases }) + '\n'
if (process.argv.includes('--check')) {
  if (fs.readFileSync(target, 'utf8') !== output) process.exitCode = 1
} else fs.writeFileSync(target, output)
