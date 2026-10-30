import { initializeEngine, closeEngine } from '../utils/engine'
export default defineNitroPlugin(async (app) => {
  app.hooks.hook('close', closeEngine)
  await initializeEngine()
})
