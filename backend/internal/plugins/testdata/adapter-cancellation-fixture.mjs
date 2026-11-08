export default {
  manifest: {
    id: 'adapter-calls',
    configSchema: { type: 'object', additionalProperties: true },
    capabilities: ['adapter'],
  },
  setup(ctx) {
    let api,
      sends = 0,
      timer
    const scope = () => api.scene('synthetic-account', 'group', 'synthetic-room')
    const message = (raw) => {
      const scene = scope()
      return {
        id: api.messageId(scene, raw),
        sceneId: scene.id,
        senderId: 'synthetic-user',
        sentAt: '2026-01-01T00:00:00Z',
        receivedAt: '2026-01-01T00:00:00Z',
        origin: 'live',
        status: 'available',
        parts: [],
      }
    }
    ctx.registerAdminAction('scene', () => scope())
    ctx.registerAdminAction('counts', () => sends)
    ctx.registerAdminAction('admin-media', async () => {
      await Promise.resolve()
      return api.media('blocked', scope())
    })
    ctx.registerAdapter('a', (c) => {
      api = c
      return {
        start() {
          c.status('connected')
          if (ctx.config.background)
            timer = setInterval(() => c.receive(message('background'), scope(), false), 2)
        },
        async send(scene, segment) {
          sends++
          await Promise.resolve()
          const mode = segment.parts[0]?.text
          if (mode === 'receive') c.receive(message('background'), scene, false)
          else if (mode === 'edit')
            c.edit(message('edit'), scene, {
              editId: 'synthetic-edit',
              order: { kind: 'platform_sequence', sequence: 1 },
            })
          else c.media(mode === 'healthy' ? 'healthy' : 'blocked', scene)
          return { status: 'sent' }
        },
        async history() {
          await Promise.resolve()
          return { coverage: 'complete', items: [message('history')] }
        },
        async getMessage() {
          await Promise.resolve()
          return { coverage: 'complete', items: [message('history')] }
        },
        dispose() {
          clearInterval(timer)
        },
      }
    })
  },
}
