import { pathToFileURL } from 'node:url'
import { resolve, relative } from 'node:path'
import { Store } from './store.ts'
import {
  Runtime,
  type RuntimeExtension,
  type SendHook,
  type TurnContext,
  type RuntimeEvent,
} from './runtime.ts'
import type { ToolDefinition } from './models.ts'

export interface PluginTool extends ToolDefinition {
  effect: 'read' | 'learningCandidate' | 'send'
  execute(input: Record<string, unknown>, context: TurnContext): Promise<unknown>
}
export interface AdminPage {
  id: string
  title: string
  entry: string
}
export interface PluginManifest {
  id: string
  name: string
  version: string
  description: string
  configSchema: Record<string, unknown>
  capabilities: string[]
}
export interface PluginContext {
  config: Record<string, unknown>
  signal: AbortSignal
  registerTool(tool: PluginTool): void
  beforeSend(hook: Omit<SendHook, 'id'>): void
  registerAdminPage(page: AdminPage): void
  registerAdminAction(name: string, handler: (input: unknown) => Promise<unknown>): void
  on(
    type: RuntimeEvent['type'],
    listener: (event: Readonly<RuntimeEvent>) => void | Promise<void>,
  ): void
  onDispose(cleanup: () => void | Promise<void>): void
  log(code: string): void
}
export interface LocalPlugin {
  manifest: PluginManifest
  setup(context: PluginContext): void | Promise<void>
}
interface Loaded {
  plugin: LocalPlugin
  controller: AbortController
  cleanups: Array<() => void | Promise<void>>
  pages: AdminPage[]
  actions: Map<string, (input: unknown) => Promise<unknown>>
  enabled: boolean
  desiredEnabled: boolean
  revision: number
  queue: Promise<void>
  error?: string
}
export class PluginManager {
  private readonly loaded = new Map<string, Loaded>()
  constructor(
    private readonly store: Store,
    private readonly runtime: Runtime,
  ) {}
  list() {
    return [...this.loaded.values()].map((p) => ({
      ...p.plugin.manifest,
      configSchema: publicSchema(p.plugin.manifest.configSchema),
      enabled: p.enabled,
      error: p.error,
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
  async register(plugin: LocalPlugin) {
    if (!/^[a-z0-9-]+$/.test(plugin.manifest.id) || this.loaded.has(plugin.manifest.id))
      throw new Error('invalid_plugin_id')
    this.loaded.set(plugin.manifest.id, {
      plugin,
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
  async configure(id: string, config: Record<string, unknown>) {
    const item = this.require(id)
    const incoming = structuredClone(config)
    validateConfig(
      preserveSecrets(
        incoming,
        this.store.get(`plugin:${id}`, {}),
        item.plugin.manifest.configSchema,
      ),
      item.plugin.manifest.configSchema,
    )
    const revision = ++item.revision
    item.controller.abort('plugin_reconfigured')
    return this.enqueue(item, async () => {
      const next = preserveSecrets(
        incoming,
        this.store.get(`plugin:${id}`, {}),
        item.plugin.manifest.configSchema,
      ) as Record<string, unknown>
      validateConfig(next, item.plugin.manifest.configSchema)
      this.store.set(`plugin:${id}`, next)
      await this.cleanup(item, id)
      if (item.desiredEnabled && item.revision === revision) await this.start(item, id, revision)
      return this.config(id)
    })
  }
  async enable(id: string) {
    const item = this.require(id),
      revision = item.revision
    item.desiredEnabled = true
    return this.enqueue(item, async () => {
      if (!item.desiredEnabled || item.revision !== revision) throw new Error('plugin_disabled')
      if (!item.enabled) await this.start(item, id, revision)
    })
  }
  async disable(id: string, persist = true) {
    const item = this.require(id)
    item.desiredEnabled = false
    item.revision++
    item.controller.abort('plugin_disabled')
    item.enabled = false
    if (persist) this.store.set(`plugin-enabled:${id}`, false)
    return this.enqueue(item, () => this.cleanup(item, id))
  }
  async action(id: string, name: string, input: unknown) {
    const item = this.require(id),
      controller = item.controller
    if (!item.enabled || controller.signal.aborted) throw new Error('plugin_disabled')
    const action = item.actions.get(name)
    if (!action) throw new Error('action_missing')
    const result = await action(input)
    if (controller.signal.aborted) throw new Error('plugin_disabled')
    return result
  }
  async dispose() {
    for (const id of this.loaded.keys()) await this.disable(id, false)
  }
  private async start(item: Loaded, id: string, revision: number) {
    const controller = new AbortController()
    item.controller = controller
    item.error = undefined
    const listeners: Array<{
      type: RuntimeEvent['type']
      listener: (event: Readonly<RuntimeEvent>) => void | Promise<void>
    }> = []
    const tools: PluginTool[] = [],
      hooks: Array<Omit<SendHook, 'id'>> = [],
      pages: AdminPage[] = [],
      actions = new Map<string, (input: unknown) => Promise<unknown>>()
    const current = () =>
      !controller.signal.aborted && item.revision === revision && item.desiredEnabled
    const guard = () => {
      if (!current()) throw new Error('plugin_disabled')
    }
    const onDispose = (cleanup: () => void | Promise<void>) => {
      if (current()) item.cleanups.push(cleanup)
      else
        void Promise.resolve()
          .then(cleanup)
          .catch(() => this.store.log('error', `plugin:${id}`, 'late_dispose_failed'))
    }
    let abortListener: () => void = () => {}
    try {
      validateConfig(this.store.get(`plugin:${id}`, {}), item.plugin.manifest.configSchema)
      guard()
      const setup = Promise.resolve().then(() => {
        guard()
        return item.plugin.setup({
          config: this.store.get(`plugin:${id}`, {}),
          signal: controller.signal,
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
              const result = await hook.handler(segments, { ...context, signal })
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
          if (context.mode === 'simulation' && tool.effect !== 'read')
            return { error: 'Simulation does not execute plugin side effects' }
          if (tool.effect === 'send')
            throw new Error('Plugins must use the core send pipeline via hooks')
          validateConfig(input, tool.inputSchema)
          const signal = AbortSignal.any([context.signal, controller.signal])
          const result = await tool.execute(input, { ...context, signal })
          if (signal.aborted) throw new Error('plugin_disabled')
          guard()
          return result
        },
      }
      item.cleanups.push(this.runtime.addExtension(extension))
      item.pages = pages
      item.actions = actions
      item.enabled = true
      this.store.set(`plugin-enabled:${id}`, true)
    } catch {
      const cancelled = !current()
      if (!cancelled) {
        item.error = 'plugin_setup_failed'
        item.desiredEnabled = false
        this.store.set(`plugin-enabled:${id}`, false)
      }
      controller.abort('plugin_setup_stopped')
      await this.cleanup(item, id)
      throw new Error(cancelled ? 'plugin_disabled' : 'plugin_setup_failed')
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
      } catch {
        this.store.log('error', `plugin:${id}`, 'dispose_failed')
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
  private require(id: string) {
    const item = this.loaded.get(id)
    if (!item) throw new Error('plugin_not_found')
    return item
  }
}

// A deliberately small JSON Schema form vocabulary, rejected rather than silently ignored when unsupported.
export function validateConfig(value: unknown, schema: Record<string, unknown>): void {
  const allowed = new Set([
    'type',
    'properties',
    'required',
    'additionalProperties',
    'description',
    'title',
    'default',
    'enum',
    'minimum',
    'maximum',
    'minLength',
    'maxLength',
    'items',
    'secret',
    'group',
  ])
  for (const key of Object.keys(schema))
    if (!allowed.has(key) && key !== '$schema') throw new Error('unsupported_schema_keyword')
  if (schema.enum && !(schema.enum as unknown[]).includes(value)) throw new Error('invalid_enum')
  if (schema.type === 'object') {
    if (!value || typeof value !== 'object' || Array.isArray(value))
      throw new Error('expected_object')
    const object = value as Record<string, unknown>,
      properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>
    for (const key of (schema.required ?? []) as string[])
      if (!(key in object)) throw new Error('required_property')
    for (const [key, entry] of Object.entries(object)) {
      if (!properties[key]) {
        if (schema.additionalProperties !== true) throw new Error('unknown_property')
      } else validateConfig(entry, properties[key])
    }
  } else if (schema.type === 'string') {
    if (
      typeof value !== 'string' ||
      (typeof schema.minLength === 'number' && value.length < schema.minLength) ||
      (typeof schema.maxLength === 'number' && value.length > schema.maxLength)
    )
      throw new Error('expected_string')
  } else if (schema.type === 'boolean') {
    if (typeof value !== 'boolean') throw new Error('expected_boolean')
  } else if (schema.type === 'integer' || schema.type === 'number') {
    if (
      typeof value !== 'number' ||
      !Number.isFinite(value) ||
      (schema.type === 'integer' && !Number.isInteger(value)) ||
      (typeof schema.minimum === 'number' && value < schema.minimum) ||
      (typeof schema.maximum === 'number' && value > schema.maximum)
    )
      throw new Error('expected_number')
  } else if (schema.type === 'array') {
    if (!Array.isArray(value)) throw new Error('expected_array')
    for (const entry of value) validateConfig(entry, schema.items as Record<string, unknown>)
  } else throw new Error('unsupported_schema_type')
}
function redactConfig(value: unknown, schema: Record<string, unknown>): unknown {
  if (schema.secret === true) return undefined
  if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
    const properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>,
      result: Record<string, unknown> = {}
    for (const [key, entry] of Object.entries(value)) {
      const next = properties[key] ? redactConfig(entry, properties[key]) : entry
      if (next !== undefined) result[key] = next
    }
    return result
  }
  if (schema.type === 'array' && Array.isArray(value))
    return value.map(
      (entry) => redactConfig(entry, (schema.items ?? {}) as Record<string, unknown>) ?? null,
    )
  return value
}
function preserveSecrets(
  value: unknown,
  previous: unknown,
  schema: Record<string, unknown>,
): unknown {
  if (schema.secret === true) return value == null ? previous : value
  if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
    const result = { ...value } as Record<string, unknown>,
      old = previous && typeof previous === 'object' ? (previous as Record<string, unknown>) : {},
      properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>
    for (const [key, field] of Object.entries(properties)) {
      if (!(key in result) && !(key in old)) continue
      const next = preserveSecrets(result[key], old[key], field)
      if (next !== undefined) result[key] = next
    }
    return result
  }
  if (schema.type === 'array' && Array.isArray(value))
    return value.map((entry, index) =>
      preserveSecrets(
        entry,
        Array.isArray(previous) ? previous[index] : undefined,
        (schema.items ?? {}) as Record<string, unknown>,
      ),
    )
  return value
}
function publicSchema(schema: Record<string, unknown>): Record<string, unknown> {
  const result = structuredClone(schema)
  if ('default' in result) {
    const safe = redactConfig(result.default, result)
    if (safe === undefined) delete result.default
    else result.default = safe
  }
  if (result.properties && typeof result.properties === 'object')
    result.properties = Object.fromEntries(
      Object.entries(result.properties as Record<string, Record<string, unknown>>).map(
        ([key, field]) => [key, publicSchema(field)],
      ),
    )
  if (result.items && typeof result.items === 'object')
    result.items = publicSchema(result.items as Record<string, unknown>)
  return result
}
