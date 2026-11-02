import type { IncomingMessage, ServerResponse } from 'node:http'

/** H3's Node toWebRequest does not attach a disconnect signal. Keep cancellation per request. */
export function requestAbort(
  request: Request,
  node: { req: IncomingMessage; res: ServerResponse },
) {
  const controller = new AbortController()
  const abort = () => controller.abort(new DOMException('Client disconnected', 'AbortError'))
  const close = () => {
    if (!node.res.writableEnded) abort()
  }
  node.req.once('aborted', abort)
  node.res.once('close', close)
  if (node.req.aborted || (node.res.destroyed && !node.res.writableEnded)) abort()
  return {
    signal: AbortSignal.any([request.signal, controller.signal]),
    dispose() {
      node.req.off('aborted', abort)
      node.res.off('close', close)
    },
  }
}
