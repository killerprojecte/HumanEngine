import { Store } from '../../src/store.ts'
import { Engine } from '../../src/engine.ts'
import { createApp } from '../../src/api.ts'
import { startupDiagnostic, startupUnavailable } from '../../src/diagnostics/startup.ts'
let pending:
  Promise<{ store: Store; engine: Engine; app: ReturnType<typeof createApp> }> | undefined
export function getEngine() {
  pending ??= (async () => {
    const store = new Store()
    let engine: Engine | undefined
    try {
      engine = new Engine(store)
      await engine.start()
      return { store, engine, app: createApp(store, { engine }) }
    } catch (error) {
      try {
        await engine?.dispose()
      } finally {
        store.close()
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

export async function closeEngine() {
  if (!pending) return
  const runtime = await pending.catch(() => undefined)
  try {
    if (runtime) await runtime.engine.dispose()
  } finally {
    runtime?.store.close()
    pending = undefined
  }
}
