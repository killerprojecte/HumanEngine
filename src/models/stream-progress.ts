/** Managed deadlines can stop after meaningful native stream content without weakening caller cancellation. */
const progress = new WeakMap<AbortSignal, () => void>()
export function signalStreamProgress(signal?: AbortSignal) {
  if (signal && !signal.aborted) progress.get(signal)?.()
}
export function progressDeadline(
  parent: AbortSignal | undefined,
  timeoutMs: number,
  reason: () => unknown,
) {
  const controller = new AbortController()
  const signal = parent ? AbortSignal.any([parent, controller.signal]) : controller.signal
  let timer: ReturnType<typeof setTimeout> | undefined = setTimeout(
    () => controller.abort(reason()),
    timeoutMs,
  )
  const clear = () => {
    if (timer) clearTimeout(timer)
    timer = undefined
  }
  const dispose = () => {
    clear()
    progress.delete(signal)
    parent?.removeEventListener('abort', dispose)
  }
  progress.set(signal, () => {
    clear()
    signalStreamProgress(parent)
  })
  parent?.addEventListener('abort', dispose, { once: true })
  if (parent?.aborted) dispose()
  return { signal, dispose }
}
