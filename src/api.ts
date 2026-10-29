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
import {
  ModelConfigSchema,
  ModelConfigWriteSchema,
  SettingsSchema,
  SceneSchema,
  LogSchema,
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
    store.assertRecoveryReady()
    await next()
    // Pending external revocations can appear while an asynchronous route reads bytes.
    store.assertRecoveryReady()
    c.header('Cache-Control', 'no-store')
    c.header('X-Content-Type-Options', 'nosniff')
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
      path: '/api/usage',
      responses: { 200: json(z.array(UsageSchema)), ...errorResponses },
    }),
    (c) => c.json(store.usage(), 200),
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
