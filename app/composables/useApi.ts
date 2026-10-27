import createClient from 'openapi-fetch'
import type { paths } from '~/types/api'

// Shared by composable instances in this browser document. Await each cookie-changing
// response before sending the next mutation; ignoring JS results cannot undo Set-Cookie.
let sessionMutation: Promise<unknown> = Promise.resolve()

export function useApi() {
  const authenticated = useState('adminAuthenticated', () => false)
  const authReady = useState('adminAuthReady', () => false)
  const authMessage = useState<'expired' | 'network' | ''>('adminAuthMessage', () => '')
  const authEpoch = useState('adminAuthEpoch', () => 0)
  const client = createClient<paths>({
    baseUrl: '',
    fetch: async (input) => {
      const epoch = authEpoch.value
      const request = new Request(input, { credentials: 'same-origin' })
      request.headers.set('X-HumanEngine-Session', '1')
      const response = await fetch(request)
      if (response.status === 401 && epoch === authEpoch.value && authenticated.value) {
        authenticated.value = false
        authMessage.value = 'expired'
        authEpoch.value++
      }
      return response
    },
  })
  async function session(method: 'GET' | 'POST' | 'DELETE', secret?: string) {
    const send = () =>
      fetch('/api/auth/session', {
        method,
        credentials: 'same-origin',
        cache: 'no-store',
        headers: {
          'X-HumanEngine-Session': '1',
          ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}),
        },
        ...(method === 'POST' ? { body: JSON.stringify({ token: secret }) } : {}),
      })
    if (method === 'GET') return send()
    const result = sessionMutation.then(send, send)
    sessionMutation = result.then(
      () => undefined,
      () => undefined,
    )
    return result
  }
  async function restoreSession() {
    const epoch = ++authEpoch.value
    try {
      const response = await session('GET')
      if (epoch !== authEpoch.value) return
      const wasAuthenticated = authenticated.value
      authenticated.value = response.ok
      authMessage.value = response.ok
        ? ''
        : response.status === 401
          ? wasAuthenticated
            ? 'expired'
            : ''
          : 'network'
    } catch {
      if (epoch === authEpoch.value) {
        authenticated.value = false
        authMessage.value = 'network'
      }
    } finally {
      if (epoch === authEpoch.value) authReady.value = true
    }
  }
  async function loginSession(secret: string) {
    const epoch = ++authEpoch.value
    const response = await session('POST', secret)
    if (epoch !== authEpoch.value) return false
    if (!response.ok) throw new Error(response.status === 401 ? 'invalid' : 'network')
    authenticated.value = true
    authReady.value = true
    authMessage.value = ''
    return true
  }
  async function logoutSession() {
    ++authEpoch.value
    // Hide protected content immediately; report failure rather than claiming revocation.
    authenticated.value = false
    const response = await session('DELETE')
    if (!response.ok) throw new Error('network')
    authMessage.value = ''
  }
  return {
    client,
    authenticated,
    authReady,
    authMessage,
    restoreSession,
    loginSession,
    logoutSession,
  }
}
