import { ensureAssertionEligibilityBaseline } from '../memory/recovery/eligibility.ts'
import { Store } from '../store.ts'
import { Engine } from '../engine.ts'
import { createApp } from '../api.ts'
import { startupDiagnostic, startupUnavailable } from '../diagnostics/startup.ts'
let pending:
  Promise<{ store: Store; engine: Engine; app: ReturnType<typeof createApp> }> | undefined
let disposal: Promise<void> | undefined
let closing: Promise<void> | undefined
async function closeStore(store: Store, phase: 'startup cleanup' | 'shutdown') {
  try {
    const delivery = await store.closeLogs()
    if (
      delivery.some((result) => !result.complete) ||
      store.loggingStatus.shutdownComplete === false
    )
      console.error(`[HumanEngine] Log delivery incomplete during ${phase}`)
  } catch {
    console.error(`[HumanEngine] Log delivery failed during ${phase}`)
  } finally {
    store.close()
  }
}
export function getEngine() {
  pending ??= (async () => {
    const store = new Store()
    let engine: Engine | undefined
    try {
      ensureAssertionEligibilityBaseline(store.db)
      engine = new Engine(store)
      await engine.start()
      return { store, engine, app: createApp(store, { engine }) }
    } catch (error) {
      try {
        await engine?.dispose()
      } finally {
        await closeStore(store, 'startup cleanup')
      }
      throw error
    }
  })()
  return pending
}

export async function initializeEngine() {
  try {
    await getEngine()
  } catch (error) {
    // Leave the rejected initialization cached until restart; requests cannot enroll,
    // retry migrations or start providers while an operator repairs recovery custody.
    console.error('[HumanEngine] Startup blocked:', startupDiagnostic(error))
  }
}

export async function handleEngineRequest(request: Request) {
  let runtime: Awaited<ReturnType<typeof getEngine>>
  try {
    runtime = await getEngine()
  } catch (error) {
    return startupUnavailable(request, error, process.env.HUMANENGINE_ADMIN_TOKEN)
  }
  return runtime.app.fetch(request)
}

/** Begin cancellation/disposal without closing SQLite. Worker shutdown starts this
 * before draining API handlers, because some routes use subsystem cancellation. */
export function startEngineDisposal(): Promise<void> {
  if (!pending) return Promise.resolve()
  disposal ??= (async () => {
    const runtime = await pending!.catch(() => undefined)
    if (runtime) await runtime.engine.dispose()
  })()
  return disposal
}

export function closeEngine(): Promise<void> {
  if (closing) return closing
  if (!pending) return Promise.resolve()
  closing = (async () => {
    const runtime = await pending!.catch(() => undefined)
    try {
      await startEngineDisposal()
    } finally {
      try {
        if (runtime) await closeStore(runtime.store, 'shutdown')
      } finally {
        pending = undefined
        disposal = undefined
        closing = undefined
      }
    }
  })()
  return closing
}
