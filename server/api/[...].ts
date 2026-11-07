import { engineUnavailable, handleEngineRequest, publishEngineResponse } from '../utils/engine'
import { requestAbort } from '../utils/requestAbort'
import { createSessionGateway } from '../../src/auth/session'
import { bindRequestId, requestIdFor } from '../../src/diagnostics/errors'

const sessionGateway = createSessionGateway()
export default defineEventHandler(async (event) => {
  // Do not infer trusted TLS from arbitrary client-supplied forwarding headers.
  const request = new Request(
    getRequestURL(event, { xForwardedProto: false, xForwardedHost: false }),
    toWebRequest(event),
  )
  const requestId = requestIdFor(request)
  const cancellation = requestAbort(request, event.node)
  try {
    const response = await sessionGateway(
      new Request(request, { signal: cancellation.signal }),
      (forwarded) => {
        // Session authentication can clone the Request to attach its bearer token.
        bindRequestId(forwarded, requestId)
        return handleEngineRequest(forwarded)
      },
      {
        token: process.env.HUMANENGINE_ADMIN_TOKEN,
        secure: process.env.HUMANENGINE_SECURE_COOKIES === 'true',
      },
    )
    try {
      if (await publishEngineResponse(response, event.node.res)) return
    } catch {
      if (event.node.res.headersSent) {
        event.node.res.destroy()
        return
      }
      return engineUnavailable(request)
    }
    return response
  } finally {
    cancellation.dispose()
  }
})
