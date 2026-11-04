import type { Dispatcher } from 'undici'

/** Disable only the streaming response body's implicit idle timer, never global networking. */
export async function nativeStreamDispatcher(): Promise<Pick<Dispatcher, 'dispatch'>> {
  // Standard lazy interface initialization makes Node establish its own default/proxy dispatcher
  // before the matching v7 API is loaded. v8's different global slot must not replace it.
  new Headers()
  const { getGlobalDispatcher } = await import('undici')
  const upstream = getGlobalDispatcher()
  return {
    dispatch(options, handler) {
      // Borrow the current dispatcher without owning/destroying its pools or changing proxy policy.
      // Caller cancellation and our pre-content deadline still own the request lifetime.
      return upstream.dispatch({ ...options, bodyTimeout: 0 }, handler)
    },
  }
}
