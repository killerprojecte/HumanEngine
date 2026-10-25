// Trusted local plugin example. No platform sends or persistent learning.
export default {
  manifest: {
    id: 'example',
    name: 'Local example',
    version: '0.1.0',
    description: 'Demonstrates schema configuration and a custom administration page.',
    capabilities: ['schema-form', 'admin-page'],
    configSchema: {
      type: 'object',
      properties: {
        greeting: {
          type: 'string',
          title: 'Greeting',
          default: 'Hello from a local plugin',
          minLength: 1,
        },
      },
      required: ['greeting'],
      additionalProperties: false,
    },
  },
  setup(context) {
    context.registerAdminPage({
      id: 'status',
      title: 'Example page',
      entry: '/plugins/example/status.html',
    })
    context.registerAdminAction('status', async () => ({ greeting: context.config.greeting }))
  },
}
