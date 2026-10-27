import { OneBotAdapter, OneBotError, type OneBotCallbacks } from './transport.ts'
import { SnowLumaConfigSchema, snowLumaConfigSchema } from './config.ts'
import { PluginStartupError } from '../../manager.ts'
import type { LocalPlugin } from '../../types.ts'
import type { Runtime } from '../../../runtime.ts'
import type { Store } from '../../../store.ts'
import { untilAbort } from '../../adapters.ts'
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
  const credentialOperation = <T>(operation: () => T): T => {
    try {
      return operation()
    } catch (cause) {
      const code =
        cause instanceof Error &&
        [
          'secret_key_unavailable',
          'secret_key_missing',
          'secret_key_permissions',
          'secret_key_invalid',
          'secret_decryption_failed',
        ].includes(cause.message)
          ? cause.message
          : 'plugin_setup_failed'
      throw new PluginStartupError(code, {}, cause)
    }
  }
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
    configuredSecrets: () => ({
      token: store.secrets.has('plugin:snowluma', 'token'),
      eventToken: store.secrets.has('plugin:snowluma', 'eventToken'),
    }),
    prepareConfiguration: (input) => {
      const config = { ...input }
      for (const [key, clear, legacy] of [
        ['token', 'clearToken', 'credentialEnv'],
        ['eventToken', 'clearEventToken', 'eventCredentialEnv'],
      ] as const) {
        if (config[clear] === true) {
          credentialOperation(() =>
            store.secrets.apply('plugin:snowluma', key, { action: 'clear' }),
          )
          delete config[legacy]
        } else if (typeof config[key] === 'string' && config[key] !== '') {
          credentialOperation(() =>
            store.secrets.apply('plugin:snowluma', key, {
              action: 'set',
              value: config[key] as string,
            }),
          )
        }
        delete config[key]
        delete config[clear]
      }
      return config
    },
    validateConfiguration: (value) => {
      SnowLumaConfigSchema.parse(value)
    },
    setup: async (context) => {
      const config = SnowLumaConfigSchema.parse(context.config)
      const token =
        credentialOperation(() => store.secrets.get('plugin:snowluma', 'token')) ??
        (config.credentialEnv ? process.env[config.credentialEnv] : undefined)
      if (!token) throw new PluginStartupError('credential_not_configured')
      const eventToken =
        credentialOperation(() => store.secrets.get('plugin:snowluma', 'eventToken')) ??
        (config.eventCredentialEnv ? process.env[config.eventCredentialEnv] : undefined)
      if (config.transport === 'http' && !eventToken)
        throw new PluginStartupError('event_credential_not_configured')
      const active = () => !context.signal.aborted
      const adapter = new OneBotAdapter(
        { id: 'snowluma', ...config, credentialEnv: config.credentialEnv ?? '', token, eventToken },
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
            if (active())
              store.log('info', 'snowluma', status, {
                requestId: context.requestId,
                operation: 'snowluma.connect',
                phase: status,
              })
          },
          onError: (code, details) => {
            if (active())
              store.log('error', 'snowluma', code, {
                requestId: context.requestId,
                operation: 'snowluma.connect',
                phase: 'transport',
                ...details,
              })
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
      try {
        await untilAbort(adapter.connect(), context.signal)
      } catch (cause) {
        // OneBotError carries fixed local codes, never upstream messages or credentials.
        if (cause instanceof OneBotError)
          throw new PluginStartupError(cause.code, cause.details, cause)
        throw cause
      }
      if (!active()) {
        stop()
        throw new Error('plugin_disabled')
      }
    },
  }
}
