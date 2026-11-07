import { ModelCallsQuerySchema, ModelCallsPageSchema } from './model-calls.ts'
import { usageTimeZone } from './usage-time.ts'
import { installWorkspaceRoutes } from './memory/api/workspace-routes.ts'
import { installCommitmentRoutes } from './memory/interfaces/commitment-routes.ts'
import { requestIdFor, requestOperation, sanitizeError } from './diagnostics/errors.ts'
import { modelScheduler } from './scheduler.ts'
import { Engine } from './engine.ts'
import { installEngineRoutes } from './admin.ts'
import { randomUUID } from 'node:crypto'
import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import { timingSafeEqual } from 'node:crypto'
import { Store } from './store.ts'
import { captureResponseReadHead, metadata, sameHead, type Head } from './memory/recovery/ledger.ts'
import {
  ModelConfigSchema,
  ModelConfigWriteSchema,
  SettingsSchema,
  SceneSchema,
  LogSchema,
  LogDeliverySchema,
  StatusSchema,
  SceneStateSchema,
  ErrorSchema,
  UsageSchema,
  DiagnosticSchema,
} from './schema.ts'

const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const body = <T extends z.ZodType>(schema: T) => ({
  required: true,
  content: { 'application/json': { schema } },
})
const errorResponses = {
  400: json(ErrorSchema, 'Invalid request'),
  401: json(ErrorSchema, 'Authentication required'),
  503: json(ErrorSchema, 'Service unavailable'),
}
const responseReadCaptures = new WeakMap<Request, { response: Response; observedHead?: Head }>()

/** Internal transport evidence only. No head read occurs after an asynchronous body read. */
export async function fetchWithPublicationProof(
  app: ReturnType<typeof createApp>,
  request: Request,
): Promise<{ response: Response; observedHead?: Head }> {
  responseReadCaptures.delete(request)
  const response = await app.fetch(request)
  const capture = responseReadCaptures.get(request)
  responseReadCaptures.delete(request)
  const exact = capture?.response === response
  // Hono implements HEAD by wrapping the corresponding GET result in a null-body Response.
  const headClone =
    capture &&
    request.method === 'HEAD' &&
    response.body === null &&
    response.status === capture.response.status &&
    response.statusText === capture.response.statusText &&
    JSON.stringify([...response.headers]) === JSON.stringify([...capture.response.headers])
  return { response, observedHead: exact || headClone ? capture?.observedHead : undefined }
}

export function createApp(store: Store, options: { token?: string; engine?: Engine } = {}) {
  const engine = options.engine ?? new Engine(store)
  const app = new OpenAPIHono({
    defaultHook: (result, c) => {
      if (!result.success) return c.json({ error: 'Invalid request' }, 400)
    },
  })
  const startedAt = new Date().toISOString()
  app.use('/api/*', async (c, next) => {
    c.header('X-Request-ID', requestIdFor(c.req.raw))
    const token = options.token ?? process.env.HUMANENGINE_ADMIN_TOKEN
    if (!token)
      return c.json(
        { error: 'Set HUMANENGINE_ADMIN_TOKEN on the server before using the API' },
        503,
      )
    const provided = c.req.header('Authorization')?.replace(/^Bearer /, '') ?? ''
    const a = Buffer.from(provided),
      b = Buffer.from(token)
    if (a.length !== b.length || !timingSafeEqual(a, b))
      return c.json({ error: 'Authentication required' }, 401)
    const started = performance.now()
    store.assertRecoveryReady()
    const handlerStarted = performance.now()
    // Bind the head before synchronous body construction, not after await next():
    // a queued recall may run between a handler returning and this middleware resuming.
    // This is only an observation; the final middleware and Node publisher both
    // revalidate current authority. Unchanged verified history is reused; routes must
    // revalidate their own async reads.
    const materializations = new WeakMap<Response, { observedHead?: Head }>()
    const bind = <T extends (...args: any[]) => Response>(build: T): T =>
      ((...args: Parameters<T>) => {
        const observedHead = metadata(store.db)?.head
        const response = build(...args)
        materializations.set(response, { observedHead })
        return response
      }) as T
    c.json = bind(c.json)
    c.text = bind(c.text)
    c.body = bind(c.body)
    c.newResponse = bind(c.newResponse)
    await next()
    const handlerFinished = performance.now()
    // Pending external revocations can appear while an asynchronous route reads bytes.
    const observedHead = captureResponseReadHead(store.db)
    const materialized = materializations.get(c.res)
    if (
      !materialized ||
      (observedHead
        ? !materialized.observedHead || !sameHead(materialized.observedHead, observedHead)
        : materialized.observedHead !== undefined)
    )
      throw new Error('response_materialization_stale_or_unbound')
    const finished = performance.now()
    // Authenticated diagnostics only: durations, never payloads or configuration values.
    c.header(
      'Server-Timing',
      `recovery;dur=${(handlerStarted - started + finished - handlerFinished).toFixed(2)}, handler;dur=${(handlerFinished - handlerStarted).toFixed(2)}, total;dur=${(finished - started).toFixed(2)}`,
    )
    c.header('Cache-Control', 'no-store')
    c.header('X-Content-Type-Options', 'nosniff')
    responseReadCaptures.set(c.req.raw, { response: c.res, observedHead })
  })
  app.onError((error, c) => {
    const requestId = requestIdFor(c.req.raw)
    c.header('X-Request-ID', requestId)
    store.log('error', 'api', 'api_unhandled_error', {
      requestId,
      operation: requestOperation(c.req.method, c.req.routePath),
      phase: 'request',
      ...sanitizeError(error, {
        operation: requestOperation(c.req.method, c.req.routePath),
        stage: 'api.request',
      }),
    })
    return c.json({ error: 'Request failed; inspect server diagnostics', requestId }, 503)
  })
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/status',
      responses: { 200: json(StatusSchema), ...errorResponses },
    }),
    (c) => {
      const scenes = store.scenes(),
        models = store.models
      return c.json(
        {
          name: store.settings.name,
          startedAt,
          uptimeSeconds: Math.floor((Date.now() - Date.parse(startedAt)) / 1000),
          sceneCount: scenes.length,
          enabledScenes: scenes.filter((s) => s.participationEnabled).length,
          providerCount: models.providers.length,
          modelCount: models.models.length,
          activeTasks: engine.runtime.activeTasks,
          recentErrors: store.logs.filter((l) => l.level === 'error').length,
          vectorVersion: store.vectorVersion,
          version: '0.1.0',
          queuedModelRequests: modelScheduler.status.queued,
          runningModelRequests: modelScheduler.status.running,
          adapters: engine.runtime.adapterStatuses,
        },
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/models',
      responses: { 200: json(ModelConfigSchema), ...errorResponses },
    }),
    (c) => c.json(store.models, 200),
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/models',
      request: { body: body(ModelConfigWriteSchema) },
      responses: { 200: json(ModelConfigSchema), ...errorResponses },
    }),
    (c) => {
      const { credentials, ...config } = c.req.valid('json')
      store.saveModels(config, credentials)
      store.log('info', 'config', 'models_updated')
      return c.json(store.models, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/providers/{id}/models',
      request: { params: z.object({ id: z.string() }) },
      responses: { 200: json(z.object({ models: z.array(z.string()) })), ...errorResponses },
    }),
    async (c) => {
      const models = await engine.router().listModels(c.req.valid('param').id)
      return c.json({ models }, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/settings',
      responses: { 200: json(SettingsSchema), ...errorResponses },
    }),
    (c) => c.json(store.settings, 200),
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/settings',
      request: { body: body(SettingsSchema) },
      responses: { 200: json(SettingsSchema), ...errorResponses },
    }),
    (c) => {
      const settings = c.req.valid('json')
      if (settings.persona !== store.settings.persona) {
        const versions = store.get<Array<{ id: string; createdAt: string; content: string }>>(
          'personaVersions',
          [],
        )
        versions.push({
          id: randomUUID(),
          createdAt: new Date().toISOString(),
          content: settings.persona,
        })
        store.set('personaVersions', versions)
      }
      store.set('settings', settings)
      engine.settingsChanged()
      store.log('info', 'config', 'settings_updated')
      return c.json(settings, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/scenes',
      responses: { 200: json(z.array(SceneSchema)), ...errorResponses },
    }),
    (c) => c.json(store.scenes(), 200),
  )
  app.openapi(
    createRoute({
      method: 'put',
      path: '/api/scenes/{id}',
      request: { params: z.object({ id: z.string() }), body: body(SceneSchema) },
      responses: { 200: json(SceneSchema), ...errorResponses },
    }),
    (c) => {
      const scene = c.req.valid('json')
      if (scene.id !== c.req.valid('param').id) return c.json({ error: 'Scene ID mismatch' }, 400)
      store.saveScene(scene)
      const saved = store.scene(scene.id)!
      engine.sceneChanged(saved)
      return c.json(saved, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/logs',
      responses: { 200: json(z.array(LogSchema)), ...errorResponses },
    }),
    (c) => c.json(store.logs.toReversed(), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/logs/status',
      responses: { 200: json(LogDeliverySchema), ...errorResponses },
    }),
    (c) => c.json(store.loggingStatus, 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/model-calls',
      request: { query: ModelCallsQuerySchema },
      responses: { 200: json(ModelCallsPageSchema), ...errorResponses },
    }),
    (c) => c.json(store.modelCalls(c.req.valid('query')), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/usage',
      request: {
        query: z
          .object({
            timeZone: z
              .string()
              .max(100)
              .refine((value) => !!usageTimeZone(value), 'Invalid IANA timezone')
              .optional()
              .describe(
                'Browser IANA timezone for precise buckets; defaults to UTC. Historical UTC daily aggregates cannot be rebucketed into non-UTC days.',
              ),
            bucket: z
              .enum(['hour', 'day'])
              .describe('Aggregation bucket; requires start and end. Defaults to day.')
              .optional(),
            start: z.iso
              .datetime({ offset: true })
              .describe('Inclusive accounting timestamp. Must be supplied with end.')
              .optional(),
            end: z.iso
              .datetime({ offset: true })
              .describe('Exclusive accounting timestamp, later than start.')
              .optional(),
          })
          .refine(
            (q) =>
              (!q.start && !q.end && !q.bucket && !q.timeZone) ||
              (!!q.start && !!q.end && Date.parse(q.start) < Date.parse(q.end)),
            'Supply an ordered start and end',
          ),
      },
      responses: {
        200: {
          ...json(z.array(UsageSchema)),
          headers: {
            'X-Usage-Planner-Incomplete': {
              description: 'Same coverage test limited to Planner records.',
              schema: { type: 'string', enum: ['true', 'false'] },
            },
            'X-Usage-Incomplete': {
              description:
                'True when historical daily records overlap but cannot be precisely included in the requested range or hourly buckets.',
              schema: { type: 'string', enum: ['true', 'false'] },
            },
          },
        },
        ...errorResponses,
      },
    }),
    (c) => {
      const { start, end, bucket, timeZone } = c.req.valid('query')
      const range = start && end ? { start, end, bucket, timeZone } : undefined
      c.header('X-Usage-Incomplete', range && store.usageIncomplete(range) ? 'true' : 'false')
      c.header(
        'X-Usage-Planner-Incomplete',
        range && store.usageIncomplete(range, 'planner') ? 'true' : 'false',
      )
      return c.json(store.usage(range), 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/diagnostics',
      responses: { 200: json(z.array(DiagnosticSchema)), ...errorResponses },
    }),
    (c) => c.json(store.diagnostics(), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/model-protocols',
      responses: {
        200: json(
          z.array(
            z.object({
              id: z.string(),
              label: z.string(),
              operations: z.array(z.enum(['complete', 'embeddings', 'listModels'])),
            }),
          ),
        ),
        ...errorResponses,
      },
    }),
    (c) => c.json(engine.protocols.list(), 200),
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/scenes/{id}/state',
      request: { params: z.object({ id: z.string() }) },
      responses: { 200: json(SceneStateSchema), ...errorResponses },
    }),
    (c) =>
      store.scene(c.req.valid('param').id)
        ? c.json(engine.runtime.sceneState(c.req.valid('param').id), 200)
        : c.json({ error: 'Scene unavailable' }, 400),
  )
  installEngineRoutes(app, engine)
  installCommitmentRoutes(app, { memory: engine.memory, store })
  installWorkspaceRoutes(app, { memory: engine.memory, store })
  app.doc('/api/openapi.json', {
    openapi: '3.1.0',
    info: { title: 'HumanEngine API', version: '0.1.0' },
    security: [{ adminToken: [] }],
  })
  app.openAPIRegistry.registerComponent('securitySchemes', 'adminToken', {
    type: 'http',
    scheme: 'bearer',
  })
  return app
}
