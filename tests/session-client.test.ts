import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

// Execute the actual composable with Nuxt state and transport replaced by local fixtures.
// No browser credentials or network services are used.
test('Session bootstrap and late responses cannot resurrect logout or overwrite a newer login', async () => {
  const state = new Map<string, { value: unknown }>()
  const pending: Array<(response: Response) => void> = []
  const sent: RequestInit[] = []
  const sentCookies: string[] = []
  let cookie = ''
  const flush = () => new Promise<void>((resolve) => setImmediate(resolve))
  const originalFetch = globalThis.fetch
  const globals = globalThis as typeof globalThis & {
    useState?: unknown
    sessionClientFixture?: unknown
  }
  const originalUseState = globals.useState
  let clientFetch: (request: Request) => Promise<Response>
  globals.useState = (key: string, init: () => unknown) => {
    if (!state.has(key)) state.set(key, { value: init() })
    return state.get(key)
  }
  globals.sessionClientFixture = (options: { fetch: typeof clientFetch }) => {
    clientFetch = options.fetch
    return {}
  }
  globalThis.fetch = async (_input, init) => {
    sent.push(init ?? {})
    sentCookies.push(cookie)
    return new Promise<Response>((resolve) => pending.push(resolve)).then((response) => {
      const next = response.headers.get('set-cookie')
      if (next !== null) cookie = next
      return response
    })
  }
  const response = (status: number) => new Response('{}', { status })
  try {
    const source = readFileSync(new URL('../app/composables/useApi.ts', import.meta.url), 'utf8')
    const compiled = ts
      .transpileModule(source, {
        compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
      })
      .outputText.replace(
        /import createClient from ['"]openapi-fetch['"];?/,
        'const createClient = globalThis.sessionClientFixture;',
      )
    const { useApi } = await import(
      `data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
    )
    const auth = useApi()
    const initial = auth.restoreSession()
    const login = auth.loginSession('fixture-only')
    await flush()
    pending[1]!(response(200))
    await login
    pending[0]!(response(401))
    await initial
    assert.equal(auth.authenticated.value, true)
    assert.equal(auth.authMessage.value, '')
    assert.equal(sent[1]!.credentials, 'same-origin')
    assert.equal(sent[1]!.body, JSON.stringify({ token: 'fixture-only' }))
    assert.equal(
      [...state.keys()].some((key) => /token/i.test(key)),
      false,
    )

    const staleApi = clientFetch!(new Request('http://fixture.test/api/status'))
    const refresh = auth.restoreSession()
    const logout = auth.logoutSession()
    assert.equal(auth.authenticated.value, false)
    await flush()
    pending[4]!(response(200))
    await logout
    pending[3]!(response(200))
    await refresh
    pending[2]!(response(200))
    await staleApi
    assert.equal(auth.authenticated.value, false)

    const nextLogin = auth.loginSession('fixture-only')
    await flush()
    pending[5]!(response(200))
    await nextLogin
    const expired = clientFetch!(new Request('http://fixture.test/api/status'))
    pending[6]!(response(401))
    await expired
    assert.equal(auth.authenticated.value, false)
    assert.equal(auth.authMessage.value, 'expired')

    const offline = auth.restoreSession()
    pending[7]!(response(503))
    await offline
    assert.equal(auth.authReady.value, true)
    assert.equal(auth.authMessage.value, 'network')

    // Simulate browser cookie application before fetch resolves, not merely JS state.
    const secondInstance = useApi()
    const pendingLogin = auth.loginSession('fixture-only')
    await flush()
    const logoutAfterLogin = secondInstance.logoutSession()
    await flush()
    assert.equal(sent.length, 9, 'DELETE waits for pending POST Set-Cookie')
    pending[8]!(new Response('{}', { headers: { 'Set-Cookie': 'session=new' } }))
    await pendingLogin
    await flush()
    assert.equal(sent[9]!.method, 'DELETE')
    assert.equal(sentCookies[9], 'session=new', 'logout revokes the newly issued session')
    pending[9]!(new Response('{}', { headers: { 'Set-Cookie': '' } }))
    await logoutAfterLogin
    assert.equal(cookie, '')
    assert.equal(auth.authenticated.value, false)

    const pendingLogout = auth.logoutSession()
    await flush()
    const loginAfterLogout = secondInstance.loginSession('fixture-only')
    await flush()
    assert.equal(sent.length, 11, 'POST waits for pending DELETE clearing cookie')
    pending[10]!(new Response('{}', { headers: { 'Set-Cookie': '' } }))
    await pendingLogout
    await flush()
    pending[11]!(new Response('{}', { headers: { 'Set-Cookie': 'session=latest' } }))
    await loginAfterLogout
    assert.equal(cookie, 'session=latest')
    assert.equal(auth.authenticated.value, true)
  } finally {
    globalThis.fetch = originalFetch
    globals.useState = originalUseState
    delete globals.sessionClientFixture
  }
})
