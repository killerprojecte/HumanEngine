import { getSnowLumaIssues, snowLumaFields, type SnowLumaIssue } from './validation.ts'
import { OneBotAdapter, OneBotError, type OneBotCallbacks } from './transport.ts'
import { SnowLumaConfigSchema, snowLumaConfigSchema } from './config.ts'
import { PluginConfigurationError, PluginStartupError } from '../../manager.ts'
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
      const http = config.transport === 'http'
      if (!http) {
        const previous = store.get<Record<string, unknown>>('plugin:snowluma', {})
        for (const key of ['eventHost', 'eventPort', 'eventPath', 'eventCredentialEnv']) {
          if (key in previous) config[key] = previous[key]
          else delete config[key]
        }
        delete config.eventToken
        delete config.clearEventToken
      }
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
      const issues = getSnowLumaIssues(value)
      const parsed = SnowLumaConfigSchema.safeParse(value)
      if (!parsed.success)
        for (const issue of parsed.error.issues) {
          const name = issue.path[0]
          const field =
            typeof name === 'string' && snowLumaFields.includes(name as SnowLumaIssue['field'])
              ? (name as SnowLumaIssue['field'])
              : 'configuration'
          if (!issues.some((item) => item.field === field))
            issues.push({
              field,
              reason:
                issue.code === 'invalid_type'
                  ? value[field] === undefined
                    ? 'required'
                    : 'invalid_type'
                  : 'invalid_value',
            })
        }
      if (issues.length) throw new PluginConfigurationError(issues)
      return parsed.success ? parsed.data : undefined
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
          onProfile: (profile) => {
            if (active()) store.updateIdentity(profile)
          },
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
              store.log(
                code === 'profile_lookup_unavailable' ? 'warn' : 'error',
                'snowluma',
                code,
                {
                  requestId: context.requestId,
                  operation:
                    code === 'profile_lookup_unavailable' ? 'snowluma.profile' : 'snowluma.connect',
                  phase: code === 'profile_lookup_unavailable' ? 'lookup' : 'transport',
                  ...details,
                },
              )
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
