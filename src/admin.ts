import { probe } from './probes.ts'
import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { Router } from './models.ts'
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
}).openapi('Memory')
const Plugin = z
  .object({
    id: z.string(),
    name: z.string(),
    description: z.string(),
    version: z.string(),
    capabilities: z.array(z.string()),
    configSchema: z.record(z.string(), z.unknown()),
    enabled: z.boolean(),
    error: z.string().optional(),
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
    analyzed: z.boolean(),
    available: z.boolean(),
    shared: z.boolean(),
    privacyReviewed: z.boolean(),
    favorite: z.boolean(),
    disabled: z.boolean(),
    preference: z.number(),
    usageCount: z.number(),
    createdAt: z.string(),
    analysisError: z.string().optional(),
  })
  .openapi('Meme')
const Success = z.object({ ok: z.boolean() })
export function installEngineRoutes(app: OpenAPIHono, engine: Engine) {
  const { runtime, store, memory, plugins } = engine

  const Habit = z
    .object({
      id: z.string(),
      kind: z.enum(['sceneRule', 'botExpression']),
      sceneId: z.string().optional(),
      content: z.string().optional(),
      preference: z.string().optional(),
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
      c.json(await probe(store, c.req.valid('param').id, c.req.valid('json').capability), 200),
  )

  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memes',
      responses: { 200: json(z.array(Meme)), ...errors },
    }),
    (c) => c.json(engine.memes.adminList(), 200),
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
      const asset = engine.memes.adminList().find((a) => a.id === c.req.valid('param').id)
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
        await engine.memes.analyze(
          { sceneId: c.req.valid('json').sceneId, simulation: false },
          c.req.valid('param').id,
          new Router(store.models),
          { force: true },
        ),
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
      responses: { 200: json(z.record(z.string(), z.unknown())), ...errors },
    }),
    async (c) => c.json(await plugins.configure(c.req.valid('param').id, c.req.valid('json')), 200),
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/plugins/{id}/enabled',
      request: { params, body: body(z.object({ enabled: z.boolean() }).strict()) },
      responses: { 200: json(Success), ...errors },
    }),
    async (c) => {
      const id = c.req.valid('param').id
      if (c.req.valid('json').enabled) await plugins.enable(id)
      else await plugins.disable(id)
      return c.json({ ok: true }, 200)
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
