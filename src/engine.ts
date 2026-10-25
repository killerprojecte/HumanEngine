import { MemeService } from './memes.ts'
import { fetchQqMedia } from './media-fetch.ts'
import { readFileSync } from 'node:fs'
import { z } from '@hono/zod-openapi'
import { Store } from './store.ts'
import { Runtime, type RuntimeMessage, type TurnContext } from './runtime.ts'
import { PluginManager } from './plugins.ts'
import { OneBotAdapter } from './onebot.ts'
import { MemoryService, type MemoryCandidate } from './memory.ts'
import { Router, type ToolDefinition } from './models.ts'
import type { Scene } from './schema.ts'

const CandidateSchema = z
  .object({
    content: z.string().min(1).max(10000),
    kind: z.enum(['fact', 'claim', 'term', 'experience', 'summary']),
    personId: z.string().optional(),
    sourceMessageIds: z.array(z.string()).min(1).max(20),
    importance: z.enum(['low', 'medium', 'high']).default('medium'),
    confidence: z.number().min(0).max(1).default(0.5),
    supersedesId: z.string().optional(),
    occurredAt: z.string().optional(),
    validUntil: z.string().optional(),
  })
  .strict()
const definition = (name: string, description: string, schema: z.ZodType): ToolDefinition => ({
  name,
  description,
  inputSchema: z.toJSONSchema(schema),
})
export interface Rule {
  id: string
  scope: 'global' | 'scene'
  sceneId?: string
  content: string
  nonOverridable: boolean
  enabled: boolean
}
export class Engine {
  readonly runtime: Runtime
  readonly plugins: PluginManager
  readonly memory: MemoryService
  readonly memes: MemeService
  private readonly mediaTasks = new Map<string, AbortController>()
  private readonly learningBatches = new Map<string, RuntimeMessage[]>()
  private readonly learningTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private readonly learningTasks = new Map<string, AbortController>()
  private disposed = false
  private proactiveTimer?: ReturnType<typeof setInterval>
  constructor(readonly store: Store) {
    this.runtime = new Runtime(store)
    this.plugins = new PluginManager(store, this.runtime)
    this.memory = new MemoryService(store)
    this.memes = new MemeService(store)
    this.runtime.beforeSend({
      id: 'media-scope',
      priority: -100,
      handler: async (segments, context) => {
        for (const segment of segments)
          for (const part of segment.parts)
            if (part.type === 'media' && /^[a-f0-9]{64}$/.test(part.assetId)) {
              this.memes.original(
                { sceneId: context.scene.id, simulation: context.mode === 'simulation' },
                part.assetId,
              )
            }
        return { action: 'continue', segments }
      },
    })
    this.runtime.addExtension({
      tools: [
        definition(
          'memes.search',
          'Search analyzed available memes by intent without re-running vision.',
          z
            .object({ query: z.string().max(500), tags: z.array(z.string()).max(10).optional() })
            .strict(),
        ),
        definition(
          'memory.search',
          'Search current-scene memories and safe shared public knowledge.',
          z.object({ query: z.string().max(500) }).strict(),
        ),
        definition(
          'memory.person',
          'Read person memory from this scene only.',
          z.object({ personId: z.string() }).strict(),
        ),
        definition(
          'memory.source',
          'Read source identifiers for a memory accessible in this scene. Missing originals are not reconstructed.',
          z.object({ memoryId: z.string() }).strict(),
        ),
        definition(
          'rules.list',
          'Read applicable manual rules. Fixed template and non-overridable manual rules take precedence.',
          z.object({}).strict(),
        ),
        definition(
          'learning.propose',
          'Propose scene-scoped learning supported by currently available nonbot sources. Simulation never saves learning.',
          CandidateSchema,
        ),
      ],
      contextPrompt: (context) =>
        this.rules(context)
          .map((r) => `Manual rule${r.nonOverridable ? ' (non-overridable)' : ''}: ${r.content}`)
          .join('\n'),
      execute: async (name, input, context) => {
        const ctx = { sceneId: context.scene.id, simulation: context.mode === 'simulation' }
        if (name === 'memes.search') {
          const assets = this.memes.search(
            ctx,
            z.string().parse(input.query),
            input.tags as string[] | undefined,
          )
          for (const asset of assets)
            context.validations.push(() => {
              try {
                this.memes.original(ctx, asset.id)
                return true
              } catch {
                return false
              }
            })
          return assets
        }
        if (name === 'memory.search') {
          const results = await this.searchMemory(ctx, z.string().max(500).parse(input.query))
          this.trackMemory(
            context,
            results.map((m) => m.id),
          )
          return results
        }
        if (name === 'memory.person') {
          const results = this.memory.list(ctx, z.string().parse(input.personId))
          this.trackMemory(
            context,
            results.map((m) => m.id),
          )
          return results
        }
        if (name === 'memory.source') {
          const memory = this.memory.list(ctx).find((m) => m.id === input.memoryId)
          return memory
            ? {
                sources: memory.sources,
                note: 'Identifiers only; use messages.get for available originals',
              }
            : { error: 'Memory unavailable' }
        }
        if (name === 'rules.list') return this.rules(context)
        if (name === 'learning.propose')
          return this.propose(
            context.scene.id,
            context.mode === 'simulation',
            input,
            this.runtime.preview(context.scene.id),
          )
        return { error: 'Unknown tool' }
      },
      received: (message, scene) => {
        if (
          scene.kind !== 'simulation' &&
          this.store.scene(scene.id)?.learningEnabled &&
          message.senderId !== 'bot'
        ) {
          this.enqueueLearning(message)
          void this.ingestMedia(message)
        }
      },
      recalled: (sceneId, messageId) => {
        this.mediaTasks.get(`${sceneId}|${messageId}`)?.abort()
        this.memory.recall(sceneId, messageId)
        this.memes.recall(sceneId, messageId)
        this.learningBatches.set(
          sceneId,
          (this.learningBatches.get(sceneId) ?? []).filter((m) => m.id !== messageId),
        )
        this.learningTasks.get(sceneId)?.abort('source_recalled')
      },
    })
  }
  async start() {
    await this.plugins.register({
      manifest: {
        id: 'snowluma',
        name: 'SnowLuma',
        version: '0.1.0',
        description:
          'Independent OneBot v11 transport with verified SnowLuma history extensions. QQ connection and animation playback require your own deployment test.',
        capabilities: ['adapter', 'history', 'recall', 'schema-form'],
        configSchema: {
          type: 'object',
          properties: {
            url: {
              type: 'string',
              title: 'WebSocket URL',
              default: 'ws://127.0.0.1:3001',
              minLength: 1,
            },
            accountId: { type: 'string', title: 'Bot account ID', minLength: 1 },
            credentialEnv: {
              type: 'string',
              title: 'Token environment variable',
              default: 'SNOWLUMA_TOKEN',
              minLength: 1,
            },
          },
          required: ['url', 'accountId', 'credentialEnv'],
          additionalProperties: false,
        },
      },
      setup: async (context) => {
        const config = context.config as { url: string; accountId: string; credentialEnv: string }
        if (
          !/^wss?:\/\//.test(config.url) ||
          !config.accountId ||
          !/^[A-Z][A-Z0-9_]*$/.test(config.credentialEnv)
        )
          throw new Error('invalid_snowluma_configuration')
        const adapter = new OneBotAdapter(
          { id: 'snowluma', ...config },
          {
            resolveMedia: (assetId, scene) => {
              if (!/^[a-f0-9]{64}$/.test(assetId)) return undefined
              const original = this.memes.original(
                { sceneId: scene.id, simulation: false },
                assetId,
              )
              return {
                file: `base64://${readFileSync(original.path).toString('base64')}`,
                type: 'image',
              }
            },
            onMessage: (message, scene) =>
              this.runtime.ingest(
                message.senderId === adapter.userId(config.accountId)
                  ? { ...message, senderId: 'bot' }
                  : message,
                scene,
              ),
            onRecall: (event) => this.runtime.recall(event.sceneId, event.messageId),
            isRecalled: (sceneId, messageId) => this.store.recalled(sceneId, messageId),
            onStatus: (status) => this.store.log('info', 'snowluma', status),
            onError: (code) => this.store.log('error', 'snowluma', code),
          },
        )
        this.runtime.addAdapter('snowluma', adapter)
        context.onDispose(() => {
          this.runtime.removeAdapter('snowluma')
          for (const scene of this.store.scenes())
            if (scene.adapterId === 'snowluma') this.cancelLearning(scene.id)
        })
        await adapter.connect()
      },
    })
    const local =
      process.env.HUMANENGINE_PLUGINS?.split(',')
        .map((entry) => entry.trim())
        .filter(Boolean) ?? this.store.get<string[]>('localPlugins', [])
    for (const entry of local) {
      try {
        await this.plugins.loadLocal(entry)
      } catch {
        this.store.log('error', 'plugin', 'local_load_failed')
      }
    }
    for (const plugin of this.plugins.list())
      if (this.store.get(`plugin-enabled:${plugin.id}`, false)) {
        try {
          await this.plugins.enable(plugin.id)
        } catch {
          this.store.log('error', 'plugin', 'startup_failed')
        }
      }
    this.proactiveTimer = setInterval(() => {
      for (const scene of this.store.scenes())
        if (scene.kind === 'group' && scene.proactiveEnabled) void this.runtime.proactive(scene.id)
    }, 60000)
    this.proactiveTimer.unref()
  }
  rules(context: TurnContext) {
    return this.store
      .get<Rule[]>('rules', [])
      .filter(
        (r) =>
          r.enabled &&
          (r.scope === 'global' || (context.mode === 'real' && r.sceneId === context.scene.id)),
      )
  }
  sceneChanged(scene: Scene) {
    this.runtime.sceneChanged(scene)
    if (!scene.learningEnabled) this.cancelLearning(scene.id)
  }
  private cancelLearning(sceneId: string) {
    this.learningBatches.delete(sceneId)
    clearTimeout(this.learningTimers.get(sceneId))
    this.learningTimers.delete(sceneId)
    this.learningTasks.get(sceneId)?.abort('learning_disabled')
    for (const [key, controller] of this.mediaTasks)
      if (key.startsWith(sceneId + '|')) controller.abort()
  }
  async dispose() {
    this.disposed = true
    this.learningBatches.clear()
    for (const controller of this.mediaTasks.values()) controller.abort()
    clearInterval(this.proactiveTimer)
    for (const timer of this.learningTimers.values()) clearTimeout(timer)
    for (const controller of this.learningTasks.values()) controller.abort()
    await this.plugins.dispose()
    this.runtime.dispose()
  }
  private propose(
    sceneId: string,
    simulation: boolean,
    input: unknown,
    evidence: RuntimeMessage[],
  ) {
    if (simulation) return { status: 'rejected' as const, reason: 'simulation_does_not_learn' }
    const value = CandidateSchema.parse(input)
    const sources = value.sourceMessageIds.map((id) => {
      const message = evidence.find((m) => m.id === id)
      return { sceneId, messageId: id, senderId: message?.senderId ?? '' }
    })
    const candidate: MemoryCandidate = { ...value, sources }
    return this.memory.propose(
      {
        sceneId,
        simulation: false,
        sourceEvidence: evidence
          .filter((m) => m.origin !== 'history')
          .map((m) => ({
            sceneId: m.sceneId,
            messageId: m.id,
            senderId: m.senderId,
            available: m.status === 'available' && !this.store.recalled(m.sceneId, m.id),
            isBot: m.senderId === 'bot',
          })),
      },
      candidate,
    )
  }
  private enqueueLearning(message: RuntimeMessage) {
    const sceneId = message.sceneId
    const batch = this.learningBatches.get(sceneId) ?? []
    batch.push(structuredClone(message))
    this.learningBatches.set(sceneId, batch.slice(-60))
    this.scheduleLearning(sceneId)
  }
  private scheduleLearning(sceneId: string) {
    if (
      this.disposed ||
      !this.learningBatches.get(sceneId)?.length ||
      this.learningTimers.has(sceneId)
    )
      return
    const timer = setTimeout(() => {
      this.learningTimers.delete(sceneId)
      void this.learn(sceneId)
    }, 30000)
    timer.unref()
    this.learningTimers.set(sceneId, timer)
  }
  private async learn(sceneId: string) {
    if (!this.store.scene(sceneId)?.learningEnabled) {
      this.cancelLearning(sceneId)
      return
    }
    if (this.learningTasks.has(sceneId)) return
    const messages = this.learningBatches.get(sceneId) ?? []
    this.learningBatches.delete(sceneId)
    if (!messages.length) return
    const controller = new AbortController()
    this.learningTasks.set(sceneId, controller)
    try {
      const router = new Router(this.store.models)
      const response = await router.complete(
        'learning',
        [
          {
            role: 'system',
            content:
              'Extract only durable useful scene-scoped information with sourceMessageIds. Do not store raw chat transcripts, bot-generated claims, private data as public knowledge, or instructions to modify code/templates. Preserve uncertainty, claims, and message-relative event dates. Use learning.propose only when worthwhile; silence is valid.',
          },
          {
            role: 'user',
            content: JSON.stringify({
              now: new Date().toISOString(),
              timezone: this.store.settings.timezone,
              messages,
            }),
          },
        ],
        [definition('learning.propose', 'Propose a sourced memory candidate', CandidateSchema)],
        controller.signal,
      )
      if (controller.signal.aborted) return
      for (const call of response.toolCalls)
        if (call.name === 'learning.propose') {
          const result = this.propose(sceneId, false, call.arguments, messages)
          if (result.status === 'saved')
            void this.indexMemory(result.memory.id, result.memory.content)
        }
    } catch {
      this.store.log(
        controller.signal.aborted ? 'info' : 'error',
        'learning',
        controller.signal.aborted ? 'learning_cancelled' : 'learning_failed',
        { sceneId },
      )
    } finally {
      this.learningTasks.delete(sceneId)
      this.scheduleLearning(sceneId)
    }
  }
  private async ingestMedia(message: RuntimeMessage) {
    if (!this.store.models.routes.some((r) => r.role === 'vision' && r.orderedModelIds.length))
      return
    const key = `${message.sceneId}|${message.id}`,
      controller = new AbortController()
    this.mediaTasks.set(key, controller)
    try {
      for (const part of message.parts) {
        if (part.type !== 'media' || !part.assetId.startsWith('ob:media:')) continue
        try {
          const media = JSON.parse(
            Buffer.from(part.assetId.slice(9), 'base64url').toString('utf8'),
          ) as { sceneId: string; file: string; type: string }
          if (media.sceneId !== message.sceneId || media.type !== 'image') continue
          const bytes = await fetchQqMedia(media.file, controller.signal)
          const source = {
            sceneId: message.sceneId,
            messageId: message.id,
            senderId: message.senderId,
          }
          const context = {
            sceneId: message.sceneId,
            simulation: false,
            sourceEvidence: [
              {
                ...source,
                available: !this.store.recalled(message.sceneId, message.id),
                isBot: message.senderId === 'bot',
              },
            ],
          }
          const asset = await this.memes.import(context, bytes, source)
          await this.memes.analyze(context, asset.id, new Router(this.store.models), {
            signal: controller.signal,
          })
        } catch {
          this.store.log('warn', 'media', 'intake_unavailable', { sceneId: message.sceneId })
        }
      }
    } finally {
      this.mediaTasks.delete(key)
    }
  }
  private trackMemory(context: TurnContext, ids: string[]) {
    for (const id of ids) {
      const record = this.memory.adminList().find((m) => m.id === id)
      if (!record) continue
      for (const source of record.sources)
        if (source.sceneId === context.scene.id) context.dependencies.add(source.messageId)
      context.validations.push(() =>
        this.memory
          .list({ sceneId: context.scene.id, simulation: context.mode === 'simulation' })
          .some((m) => m.id === id && m.content === record.content),
      )
    }
  }
  async searchMemory(ctx: { sceneId: string; simulation: boolean }, query: string) {
    try {
      const config = this.store.models,
        route = config.routes.find((r) => r.role === 'embedding'),
        model = config.models.find((m) => m.id === route?.orderedModelIds[0]),
        provider = config.providers.find((p) => p.id === model?.providerId)
      if (!model || !provider) return this.memory.search(ctx, query)
      const embedded = await new Router(config).embeddings(model.id, [query])
      const vector = embedded.vectors[0]
      if (!vector) return this.memory.search(ctx, query)
      const spaceConfig = {
        providerId: provider.id,
        modelId: model.id,
        apiModelName: model.apiModelName,
        deployment: provider.baseUrl,
        dimensions: vector.length,
        preprocessing: 'text-v1',
        normalized: false,
      }
      // Simulation performs no index/config writes, even when a query model is configured.
      if (ctx.simulation) return this.memory.search(ctx, query)
      const space = this.memory.registerSpace(spaceConfig)
      return this.memory.search(ctx, query, undefined, { spaceId: space, vector })
    } catch {
      this.store.log('warn', 'memory', 'query_embedding_unavailable')
      return this.memory.search(ctx, query)
    }
  }
  async indexMemory(id: string, content: string) {
    try {
      const config = this.store.models,
        route = config.routes.find((r) => r.role === 'embedding'),
        model = config.models.find((m) => m.id === route?.orderedModelIds[0]),
        provider = config.providers.find((p) => p.id === model?.providerId)
      if (!model || !provider) return
      const embedded = await new Router(config).embeddings(model.id, [content])
      const vector = embedded.vectors[0]
      if (!vector) return
      const space = this.memory.registerSpace({
        providerId: provider.id,
        modelId: model.id,
        apiModelName: model.apiModelName,
        deployment: provider.baseUrl,
        dimensions: vector.length,
        preprocessing: 'text-v1',
        normalized: false,
      })
      if (
        this.memory
          .adminList()
          .some((m) => m.id === id && m.content === content && m.status === 'active')
      )
        this.memory.index(id, space, vector)
    } catch {
      this.store.log('warn', 'memory', 'embedding_unavailable')
    }
  }
}
