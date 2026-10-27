import { z } from '@hono/zod-openapi'
import { OneBotAdapter, type OneBotCallbacks } from './onebot.ts'
import type { LocalPlugin } from './plugins.ts'
import type { Runtime } from './runtime.ts'
import type { Store } from './store.ts'
import { untilAbort } from './plugin-adapters.ts'
import { TypingDelayConfigSchema, typingDelayConfigSchema } from './typing-delay.ts'

const environmentName = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[A-Z][A-Z0-9_]*$/)
export const SnowLumaConfigSchema = z
  .object({
    transport: z
      .enum(['websocket', 'http'])
      .default('websocket')
      .describe('Omitted means legacy WebSocket.'),
    url: z.string().min(1).max(2048),
    accountId: z.string().min(1).max(256),
    credentialEnv: environmentName,
    eventHost: z.enum(['127.0.0.1', '::1', '0.0.0.0', '::']).default('127.0.0.1'),
    eventPort: z
      .number()
      .int()
      .min(1)
      .max(65535)
      .optional()
      .describe('Required for HTTP transport.'),
    eventPath: z
      .string()
      .min(1)
      .max(256)
      .regex(/^\/[a-zA-Z0-9/_-]+$/)
      .default('/onebot/events'),
    eventCredentialEnv: environmentName
      .optional()
      .describe(
        'Required for HTTP transport; SnowLuma httpClients HMAC signature secret environment variable.',
      ),
  })
  .strict()
  .superRefine((value, context) => {
    let url: URL
    try {
      url = new URL(value.url)
    } catch {
      context.addIssue({ code: 'custom', message: 'invalid_transport_url', path: ['url'] })
      return
    }
    const protocols = value.transport === 'http' ? ['http:', 'https:'] : ['ws:', 'wss:']
    if (!protocols.includes(url.protocol) || url.username || url.password || url.search || url.hash)
      context.addIssue({ code: 'custom', message: 'invalid_transport_url', path: ['url'] })
    if (value.eventPath?.includes('//'))
      context.addIssue({ code: 'custom', message: 'invalid_event_path', path: ['eventPath'] })
    if (value.transport === 'http' && (!value.eventPort || !value.eventCredentialEnv))
      context.addIssue({ code: 'custom', message: 'http_event_configuration_required' })
  })
export const snowLumaConfigSchema = z.toJSONSchema(SnowLumaConfigSchema, { io: 'input' })

export interface BuiltinServices {
  resolveMedia?: OneBotCallbacks['resolveMedia']
  adapterStopped(id: string): void
}
/** Trusted bundled code only. IDs and stored settings remain compatible with existing scenes. */
export function snowLumaPlugin(
  runtime: Runtime,
  store: Store,
  services: BuiltinServices,
): LocalPlugin {
  return {
    manifest: {
      id: 'snowluma',
      name: 'SnowLuma',
      version: '0.2.0',
      description:
        'Built-in SnowLuma adapter with WebSocket or HTTP actions and authenticated HTTP events.',
      capabilities: ['adapter', 'history', 'recall', 'schema-form'],
      configSchema: snowLumaConfigSchema,
    },
    validateConfiguration: (value) => {
      SnowLumaConfigSchema.parse(value)
    },
    setup: async (context) => {
      const config = SnowLumaConfigSchema.parse(context.config)
      const active = () => !context.signal.aborted
      const adapter = new OneBotAdapter(
        { id: 'snowluma', ...config },
        {
          resolveMedia: services.resolveMedia,
          onMessage: (message, scene) => {
            if (active())
              runtime.ingest(
                message.senderId === adapter.userId(config.accountId)
                  ? { ...message, senderId: 'bot' }
                  : message,
                scene,
              )
          },
          onRecall: (event) => {
            if (active()) runtime.recall(event.sceneId, event.messageId)
          },
          isRecalled: (sceneId, messageId) => store.recalled(sceneId, messageId),
          onStatus: (status) => {
            if (active()) store.log('info', 'snowluma', status)
          },
          onError: (code) => {
            if (active()) store.log('error', 'snowluma', code)
          },
        },
      )
      let stopped = false
      const stop = () => {
        if (stopped) return
        stopped = true
        runtime.removeAdapter('snowluma')
        adapter.dispose()
        services.adapterStopped('snowluma')
      }
      context.onDispose(() => {
        context.signal.removeEventListener('abort', stop)
        stop()
      })
      context.signal.addEventListener('abort', stop, { once: true })
      if (!active()) {
        stop()
        throw new Error('plugin_disabled')
      }
      runtime.addAdapter('snowluma', adapter, adapter.userId(config.accountId))
      await untilAbort(adapter.connect(), context.signal)
      if (!active()) {
        stop()
        throw new Error('plugin_disabled')
      }
    },
  }
}
export function typingDelayPlugin(runtime: Runtime): LocalPlugin {
  return {
    manifest: {
      id: 'typing-delay',
      name: 'Typing delay',
      version: '0.1.0',
      description:
        'Optional human typing delay for each complete outgoing message segment, not each sentence.',
      capabilities: ['send-delay', 'schema-form'],
      configSchema: typingDelayConfigSchema,
    },
    validateConfiguration: (value) => {
      TypingDelayConfigSchema.parse(value)
    },
    setup(context) {
      context.onDispose(
        runtime.installTypingDelay(TypingDelayConfigSchema.parse(context.config), context.signal),
      )
    },
  }
}
