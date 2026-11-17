// No provider/network/adapter effects. Proves the packaged real V1 worker path.
export default {
  manifest: {
    id: 'package-fixture',
    name: 'Package acceptance fixture',
    version: '1',
    capabilities: [],
    configSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  setup(ctx) {
    ctx.registerAdminAction('echo', async (input) => ({ input, packaged: true }))
  },
}
