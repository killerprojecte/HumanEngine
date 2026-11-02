import { handleEngineRequest } from '../utils/engine'
import { requestAbort } from '../utils/requestAbort'
import { createSessionGateway } from '../../src/auth/session'

const sessionGateway = createSessionGateway()
export default defineEventHandler(async (event) => {
  // Do not infer trusted TLS from arbitrary client-supplied forwarding headers.
  const request = new Request(
    getRequestURL(event, { xForwardedProto: false, xForwardedHost: false }),
    toWebRequest(event),
  )
  const cancellation = requestAbort(request, event.node)
  try {
    return await sessionGateway(
      new Request(request, { signal: cancellation.signal }),
      handleEngineRequest,
      {
        token: process.env.HUMANENGINE_ADMIN_TOKEN,
        secure: process.env.HUMANENGINE_SECURE_COOKIES === 'true',
      },
    )
  } finally {
    cancellation.dispose()
  }
})
