import { getEngine } from '../utils/engine'
import { createSessionGateway } from '../../src/auth/session'

const sessionGateway = createSessionGateway()
export default defineEventHandler(async (event) => {
  // Do not infer trusted TLS from arbitrary client-supplied forwarding headers.
  const request = new Request(
    getRequestURL(event, { xForwardedProto: false, xForwardedHost: false }),
    toWebRequest(event),
  )
  return sessionGateway(request, async (authorized) => (await getEngine()).app.fetch(authorized), {
    token: process.env.HUMANENGINE_ADMIN_TOKEN,
    secure: process.env.HUMANENGINE_SECURE_COOKIES === 'true',
  })
})
