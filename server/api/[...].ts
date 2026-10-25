import { getEngine } from '../utils/engine'
export default defineEventHandler(async (event) =>
  (await getEngine()).app.fetch(toWebRequest(event)),
)
