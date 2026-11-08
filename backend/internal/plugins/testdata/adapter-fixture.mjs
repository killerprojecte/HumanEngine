import { appendFileSync } from 'node:fs'
export default {
  manifest: {
    id: 'adapter-fixture',
    name: 'Synthetic adapter',
    version: '1',
    configSchema: { type: 'object', additionalProperties: true },
    capabilities: ['adapter'],
  },
  async setup(ctx) {
    const trace = [],
      deferred = []
    let api,
      transport,
      sends = 0
    const record = (v) => {
      trace.push(v)
      if (ctx.config.trace) appendFileSync(ctx.config.trace, v + '\n')
    }
    ctx.onDispose(async () => {
      await Promise.resolve()
      record('setup.dispose')
    })
    ctx.registerAdminAction('inspect', async () => ({ trace, sends }))
    ctx.registerAdminAction('release', async () => {
      for (const release of deferred.splice(0)) release()
      return true
    })
    ctx.registerAdminAction('context', async (op) => {
      const scene = api.scene('synthetic-account', 'group', 'synthetic-room')
      const id = api.messageId(scene, 'synthetic-message')
      const message = {
        id,
        sceneId: scene.id,
        senderId: 'synthetic-user',
        sentAt: '2026-01-01T00:00:00Z',
        receivedAt: '2026-01-01T00:00:00Z',
        origin: 'live',
        status: 'available',
        parts: [{ type: 'text', text: 'synthetic' }],
        ignored: 'omit',
      }
      if (op === 'scene') return { scene, message }
      if (op === 'media') return api.media('synthetic-asset', scene)
      if (op === 'recall') {
        api.recall(scene, id)
        return api.isRecalled(scene, id)
      }
      if (op === 'edit')
        return api.edit(message, scene, {
          editId: 'synthetic-edit',
          order: { kind: 'platform_sequence', sequence: 2 },
        })
      if (op === 'receive') {
        api.receive(message, scene, true)
        return true
      }
      if (op === 'invalid') {
        const capture = (fn) => {
          try {
            fn()
            return 'accepted'
          } catch (error) {
            return error.message
          }
        }
        return [
          capture(() => api.scene('', 'group', 'x')),
          capture(() => api.messageId({ ...scene, id: 'forged' }, 'x')),
          capture(() => api.receive(message, scene)),
          capture(() => api.receive({ ...message, origin: 'history' }, scene, false)),
          capture(() =>
            api.receive(
              {
                ...message,
                parts: Array(65).fill({ type: 'text', text: 'x' }),
              },
              scene,
              false,
            ),
          ),
          capture(() => api.recall(scene, 'wrong:message')),
        ]
      }
      throw Error('unknown_fixture_operation')
    })
    ctx.registerAdapter('transport', (context) => {
      api = context
      record('factory')
      context.status('connecting')
      const scene = context.scene('synthetic-account', 'group', 'synthetic-room')
      const message = (raw) => ({
        id: context.messageId(scene, raw),
        sceneId: scene.id,
        senderId: 'synthetic-user',
        sentAt: '2026-01-01T00:00:00Z',
        receivedAt: '2026-01-01T00:00:00Z',
        origin: 'live',
        status: 'available',
        parts: [{ type: 'text', text: raw }],
        ignored: 'omit',
      })
      transport = {
        async start() {
          if (this !== transport) throw Error('start_receiver_lost')
          record('start')
          context.status('connected')
          if (ctx.config.startPending)
            await new Promise((resolve) =>
              context.signal.addEventListener('abort', resolve, { once: true }),
            )
          else context.receive(message('synthetic-message'), scene, false)
        },
        async send(scope, segment, signal, beforeTransmit) {
          if (this !== transport || !Object.isFrozen(scope) || !(signal instanceof AbortSignal))
            throw Error('send_contract_lost')
          sends++
          const mode = segment.parts[0]?.text
          if (mode === 'pending')
            await new Promise((resolve) => {
              deferred.push(resolve)
              signal.addEventListener('abort', () => record('send.aborted'), {
                once: true,
              })
            })
          if (mode === 'throw') throw Error('synthetic_send_failure')
          if (mode === 'invalid') return { status: 'sent', messageId: 'foreign:receipt' }
          beforeTransmit?.()
          return {
            status: 'sent',
            messageId: context.messageId(scope, 'synthetic-sent'),
            detail: mode === 'private-detail' ? 'secret arbitrary detail' : 'send_failed',
          }
        },
        async history(scope, input, signal) {
          if (this !== transport || !Object.isFrozen(scope) || !(signal instanceof AbortSignal))
            throw Error('history_contract_lost')
          if (input.cursor === 'pending') await new Promise((resolve) => deferred.push(resolve))
          if (input.cursor === 'invalid') return { coverage: 'bad', items: [] }
          return {
            coverage: 'complete',
            items: [message('synthetic-message'), message('other')],
            cursor: input.cursor ?? null,
          }
        },
        async getMessage(scope, id, signal) {
          if (this !== transport || !Object.isFrozen(scope) || !(signal instanceof AbortSignal))
            throw Error('get_receiver_lost')
          return {
            coverage: 'complete',
            items: [message('synthetic-message'), message('other')],
          }
        },
        async readImage() {
          throw Error('legacy_must_not_expose_readImage')
        },
        dispose() {
          if (this !== transport) throw Error('dispose_receiver_lost')
          record('adapter.dispose')
        },
      }
      return transport
    })
    await Promise.resolve()
    record('setup.complete')
  },
}
