// No provider/network/adapter effects. Proves the packaged real V1 worker path.
import { basename } from 'node:path'
import { fileURLToPath } from 'node:url'

const pluginId = basename(fileURLToPath(import.meta.url), '.mjs')

export default {
  manifest: {
    id: pluginId,
    name: 'Package acceptance fixture',
    version: '1',
    capabilities: [],
    configSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  setup(ctx) {
    ctx.registerAdminAction('echo', async (input) => ({ input, packaged: true, pluginId }))
  },
}
