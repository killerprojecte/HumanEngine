import { Store } from '../../src/store'
import { Engine } from '../../src/engine'
import { createApp } from '../../src/api'
let pending:
  Promise<{ store: Store; engine: Engine; app: ReturnType<typeof createApp> }> | undefined
export function getEngine() {
  pending ??= (async () => {
    const store = new Store()
    const engine = new Engine(store)
    await engine.start()
    return { store, engine, app: createApp(store, { engine }) }
  })()
  return pending
}
export async function closeEngine() {
  if (!pending) return
  const { store, engine } = await pending
  await engine.dispose()
  store.close()
  pending = undefined
}
