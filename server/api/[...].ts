import { createApp } from '../../src/api'
import { Store } from '../../src/store'
let app: ReturnType<typeof createApp> | undefined
export default defineEventHandler(event => {
  app ??= createApp(new Store())
  return app.fetch(toWebRequest(event))
})
