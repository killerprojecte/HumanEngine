import { sanitizeError } from '../diagnostics/errors.ts'
import { randomUUID } from 'node:crypto'
import type { LocalPlugin, PluginTool, AdminPage } from './types.ts'
import { validateConfig, redactConfig, preserveSecrets, publicSchema } from './config.ts'
import {
  isPluginStartupErrorCode,
  safeNetworkDetails,
  type SafeNetworkDetails,
  type PluginStartupErrorCode,
} from './errors.ts'
import {
  installAdapter,
  untilAbort,
  type AdapterFactory,
  type AdapterServices,
} from './adapters.ts'
import { ModelProtocolRegistry, type ModelProtocol } from '../model-protocols.ts'
import { pathToFileURL } from 'node:url'
import { resolve, relative } from 'node:path'
import { Store } from '../store.ts'
import {
  Runtime,
  type RuntimeExtension,
  type SendHook,
  type TurnContext,
  type RuntimeEvent,
} from '../runtime.ts'
import type { ToolDefinition } from '../models.ts'
import type { SendResult } from '../onebot.ts'
interface Loaded {
  plugin: LocalPlugin
  builtin: boolean
  controller: AbortController
  cleanups: Array<() => void | Promise<void>>
  pages: AdminPage[]
  actions: Map<string, (input: unknown) => Promise<unknown>>
  enabled: boolean
  desiredEnabled: boolean
  revision: number
  queue: Promise<void>
  error?: string
  errorRequestId?: string
}
export class PluginConfigurationError extends Error {
  requestId?: string
  constructor(readonly issues: Array<{ field: string; reason: string }> = []) {
    super('invalid_plugin_configuration')
    this.name = 'PluginConfigurationError'
  }
}
export class PluginStartupError extends Error {
  readonly code: PluginStartupErrorCode
  requestId?: string
  readonly details: SafeNetworkDetails
  constructor(code: string, details: SafeNetworkDetails = {}, cause?: unknown) {
    const safeCode = isPluginStartupErrorCode(code) ? code : 'plugin_setup_failed'
    super(safeCode, cause === undefined ? undefined : { cause })
    this.name = 'PluginStartupError'
    this.code = safeCode
    this.details = safeNetworkDetails(details)
  }
}
export class PluginManager {
  private readonly loaded = new Map<string, Loaded>()
  constructor(
    private readonly store: Store,
    private readonly runtime: Runtime,
    private readonly protocols = new ModelProtocolRegistry(),
    private readonly adapterServices: AdapterServices = {},
  ) {}
  list() {
    return [...this.loaded.values()].map((p) => ({
      ...p.plugin.manifest,
      configSchema: publicSchema(p.plugin.manifest.configSchema),
      builtin: p.builtin,
      enabled: p.enabled,
      configuredSecrets: p.plugin.configuredSecrets?.() ?? {},
      error: p.error,
      errorRequestId: p.errorRequestId,
      pages: structuredClone(p.pages),
    }))
  }
  config(id: string) {
    const item = this.require(id)
    return redactConfig(
      this.store.get<Record<string, unknown>>(`plugin:${id}`, {}),
      item.plugin.manifest.configSchema,
    ) as Record<string, unknown>
  }
  async register(plugin: LocalPlugin, options: { builtin?: boolean } = {}) {
    if (!/^[a-z0-9-]+$/.test(plugin.manifest.id) || this.loaded.has(plugin.manifest.id))
      throw new Error('invalid_plugin_id')
    this.loaded.set(plugin.manifest.id, {
      plugin,
      builtin: options.builtin === true,
      controller: new AbortController(),
      cleanups: [],
      pages: [],
      actions: new Map(),
      enabled: false,
      desiredEnabled: false,
      revision: 0,
      queue: Promise.resolve(),
    })
  }
  async loadLocal(entry: string) {
    const root = resolve('plugins'),
      file = resolve(root, entry)
    if (relative(root, file).startsWith('..') || !file.endsWith('.mjs'))
      throw new Error('invalid_local_plugin_path')
    const module = await import(pathToFileURL(file).href)
    await this.register(module.default as LocalPlugin)
  }
  async configure(id: string, config: Record<string, unknown>, requestId: string = randomUUID()) {
    const item = this.require(id)
    const incoming = structuredClone(config)
    this.validateConfiguration(
      item,
      preserveSecrets(
        incoming,
        this.store.get(`plugin:${id}`, {}),
        item.plugin.manifest.configSchema,
      ) as Record<string, unknown>,
    )
    const revision = ++item.revision
    item.controller.abort('plugin_reconfigured')
    return this.enqueue(item, async () => {
      const next = preserveSecrets(
        incoming,
        this.store.get(`plugin:${id}`, {}),
        item.plugin.manifest.configSchema,
      ) as Record<string, unknown>
      this.validateConfiguration(item, next)
      try {
        this.store.db.transaction(() => {
          const persisted = item.plugin.prepareConfiguration?.(next) ?? next
          this.store.set(`plugin:${id}`, persisted)
        })()
      } catch (cause) {
        await this.cleanup(item, id)
        item.desiredEnabled = false
        this.store.set(`plugin-enabled:${id}`, false)
        const failure =
          cause instanceof PluginStartupError
            ? cause
            : new PluginStartupError('plugin_setup_failed', {}, cause)
        failure.requestId = requestId
        item.error = failure.code
        item.errorRequestId = requestId
        this.store.log('error', `plugin:${id}`, failure.code, {
          requestId,
          operation: 'plugin.configure',
          phase: 'persist_configuration',
          ...failure.details,
          ...sanitizeError(cause),
        })
        throw failure
      }
      await this.cleanup(item, id)
      if (item.desiredEnabled && item.revision === revision)
        await this.start(item, id, revision, requestId)
      return this.config(id)
    })
  }
  async enable(id: string, requestId: string = randomUUID()) {
    const item = this.require(id),
      revision = item.revision
    item.desiredEnabled = true
    return this.enqueue(item, async () => {
      if (!item.desiredEnabled || item.revision !== revision)
        throw new PluginStartupError('plugin_disabled')
      if (!item.enabled) await this.start(item, id, revision, requestId)
    })
  }
  async disable(id: string, persist = true, requestId: string = randomUUID()) {
    const item = this.require(id)
    item.desiredEnabled = false
    item.revision++
    item.controller.abort('plugin_disabled')
    item.enabled = false
    if (persist) this.store.set(`plugin-enabled:${id}`, false)
    return this.enqueue(item, async () => {
      await this.cleanup(item, id)
      this.store.log('info', `plugin:${id}`, 'plugin_disabled', {
        requestId,
        operation: 'plugin.disable',
        phase: 'cleanup',
      })
    })
  }
  async action(id: string, name: string, input: unknown) {
    const item = this.require(id),
      controller = item.controller
    if (!item.enabled || controller.signal.aborted) throw new PluginStartupError('plugin_disabled')
    const action = item.actions.get(name)
    if (!action) throw new Error('action_missing')
    const result = await action(input)
    if (controller.signal.aborted) throw new PluginStartupError('plugin_disabled')
    return result
  }
  async dispose() {
    for (const id of this.loaded.keys()) await this.disable(id, false)
  }
  private async start(item: Loaded, id: string, revision: number, requestId: string) {
    const controller = new AbortController()
    item.controller = controller
    item.error = undefined
    item.errorRequestId = undefined
    this.store.log('info', `plugin:${id}`, 'plugin_starting', {
      requestId,
      operation: 'plugin.enable',
      phase: 'setup',
    })
    const listeners: Array<{
      type: RuntimeEvent['type']
      listener: (event: Readonly<RuntimeEvent>) => void | Promise<void>
    }> = []
    const protocols: Array<{ name: string; label: string; protocol: ModelProtocol }> = []
    const adapters: Array<{ name: string; factory: AdapterFactory }> = []
    const adapterStarts: Array<() => Promise<void>> = []
    const tools: PluginTool[] = [],
      hooks: Array<Omit<SendHook, 'id'>> = [],
      pages: AdminPage[] = [],
      actions = new Map<string, (input: unknown) => Promise<unknown>>()
    const current = () =>
      !controller.signal.aborted && item.revision === revision && item.desiredEnabled
    const guard = () => {
      if (!current()) throw new PluginStartupError('plugin_disabled')
    }
    const onDispose = (cleanup: () => void | Promise<void>) => {
      if (current()) item.cleanups.push(cleanup)
      else
        void Promise.resolve()
          .then(cleanup)
          .catch((error) =>
            this.store.log('error', `plugin:${id}`, 'late_dispose_failed', {
              requestId,
              operation: 'plugin.disable',
              phase: 'late_cleanup',
              ...sanitizeError(error),
            }),
          )
    }
    let abortListener: () => void = () => {}
    try {
      this.validateConfiguration(item, this.store.get(`plugin:${id}`, {}))
      guard()
      const setup = Promise.resolve().then(() => {
        guard()
        return item.plugin.setup({
          requestId,
          config: this.store.get(`plugin:${id}`, {}),
          signal: controller.signal,
          registerModelProtocol: (name, label, protocol) => {
            guard()
            if (
              !item.plugin.manifest.capabilities.includes('model-protocol') ||
              !/^[a-z0-9-]{1,32}$/.test(name) ||
              protocols.some((p) => p.name === name)
            )
              throw new Error('invalid_protocol_registration')
            protocols.push({ name, label, protocol })
          },
          registerAdapter: (name, factory) => {
            guard()
            if (
              !item.plugin.manifest.capabilities.includes('adapter') ||
              !/^[a-z0-9-]{1,32}$/.test(name) ||
              (id + ':' + name).length > 64 ||
              adapters.some((a) => a.name === name)
            )
              throw new Error('invalid_adapter_registration')
            adapters.push({ name, factory })
          },
          registerTool: (tool) => {
            guard()
            if (!tool.name.startsWith(`${id}.`) || tools.some((t) => t.name === tool.name))
              throw new Error('invalid_plugin_tool')
            tools.push(tool)
          },
          beforeSend: (hook) => {
            guard()
            hooks.push(hook)
          },
          registerAdminPage: (page) => {
            guard()
            if (
              !/^[a-z0-9-]+$/.test(page.id) ||
              !page.entry.startsWith(`/plugins/${id}/`) ||
              page.entry.includes('..') ||
              !/^\/[a-zA-Z0-9/_\-.]+$/.test(page.entry)
            )
              throw new Error('invalid_admin_page')
            pages.push(page)
          },
          registerAdminAction: (name, handler) => {
            guard()
            if (!/^[a-z0-9-]+$/.test(name)) throw new Error('invalid_admin_action')
            actions.set(name, handler)
          },
          on: (type, listener) => {
            guard()
            listeners.push({ type, listener })
          },
          onDispose,
          log: (code) => {
            if (current())
              this.store.log(
                'info',
                `plugin:${id}`,
                /^[a-z0-9_.-]{1,100}$/i.test(code) ? code : 'plugin_event',
                { requestId },
              )
          },
        })
      })
      const cancelled = new Promise<never>((_resolve, reject) => {
        abortListener = () => reject(new Error('plugin_disabled'))
        controller.signal.addEventListener('abort', abortListener, { once: true })
        if (controller.signal.aborted) abortListener()
      })
      await Promise.race([setup, cancelled])
      guard()
      for (const entry of protocols)
        item.cleanups.push(
          this.protocols.register(
            id + ':' + entry.name,
            entry.label,
            entry.protocol,
            controller.signal,
          ),
        )
      for (const entry of adapters) {
        guard()
        const installed = installAdapter(
          id + ':' + entry.name,
          entry.factory,
          this.runtime,
          this.store,
          controller.signal,
          this.adapterServices,
        )
        item.cleanups.push(installed.dispose)
        adapterStarts.push(installed.start)
      }
      for (const { type, listener } of listeners)
        item.cleanups.push(
          this.runtime.on(type, async (event) => {
            if (current()) await listener(event)
          }),
        )
      for (const [index, hook] of hooks.entries())
        item.cleanups.push(
          this.runtime.beforeSend({
            id: `${id}:${index}`,
            priority: hook.priority,
            handler: async (segments, context) => {
              const signal = AbortSignal.any([context.signal, controller.signal])
              if (signal.aborted || !current())
                return { action: 'cancel', reason: 'plugin_disabled' }
              const result = await hook.handler(segments, {
                ...context,
                scene: Object.freeze({ ...context.scene }),
                dependencies: new Set(context.dependencies),
                validations: [],
                signal,
              })
              return signal.aborted || !current()
                ? { action: 'cancel', reason: 'plugin_disabled' }
                : result
            },
          }),
        )
      const extension: RuntimeExtension = {
        tools,
        execute: async (name, input, context) => {
          guard()
          const tool = tools.find((t) => t.name === name)
          if (!tool) throw new Error('tool_missing')
          if (context.mode === 'simulation' && tool.effect === 'learningCandidate')
            return { error: 'Simulation does not execute plugin side effects' }
          validateConfig(input, tool.inputSchema)
          const lease = new AbortController()
          const signal = AbortSignal.any([context.signal, controller.signal, lease.signal])
          let executing = true
          try {
            const result = await tool.execute(structuredClone(input), {
              ...context,
              scene: Object.freeze({ ...context.scene }),
              dependencies: new Set(context.dependencies),
              validations: [],
              signal,
              send: (request) => {
                guard()
                if (!executing || signal.aborted || tool.effect !== 'send')
                  throw new Error('plugin_send_not_allowed')
                return this.runtime.sendFromPlugin(context, request, signal)
              },
            })
            if (signal.aborted) throw new PluginStartupError('plugin_disabled')
            guard()
            if (tool.outputSchema) validateConfig(result, tool.outputSchema)
            const encoded = JSON.stringify(result)
            if (encoded === undefined || Buffer.byteLength(encoded) > 1024 * 1024)
              throw new Error('invalid_plugin_output')
            return JSON.parse(encoded)
          } finally {
            executing = false
            lease.abort('tool_finished')
          }
        },
      }
      item.cleanups.push(this.runtime.addExtension(extension))
      for (const start of adapterStarts) {
        await untilAbort(start(), AbortSignal.any([controller.signal, AbortSignal.timeout(30000)]))
        guard()
      }
      item.pages = pages
      item.actions = actions
      item.enabled = true
      this.store.set(`plugin-enabled:${id}`, true)
      this.store.log('info', `plugin:${id}`, 'plugin_started', {
        requestId,
        operation: 'plugin.enable',
        phase: 'ready',
      })
    } catch (cause) {
      const cancelled = !current()
      const code =
        cause instanceof PluginConfigurationError
          ? 'invalid_plugin_configuration'
          : cause instanceof PluginStartupError
            ? cause.code
            : 'plugin_setup_failed'
      if (!cancelled) {
        item.error = code
        item.errorRequestId = requestId
        item.desiredEnabled = false
        this.store.set(`plugin-enabled:${id}`, false)
        this.store.log('error', `plugin:${id}`, code, {
          requestId,
          ...(cause instanceof PluginStartupError ? cause.details : {}),
          operation: 'plugin.enable',
          phase: 'setup',
          ...sanitizeError(cause),
        })
      }
      controller.abort('plugin_setup_stopped')
      await this.cleanup(item, id)
      if (!cancelled && cause instanceof PluginConfigurationError) {
        cause.requestId = requestId
        throw cause
      }
      const failure = new PluginStartupError(
        cancelled ? 'plugin_disabled' : code,
        cause instanceof PluginStartupError ? cause.details : {},
        cause,
      )
      failure.requestId = requestId
      if (cancelled) this.store.log('info', `plugin:${id}`, 'plugin_start_cancelled', { requestId })
      throw failure
    } finally {
      controller.signal.removeEventListener('abort', abortListener)
    }
  }
  private async cleanup(item: Loaded, id: string) {
    item.enabled = false
    item.controller.abort('plugin_stopped')
    item.pages = []
    item.actions.clear()
    const cleanups = item.cleanups.splice(0).reverse()
    for (const cleanup of cleanups) {
      try {
        await cleanup()
      } catch (error) {
        this.store.log('error', `plugin:${id}`, 'dispose_failed', {
          operation: 'plugin.disable',
          phase: 'cleanup',
          ...sanitizeError(error),
        })
      }
    }
  }
  private enqueue<T>(item: Loaded, operation: () => Promise<T>): Promise<T> {
    const result = item.queue.then(operation, operation)
    item.queue = result.then(
      () => {},
      () => {},
    )
    return result
  }
  private validateConfiguration(item: Loaded, config: Record<string, unknown>) {
    try {
      const normalized = item.plugin.validateConfiguration?.(config)
      validateConfig(normalized ?? config, item.plugin.manifest.configSchema)
    } catch (error) {
      if (error instanceof PluginConfigurationError) throw error
      throw new PluginConfigurationError()
    }
  }
  private require(id: string) {
    const item = this.loaded.get(id)
    if (!item) throw new Error('plugin_not_found')
    return item
  }
}
