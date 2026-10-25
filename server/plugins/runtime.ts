import { getEngine, closeEngine } from '../utils/engine'
export default defineNitroPlugin(async (app) => {
  await getEngine()
  app.hooks.hook('close', closeEngine)
})
