import { z } from '@hono/zod-openapi'

export const Id = z.string().regex(/^[a-zA-Z0-9_.:-]{1,160}$/)
export const SecretMutationSchema = z
  .discriminatedUnion('action', [
    z.object({ action: z.literal('preserve') }).strict(),
    z.object({ action: z.literal('clear') }).strict(),
    z
      .object({
        action: z.literal('set'),
        value: z
          .string()
          .min(1)
          .max(16384)
          .refine((v) => !!v.trim()),
      })
      .strict(),
  ])
  .openapi('SecretMutation')

export const ProviderSchema = z
  .object({
    id: Id,
    name: z.string().min(1).max(100),
    protocol: z
      .string()
      .max(100)
      .regex(/^(?:openai-chat|openai-responses|[a-z0-9-]+:[a-z0-9-]+)$/),
    baseUrl: z
      .url()
      .refine(
        (value) =>
          ['http:', 'https:'].includes(new URL(value).protocol) &&
          !new URL(value).username &&
          !new URL(value).password &&
          !new URL(value).search &&
          !new URL(value).hash,
        'Use an HTTP(S) URL without credentials',
      ),
    credentialEnv: z
      .string()
      .regex(/^(?:[A-Z][A-Z0-9_]*)?$/)
      .default(''),
    credentialConfigured: z.boolean().optional(),
  })
  .strict()
  .openapi('Provider')
export const ModelSchema = z
  .object({
    id: Id,
    displayName: z.string().min(1).max(100),
    providerId: Id,
    apiModelName: z.string().min(1).max(200),
    capabilities: z.array(z.enum(['text', 'tools', 'vision', 'embedding'])).min(1),
    embeddingBatchSize: z.number().int().min(1).max(64).default(4),
    embeddingDimensions: z.number().int().min(1).max(16384).optional(),
    inputUsdPerMillion: z.number().min(0).optional(),
    outputUsdPerMillion: z.number().min(0).optional(),
  })
  .strict()
  .openapi('ModelInstance')
export const RoleSchema = z.enum(['planner', 'thinker', 'learning', 'vision', 'embedding'])
export const RouteSchema = z
  .object({
    role: RoleSchema,
    orderedModelIds: z.array(Id),
    timeoutMs: z.number().int().min(100).max(300000),
  })
  .strict()
  .openapi('RoleRoute')
export const ModelConfigSchema = z
  .object({
    providers: z.array(ProviderSchema),
    models: z.array(ModelSchema),
    routes: z.array(RouteSchema),
  })
  .strict()
  .superRefine((c, ctx) => {
    for (const [name, rows] of [
      ['providers', c.providers],
      ['models', c.models],
    ] as const) {
      if (new Set(rows.map((r) => r.id)).size !== rows.length)
        ctx.addIssue({ code: 'custom', message: `Duplicate ${name} IDs` })
    }
    if (new Set(c.routes.map((r) => r.role)).size !== c.routes.length)
      ctx.addIssue({ code: 'custom', message: 'Duplicate role routes' })
    for (const m of c.models)
      if (!c.providers.some((p) => p.id === m.providerId))
        ctx.addIssue({ code: 'custom', message: 'Unknown provider' })
    for (const r of c.routes)
      if (
        new Set(r.orderedModelIds).size !== r.orderedModelIds.length ||
        r.orderedModelIds.some((id) => !c.models.some((m) => m.id === id))
      )
        ctx.addIssue({ code: 'custom', message: 'Invalid route model IDs' })
  })
  .openapi('ModelConfig')
// Write-only operations are separate from the serializable model configuration.
export const ModelConfigWriteSchema = ModelConfigSchema.safeExtend({
  credentials: z.record(Id, SecretMutationSchema).optional(),
}).openapi('ModelConfigWrite')
export const SettingsSchema = z
  .object({
    name: z.string().min(1).max(100),
    timezone: z.string().refine((value) => {
      try {
        new Intl.DateTimeFormat('en', { timeZone: value })
        return true
      } catch {
        return false
      }
    }, 'Invalid timezone'),
    persona: z.string().min(1).max(20000),
    cacheEnabled: z.boolean(),
    cacheHours: z.number().min(0.1).max(168),
    debounceMs: z.number().int().min(0).max(30000),
    maxWaitMs: z.number().int().min(100).max(60000),
    learningBatchMs: z.number().int().min(100).max(300000).default(30000),
    learningQueueMaxMessages: z.number().int().min(60).max(20000).default(2048),
    learningQueueMaxTotalMessages: z.number().int().min(60).max(100000).default(8192),
    learningRetryAttempts: z.number().int().min(1).max(5).default(3),
    learningRetryBaseMs: z.number().int().min(100).max(30000).default(1000),
    debugModelIO: z.boolean().default(false),
    modelConcurrency: z.number().int().min(1).max(32).default(4),
    participationWindows: z
      .array(
        z
          .object({
            startHour: z.number().int().min(0).max(23),
            endHour: z.number().int().min(1).max(24),
            inclination: z.enum(['quiet', 'balanced', 'engaged']),
          })
          .strict()
          .refine((w) => w.startHour < w.endHour, 'Use a non-wrapping time window'),
      )
      .max(12)
      .default([]),
  })
  .strict()
  .openapi('Settings')
export const SceneSchema = z
  .object({
    id: Id,
    kind: z.enum(['group', 'private']),
    adapterId: Id,
    accountId: z.string(),
    platformSceneId: z.string(),
    name: z.string(),
    avatarUrl: z.string().optional(),
    participationEnabled: z.boolean(),
    learningEnabled: z.boolean(),
    proactiveEnabled: z.boolean(),
  })
  .strict()
  .refine((s) => s.kind === 'group' || !s.proactiveEnabled, 'Private scenes cannot be proactive')
  .openapi('Scene')
const ErrorContextSchema = z.object({
  operation: z.string().optional(),
  stage: z.string().optional(),
  role: z.string().optional(),
  modelId: z.string().optional(),
  providerId: z.string().optional(),
  apiModel: z.string().optional(),
  attempt: z.number().optional(),
  timeoutMs: z.number().optional(),
  remainingCandidates: z.number().optional(),
  retryable: z.boolean().optional(),
})
const ErrorCauseDiagnosticSchema = z.object({
  errorType: z.string(),
  errorCode: z.string().optional(),
  errorMessage: z.string().optional(),
  adviceCode: z.string().optional(),
  errorContext: ErrorContextSchema.optional(),
  systemCode: z.string().optional(),
  httpStatus: z.number().int().min(100).max(599).optional(),
  stackFrames: z.array(z.string()).max(8),
})
export const LogSchema = z
  .object({
    id: z.string(),
    at: z.string(),
    level: z.enum(['info', 'warn', 'error']),
    module: z.string(),
    code: z.string(),
    message: z.string().optional(),
    traceId: z.string().optional(),
    spanId: z.string().optional(),
    parentSpanId: z.string().optional(),
    parentTraceId: z.string().optional(),
    linkedTraceIds: z.array(z.string()).max(60).optional(),
    batchId: z.string().optional(),
    stage: z.string().optional(),
    status: z
      .enum([
        'started',
        'completed',
        'failed',
        'skipped',
        'cancelled',
        'cancel_requested',
        'partial',
      ])
      .optional(),
    reason: z.string().optional(),
    nextStep: z.string().optional(),
    counts: z.record(z.string(), z.number()).optional(),
    ruleIds: z.array(z.string()).max(32).optional(),
    role: z.string().optional(),
    apiModel: z.string().optional(),
    toolName: z.string().optional(),
    action: z.string().optional(),
    outputKind: z.string().optional(),
    receiptState: z.string().optional(),
    mode: z.string().optional(),
    operation: z.string().optional(),
    phase: z.string().optional(),
    errorType: z.string().optional(),
    errorCode: z.string().optional(),
    errorMessage: z.string().optional(),
    adviceCode: z.string().optional(),
    errorContext: ErrorContextSchema.optional(),
    rootCause: ErrorCauseDiagnosticSchema.optional(),
    failures: z.array(ErrorCauseDiagnosticSchema).max(4).optional(),
    stackFrames: z.array(z.string()).max(8).optional(),
    causes: z.array(ErrorCauseDiagnosticSchema).max(4).optional(),
    requestId: z.string().optional(),
    systemCode: z.string().optional(),
    httpStatus: z.number().optional(),
    sceneId: z.string().optional(),
    turnId: z.string().optional(),
    modelId: z.string().optional(),
    providerId: z.string().optional(),
    durationMs: z.number().optional(),
  })
  .openapi('Log')
export const StatusSchema = z
  .object({
    name: z.string(),
    startedAt: z.string(),
    uptimeSeconds: z.number(),
    sceneCount: z.number(),
    enabledScenes: z.number(),
    providerCount: z.number(),
    modelCount: z.number(),
    activeTasks: z.number(),
    recentErrors: z.number(),
    vectorVersion: z.string(),
    version: z.string(),
    queuedModelRequests: z.number(),
    runningModelRequests: z.number(),
    adapters: z.array(z.object({ id: z.string(), status: z.string() })),
  })
  .openapi('Status')
export const ErrorSchema = z
  .object({ error: z.string(), requestId: z.string().optional() })
  .openapi('Error')
export type ModelConfig = z.infer<typeof ModelConfigSchema>
export type Settings = z.infer<typeof SettingsSchema>
export type Scene = z.infer<typeof SceneSchema>
export type Log = z.infer<typeof LogSchema>

export const UsageSchema = z
  .object({
    day: z.string(),
    modelId: z.string(),
    providerId: z.string(),
    apiModelName: z.string(),
    role: z.string(),
    calls: z.number(),
    inputTokens: z.number(),
    outputTokens: z.number(),
    durationMs: z.number(),
    pricedCalls: z.number(),
    estimatedUsd: z.number(),
  })
  .openapi('Usage')

export const DiagnosticSchema = z
  .object({
    id: z.string(),
    at: z.string(),
    modelId: z.string(),
    role: z.string(),
    input: z.string(),
    output: z.string(),
  })
  .openapi('Diagnostic')

export const SceneStateSchema = z
  .object({
    activeTasks: z.number(),
    secondsSinceBotSpoke: z.number().nullable(),
    topics: z.array(
      z.object({
        id: z.string(),
        attention: z.number(),
        ageMs: z.number(),
        messageCount: z.number(),
      }),
    ),
  })
  .openapi('SceneState')
