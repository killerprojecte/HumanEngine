import {
  snowLumaFields,
  snowLumaReasons,
  isSnowLumaIssue,
} from './plugins/builtin/snowluma/validation.ts'
import { requestIdFor } from './diagnostics/errors.ts'
import { pluginStartupErrorCodes } from './plugin-errors.ts'
import { SnowLumaConfigSchema } from './builtin-plugins.ts'
import { TypingDelayConfigSchema } from './typing-delay.ts'
import { PluginConfigurationError, PluginStartupError } from './plugins.ts'
import { probe } from './probes.ts'
import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { Engine } from './engine.ts'
import { ErrorSchema } from './schema.ts'

const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const body = <T extends z.ZodType>(schema: T) => ({
  required: true,
  content: { 'application/json': { schema } },
})
const params = z.object({ id: z.string() })
const PluginStartupFailure = z
  .object({
    error: z.enum(pluginStartupErrorCodes),
    requestId: z.string().optional(),
    systemCode: z.string().optional(),
    httpStatus: z.number().int().optional(),
  })
  .openapi('PluginStartupFailure')
const PluginConfigurationFailure = z
  .object({
    error: z.literal('invalid_plugin_configuration'),
    requestId: z.string(),
    issues: z
      .array(z.object({ field: z.enum(snowLumaFields), reason: z.enum(snowLumaReasons) }).strict())
      .max(32),
  })
  .openapi('PluginConfigurationFailure')
const errors = {
  400: json(ErrorSchema, 'Invalid request'),
  401: json(ErrorSchema, 'Authentication required'),
  503: json(ErrorSchema, 'Service unavailable'),
}
const Part = z.object({
  type: z.enum(['text', 'mention', 'media', 'unsupported']),
  text: z.string().optional(),
  userId: z.string().optional(),
  assetId: z.string().optional(),
  mediaType: z.string().optional(),
  description: z.string().optional(),
})
const Message = z
  .object({
    id: z.string(),
    sceneId: z.string(),
    senderId: z.string(),
    sentAt: z.string(),
    receivedAt: z.string(),
    origin: z.enum(['live', 'history', 'simulation']),
    parts: z.array(Part),
    replyToId: z.string().optional(),
    status: z.enum(['available', 'recalled', 'unavailable']),
  })
  .openapi('Message')
const Scope = z.discriminatedUnion('kind', [
  z
    .object({ kind: z.literal('scene'), sceneId: z.string(), personId: z.string().optional() })
    .strict(),
  z.object({ kind: z.literal('sharedPublicKnowledge') }).strict(),
])
const MemoryInput = z
  .object({
    scope: Scope,
    content: z.string().min(1).max(10000),
    kind: z.enum(['fact', 'claim', 'term', 'experience', 'summary']),
    importance: z.enum(['low', 'medium', 'high']).default('medium'),
    confidence: z.number().min(0).max(1).default(1),
    locked: z.boolean().default(false),
  })
  .strict()
const Memory = MemoryInput.extend({
  id: z.string(),
  sources: z.array(z.object({ sceneId: z.string(), messageId: z.string(), senderId: z.string() })),
  learnedAt: z.string(),
  status: z.enum(['active', 'disputed', 'superseded', 'invalidated']),
  origin: z.enum(['manual', 'learned']),
  lineageStatus: z.enum(['verified', 'unverified', 'quarantined']).optional(),
}).openapi('Memory')
const Plugin = z
  .object({
    id: z.string(),
    name: z.string(),
    description: z.string(),
    version: z.string(),
    capabilities: z.array(z.string()),
    configSchema: z.record(z.string(), z.unknown()),
    builtin: z.boolean(),
    configuredSecrets: z.record(z.string(), z.boolean()),
    enabled: z.boolean(),
    error: z.string().optional(),
    errorRequestId: z.string().optional(),
    pages: z.array(z.object({ id: z.string(), title: z.string(), entry: z.string() })),
  })
  .openapi('Plugin')
const Rule = z
  .object({
    id: z.string(),
    scope: z.enum(['global', 'scene']),
    sceneId: z.string().optional(),
    content: z.string().min(1).max(5000),
    nonOverridable: z.boolean(),
    enabled: z.boolean(),
  })
  .strict()
  .refine((r) => r.scope === 'global' || !!r.sceneId, 'Scene rule requires sceneId')
  .openapi('Rule')
const MemeTags = z.object({
  appearance: z.array(z.string()),
  style: z.array(z.string()),
  subject: z.array(z.string()),
  emotion: z.array(z.string()),
  intent: z.array(z.string()),
  tone: z.array(z.string()),
  intensity: z.array(z.string()),
  memeMeaning: z.array(z.string()),
  ocr: z.string(),
})
const Meme = z
  .object({
    id: z.string(),
    format: z.string(),
    bytes: z.number(),
    width: z.number(),
    height: z.number(),
    frames: z.number(),
    sources: z.array(
      z.object({ sceneId: z.string(), messageId: z.string(), senderId: z.string() }),
    ),
    tags: MemeTags,
    tagRevision: z.number().int().optional(),
    analyzed: z.boolean(),
    available: z.boolean(),
    shared: z.boolean(),
    privacyReviewed: z.boolean(),
    selectionOrigin: z.enum(['automatic', 'manual']).optional(),
    sharingOrigin: z.enum(['automatic', 'manual']).optional(),
    favorite: z.boolean(),
    disabled: z.boolean(),
    preference: z.number(),
    usageCount: z.number(),
    lastUsedAt: z.string().optional(),
    analysisState: z
      .enum(['running', 'retrying', 'succeeded', 'failed', 'cancelled', 'interrupted'])
      .optional(),
    analysisAttempts: z.number().int().optional(),
    analysisNextRetryAt: z.string().optional(),
    analysisFailureCode: z.string().optional(),
    createdAt: z.string(),
    analysisError: z.string().optional(),
  })
  .openapi('Meme')
const Success = z.object({ ok: z.boolean() })
export function installEngineRoutes(app: OpenAPIHono, engine: Engine) {
  app.openAPIRegistry.register('SnowLumaPluginConfig', SnowLumaConfigSchema)
  app.openAPIRegistry.register('TypingDelayPluginConfig', TypingDelayConfigSchema)
  const { runtime, store, memory, plugins } = engine

  const EmbeddingIndexStatus = z
    .object({
      state: z.enum(['unconfigured', 'needs_rebuild', 'ready']),
      selected: z
        .object({
          modelId: z.string(),
          encoderRevision: z.string(),
          requestedDimensions: z.number().int().optional(),
        })
        .optional(),
      active: z
        .object({
          generationId: z.string(),
          encoderRevision: z.string(),
          modelId: z.string(),
          dimensions: z.number().int(),
          spaceId: z.string(),
        })
        .optional(),
      coverage: z.object({
        eligible: z.number().int(),
        indexed: z.number().int(),
        pending: z.number().int(),
      }),
      retained: z.object({ generations: z.number().int(), vectors: z.number().int() }),
      job: z
        .object({
          id: z.string(),
          encoderRevision: z.string(),
          modelId: z.string(),
          state: z.enum(['running', 'paused', 'failed', 'cancelled', 'completed']),
          processed: z.number().int(),
          total: z.number().int(),
          dimensions: z.number().int().optional(),
          spaceId: z.string().optional(),
          error: z.string().optional(),
          errorRequestId: z.string().optional(),
          requiresRetryConfirmation: z.boolean(),
          expectedActive: z.string().optional(),
          attemptId: z.string().optional(),
          ownerId: z.string().optional(),
          leaseUntil: z.number().optional(),
        })
        .optional(),
    })
    .openapi('EmbeddingIndexStatus')
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/embedding-index',
      responses: { 200: json(EmbeddingIndexStatus), ...errors },
    }),
    (c) => c.json(engine.embeddingIndex.status(), 200),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/embedding-index/{action}',
      request: {
        params: z.object({ action: z.enum(['start', 'pause', 'cancel', 'resume']) }),
        body: body(
          z
            .object({
              confirmUnknownRequest: z.boolean().default(false),
              expectedJobId: z.string().nullable(),
              expectedEncoderRevision: z.string().optional(),
            })
            .strict(),
        ),
      },
      responses: { 200: json(EmbeddingIndexStatus), ...errors },
    }),
    (c) => {
      const { action } = c.req.valid('param')
      const input = c.req.valid('json')
      try {
        if (action === 'start') {
          if (!input.expectedEncoderRevision)
            return c.json({ error: 'embedding_index_refresh_required' }, 400)
          engine.embeddingIndex.start(input.expectedJobId, input.expectedEncoderRevision)
        } else if (action === 'resume')
          engine.embeddingIndex.resume(input.confirmUnknownRequest, input.expectedJobId)
        else engine.embeddingIndex[action](input.expectedJobId)
        return c.json(engine.embeddingIndex.status(), 200)
      } catch {
        // Never expose provider messages, endpoints or source contents via action failures.
        return c.json({ error: 'embedding_index_action_unavailable' }, 400)
      }
    },
  )

  const LearningStatus = z
    .object({
      accepted: z.number(),
      completed: z.number(),
      failed: z.number(),
      overflow: z.number(),
      retried: z.number(),
      queued: z.number(),
      retrying: z.number(),
      active: z.number(),
      paused: z.number(),
      failures: z.array(
        z.object({
          id: z.string(),
          sceneId: z.string(),
          attempt: z.number(),
          messageCount: z.number(),
          partial: z.boolean(),
          canRetry: z.boolean(),
          createdAt: z.string(),
        }),
      ),
    })
    .openapi('LearningStatus')
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/learning/status',
      responses: { 200: json(LearningStatus), ...errors },
    }),
    (c) => c.json(engine.learningStatus(), 200),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/learning/failures/{id}/retry',
      request: { params },
      responses: { 200: json(Success), ...errors },
    }),
    (c) =>
      engine.retryLearning(c.req.valid('param').id).ok
        ? c.json({ ok: true }, 200)
        : c.json({ error: 'Batch is unavailable or cannot safely retry' }, 400),
  )
  app.openapi(
    createRoute({
      method: 'delete',
      path: '/api/learning/failures/{id}',
      request: { params },
      responses: { 200: json(Success), ...errors },
    }),
    (c) =>
      engine.discardLearning(c.req.valid('param').id).ok
        ? c.json({ ok: true }, 200)
        : c.json({ error: 'Batch is unavailable' }, 400),
  )

  const Habit = z
    .object({
      id: z.string(),
      kind: z.enum(['sceneRule', 'botExpression']),
      sceneId: z.string().optional(),
      content: z.string().optional(),
      preference: z.string().optional(),
      context: z.literal('technical').optional(),
      evidenceVersion: z.literal(2).optional(),
      sourceCutoff: z.number().optional(),
      authorWatermarks: z.record(z.string(), z.number()).optional(),
      weight: z.number(),
      confidence: z.number(),
      status: z.enum(['active', 'disputed', 'invalidated']),
      locked: z.boolean(),
      learnedAt: z.string(),
      sources: z.array(
        z.object({
          sceneId: z.string(),
          messageId: z.string(),
          senderId: z.string(),
          direction: z.number(),
          timestamp: z.number().optional(),
          authorKey: z.string().optional(),
        }),
      ),
    })
    .openapi('Habit')
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/habits',
      responses: { 200: json(z.array(Habit)), ...errors },
    }),
    (c) => c.json(engine.habits.adminList(), 200),
  )
  app.openapi(
    createRoute({
      method: 'patch',
      path: '/api/habits/{id}',
      request: { params, body: body(z.object({ locked: z.boolean() }).strict()) },
      responses: { 200: json(Habit), ...errors },
    }),
    (c) => c.json(engine.habits.update(c.req.valid('param').id, c.req.valid('json')), 200),
  )
  app.openapi(
    createRoute({
      method: 'delete',
      path: '/api/habits/{id}',
      request: { params },
      responses: { 200: json(Success), ...errors },
    }),
    (c) => {
      engine.habits.delete(c.req.valid('param').id)
      return c.json({ ok: true }, 200)
    },
  )
  const Capability = z.enum(['text', 'tools', 'vision', 'embedding'])
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/models/{id}/probe',
      request: { params, body: body(z.object({ capability: Capability }).strict()) },
      responses: {
        200: json(
          z.object({
            ok: z.boolean(),
            capability: Capability,
            detail: z.string(),
            durationMs: z.number(),
          }),
        ),
        ...errors,
      },
    }),
    async (c) =>
      c.json(
        await probe(
          store,
          c.req.valid('param').id,
          c.req.valid('json').capability,
          engine.protocols,
        ),
        200,
      ),
  )

  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memes',
      request: {
        query: z.object({
          page: z.coerce.number().int().min(1).max(1000000).default(1),
          pageSize: z.coerce.number().int().min(1).max(96).default(24),
          query: z.string().max(200).default(''),
          scope: z
            .string()
            .max(300)
            .refine(
              (value) => ['*', 'shared', 'local'].includes(value) || value.startsWith('scene:'),
              'Invalid scope',
            )
            .default('*'),
          state: z.enum(['*', 'favorite', 'disabled', 'available', 'unanalyzed']).default('*'),
        }),
      },
      responses: {
        200: json(
          z.object({
            items: z.array(Meme),
            total: z.number().int(),
            page: z.number().int(),
            pageSize: z.number().int(),
          }),
        ),
        ...errors,
      },
    }),
    (c) => c.json(engine.memes.adminPage(c.req.valid('query')), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memes/{id}',
      request: { params },
      responses: { 200: json(Meme), ...errors },
    }),
    (c) => {
      const asset = engine.memes.adminGet(c.req.valid('param').id)
      return asset ? c.json(asset, 200) : c.json({ error: 'Asset unavailable' }, 400)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memes/{id}/thumbnail',
      request: { params },
      responses: {
        200: {
          description: 'Static first-frame thumbnail, at most 320 pixels per side',
          content: { 'image/webp': { schema: z.string().openapi({ format: 'binary' }) } },
        },
        ...errors,
      },
    }),
    async (c) =>
      new Response(new Uint8Array(await engine.memes.thumbnail(c.req.valid('param').id)), {
        headers: { 'Content-Type': 'image/webp', 'Cache-Control': 'no-store' },
      }),
  )
  app.openapi(
    createRoute({
      method: 'patch',
      path: '/api/memes/{id}',
      request: {
        params,
        body: body(
          z
            .object({
              available: z.boolean().optional(),
              shared: z.boolean().optional(),
              privacyReviewed: z.boolean().optional(),
              favorite: z.boolean().optional(),
              disabled: z.boolean().optional(),
              tags: MemeTags.optional(),
            })
            .strict(),
        ),
      },
      responses: { 200: json(Meme), ...errors },
    }),
    (c) => c.json(engine.memes.update(c.req.valid('param').id, c.req.valid('json')), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memes/{id}/file',
      request: { params },
      responses: {
        200: {
          description: 'Original image bytes, preserving animation',
          content: {
            'application/octet-stream': { schema: z.string().openapi({ format: 'binary' }) },
          },
        },
        ...errors,
      },
    }),
    async (c) => {
      const asset = engine.memes.adminGet(c.req.valid('param').id)
      if (!asset) return c.json({ error: 'Asset unavailable' }, 400)
      const bytes = await readFile(join(engine.memes.mediaRoot, `${asset.id}.${asset.format}`))
      return new Response(bytes, {
        headers: { 'Content-Type': `image/${asset.format}`, 'Cache-Control': 'no-store' },
      })
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memes/{id}/analyze',
      request: { params, body: body(z.object({ sceneId: z.string() }).strict()) },
      responses: { 200: json(Meme), ...errors },
    }),
    async (c) =>
      c.json(
        await engine.analyzeMeme(c.req.valid('json').sceneId, c.req.valid('param').id, {
          force: true,
        }),
        200,
      ),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memes/import',
      request: {
        body: body(
          z
            .object({
              sceneId: z.string(),
              messageId: z.string(),
              base64: z.string().max(14000000),
            })
            .strict(),
        ),
      },
      responses: { 200: json(Meme), ...errors },
    }),
    async (c) => {
      const input = c.req.valid('json'),
        message = runtime.preview(input.sceneId).find((m) => m.id === input.messageId)
      if (!message || message.origin === 'simulation' || message.senderId === 'bot')
        return c.json({ error: 'An available real source message is required' }, 400)
      const source = { sceneId: input.sceneId, messageId: message.id, senderId: message.senderId }
      return c.json(
        await engine.memes.import(
          {
            sceneId: input.sceneId,
            simulation: false,
            sourceEvidence: [
              { ...source, available: message.status === 'available', isBot: false },
            ],
          },
          Buffer.from(input.base64, 'base64'),
          source,
        ),
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/simulations',
      responses: { 200: json(z.object({ id: z.string() })), ...errors },
    }),
    (c) => c.json({ id: runtime.createSimulation() }, 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/simulations/{id}',
      request: { params },
      responses: { 200: json(z.array(Message)), ...errors },
    }),
    (c) => {
      const id = c.req.valid('param').id
      if (!runtime.isSimulation(id)) return c.json({ error: 'Simulation unavailable' }, 400)
      return c.json(runtime.preview(id), 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'delete',
      path: '/api/simulations/{id}',
      request: { params },
      responses: { 200: json(Success), ...errors },
    }),
    (c) => {
      runtime.endSimulation(c.req.valid('param').id)
      return c.json({ ok: true }, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/simulations/{id}/messages',
      request: { params, body: body(z.object({ text: z.string().min(1).max(12000) }).strict()) },
      responses: { 200: json(z.array(Message)), ...errors },
    }),
    async (c) =>
      c.json(await runtime.simulate(c.req.valid('param').id, c.req.valid('json').text), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/scenes/{id}/messages',
      request: { params },
      responses: { 200: json(z.array(Message)), ...errors },
    }),
    (c) => c.json(runtime.preview(c.req.valid('param').id), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/scenes/{id}/history',
      request: { params },
      responses: {
        200: json(
          z.object({
            items: z.array(Message),
            coverage: z.enum(['complete', 'partial', 'unavailable']),
            nextCursor: z.string().optional(),
            reason: z.string().optional(),
          }),
        ),
        ...errors,
      },
    }),
    async (c) => c.json(await runtime.previewHistory(c.req.valid('param').id), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memories',
      responses: { 200: json(z.array(Memory)), ...errors },
    }),
    (c) => c.json(memory.adminList(), 200),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memories',
      request: { body: body(MemoryInput) },
      responses: { 200: json(Memory), ...errors },
    }),
    (c) => {
      const record = memory.create(c.req.valid('json'))
      void engine.indexMemory(record.id, record.content)
      return c.json(record, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'patch',
      path: '/api/memories/{id}',
      request: {
        params,
        body: body(
          z
            .object({
              content: z.string().min(1).max(10000).optional(),
              locked: z.boolean().optional(),
              status: z.enum(['active', 'disputed', 'superseded', 'invalidated']).optional(),
            })
            .strict(),
        ),
      },
      responses: { 200: json(Memory), ...errors },
    }),
    (c) => {
      const record = memory.update(c.req.valid('param').id, c.req.valid('json'))
      void engine.indexMemory(record.id, record.content)
      return c.json(record, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'delete',
      path: '/api/memories/{id}',
      request: { params },
      responses: { 200: json(Success), ...errors },
    }),
    (c) => c.json({ ok: memory.delete(c.req.valid('param').id) }, 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/rules',
      responses: { 200: json(z.array(Rule)), ...errors },
    }),
    (c) => c.json(store.get('rules', []), 200),
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/rules',
      request: { body: body(z.array(Rule)) },
      responses: { 200: json(z.array(Rule)), ...errors },
    }),
    (c) => {
      const rules = c.req.valid('json')
      store.set('rules', rules)
      return c.json(rules, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/persona/versions',
      responses: {
        200: json(
          z.array(z.object({ id: z.string(), createdAt: z.string(), content: z.string() })),
        ),
        ...errors,
      },
    }),
    (c) => c.json(store.get('personaVersions', []), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/plugins',
      responses: { 200: json(z.array(Plugin)), ...errors },
    }),
    (c) => c.json(plugins.list(), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/plugins/{id}/config',
      request: { params },
      responses: { 200: json(z.record(z.string(), z.unknown())), ...errors },
    }),
    (c) => c.json(plugins.config(c.req.valid('param').id), 200),
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/plugins/{id}/config',
      request: { params, body: body(z.record(z.string(), z.unknown())) },
      responses: {
        200: json(z.record(z.string(), z.unknown())),
        ...errors,
        400: json(
          PluginConfigurationFailure,
          'Invalid plugin configuration with safe field diagnostics',
        ),
        503: json(PluginStartupFailure, 'Plugin startup failed with a sanitized diagnostic code'),
      },
    }),
    async (c) => {
      try {
        return c.json(
          await plugins.configure(
            c.req.valid('param').id,
            c.req.valid('json'),
            requestIdFor(c.req.raw),
          ),
          200,
        )
      } catch (error) {
        if (error instanceof PluginConfigurationError)
          return c.json(
            {
              error: 'invalid_plugin_configuration' as const,
              requestId: error.requestId ?? requestIdFor(c.req.raw),
              issues: error.issues
                .filter(isSnowLumaIssue)
                .slice(0, 32)
                .map(({ field, reason }) => ({ field, reason })),
            },
            400,
          )
        if (error instanceof PluginStartupError)
          return c.json({ error: error.code, requestId: error.requestId, ...error.details }, 503)
        throw error
      }
    },
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/plugins/{id}/enabled',
      request: { params, body: body(z.object({ enabled: z.boolean() }).strict()) },
      responses: {
        200: json(Success),
        ...errors,
        400: json(
          PluginConfigurationFailure,
          'Invalid plugin configuration with safe field diagnostics',
        ),
        503: json(PluginStartupFailure, 'Plugin startup failed with a sanitized diagnostic code'),
      },
    }),
    async (c) => {
      const id = c.req.valid('param').id,
        requestId = requestIdFor(c.req.raw)
      c.header('X-Request-ID', requestId)
      try {
        if (c.req.valid('json').enabled) await plugins.enable(id, requestId)
        else await plugins.disable(id, true, requestId)
        return c.json({ ok: true }, 200)
      } catch (error) {
        if (error instanceof PluginConfigurationError)
          return c.json(
            {
              error: 'invalid_plugin_configuration' as const,
              requestId: error.requestId ?? requestIdFor(c.req.raw),
              issues: error.issues
                .filter(isSnowLumaIssue)
                .slice(0, 32)
                .map(({ field, reason }) => ({ field, reason })),
            },
            400,
          )
        if (error instanceof PluginStartupError)
          return c.json({ error: error.code, requestId: error.requestId, ...error.details }, 503)
        throw error
      }
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/plugins/{id}/actions/{action}',
      request: {
        params: z.object({ id: z.string(), action: z.string() }),
        body: body(z.record(z.string(), z.unknown())),
      },
      responses: { 200: json(z.unknown()), ...errors },
    }),
    async (c) =>
      c.json(
        await plugins.action(
          c.req.valid('param').id,
          c.req.valid('param').action,
          c.req.valid('json'),
        ),
        200,
      ),
  )
}
