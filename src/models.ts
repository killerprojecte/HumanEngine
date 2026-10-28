import { canonicalModelRole, normalizeRoleRoutes, type ModelRole } from './model-roles.ts'
export type { ModelRole, CanonicalModelRole } from './model-roles.ts'
import {
  providerPolicy,
  providerAttemptsHandled,
  markProviderAttemptsHandled,
  providerOperationBudget,
  parseRetryAfter,
  runProviderAttempts,
  waitRetry,
} from './models/request-policy.ts'
import { preserveErrorContext, errorContext } from './diagnostics/error-context.ts'
import { withRoleBudget } from './models/role-budget.ts'
import {
  ModelProtocolRegistry,
  ModelProtocolError,
  isBuiltinProtocol,
  type ModelProtocolContext,
  type ProtocolRegistration,
} from './model-protocols.ts'
import { currentFlowWriter, startFlow, type FlowWriter } from './diagnostics/flow.ts'
import {
  observeModel,
  completionDetails,
  modelUsageCounts,
  scheduleModel,
} from './diagnostics/model-flow.ts'
export interface Provider {
  id: string
  name: string
  protocol: string
  baseUrl: string
  credentialEnv: string
  credentialConfigured?: boolean
  retryCount?: number
  timeoutMs?: number
}
export interface ModelInstance {
  id: string
  displayName: string
  providerId: string
  apiModelName: string
  capabilities: Array<'text' | 'tools' | 'vision' | 'embedding'>
  inputUsdPerMillion?: number
  outputUsdPerMillion?: number
  cacheReadUsdPerMillion?: number
  cacheWriteUsdPerMillion?: number
  cacheWrite5mUsdPerMillion?: number
  cacheWrite1hUsdPerMillion?: number
  cacheWrite30mUsdPerMillion?: number
  cacheWrite12hUsdPerMillion?: number
  /** Maximum inputs per sequential embedding request; defaults to four. */
  embeddingBatchSize?: number
  /** Omit for provider-native output; explicit values require provider support. */
  embeddingDimensions?: number
}
export interface RoleRoute {
  role: ModelRole
  orderedModelIds: string[]
  timeoutMs: number
}
export interface ToolDefinition {
  effect?: 'read' | 'learningCandidate' | 'send'
  name: string
  description: string
  inputSchema: Record<string, unknown>
}
export interface ToolCall {
  id: string
  name: string
  arguments: Record<string, unknown>
}
export interface ChatMessage {
  role: 'system' | 'user' | 'assistant' | 'tool'
  content: string
  images?: string[]
  toolCalls?: ToolCall[]
  toolCallId?: string
}
export interface Usage {
  /** False when provider counters are missing; zero is not evidence of free usage. */
  known?: boolean
  inputTokens: number
  outputTokens: number
  totalTokens: number
  /** Input totals include ordinary input, cache reads, and cache writes exactly once. */
  ordinaryInputTokens?: number
  cacheReadInputTokens?: number
  cacheWriteInputTokens?: number
  cacheWrite5mInputTokens?: number
  cacheWrite1hInputTokens?: number
  cacheWrite30mInputTokens?: number
  cacheWrite12hInputTokens?: number
  /** True only when the complete input partition is reported and internally consistent. */
  cacheKnown?: boolean
  /** Fixed allowlisted numeric provider counters only, never a raw response body. */
  rawTokenCounters?: Record<string, number>
}
export interface Completion {
  text: string
  toolCalls: ToolCall[]
  usage: Usage
  modelId: string
  assistant: ChatMessage
}
export interface RouterConfig {
  providers: Provider[]
  models: ModelInstance[]
  routes: RoleRoute[]
}
export interface ModelDiagnostic {
  role: ModelRole
  modelId: string
  messages: ChatMessage[]
  output: { text: string; toolCalls: ToolCall[] }
}
export interface ModelPriceSnapshot {
  apiModelName: string
  inputUsdPerMillion?: number
  outputUsdPerMillion?: number
  cacheReadUsdPerMillion?: number
  cacheWriteUsdPerMillion?: number
  cacheWrite5mUsdPerMillion?: number
  cacheWrite1hUsdPerMillion?: number
  cacheWrite30mUsdPerMillion?: number
  cacheWrite12hUsdPerMillion?: number
}
export interface ModelLog {
  event: 'model.completed' | 'model.failed'
  modelId: string
  providerId: string
  role: ModelRole | 'models'
  durationMs: number
  code?: string
  usage?: Usage
  pricing?: ModelPriceSnapshot
}
export class ModelError extends Error {
  readonly retryAfterMs?: number
  constructor(
    public readonly code: string,
    public readonly status?: number,
    public readonly retryable = status !== undefined && (status === 429 || status >= 500),
    options: { cause?: unknown; failures?: unknown[]; retryAfterMs?: number } = {},
  ) {
    super(code, options)
    if (providerAttemptsHandled(options.cause) || options.failures?.some(providerAttemptsHandled))
      markProviderAttemptsHandled(this)
    if (options.failures)
      Object.defineProperty(this, 'failures', { value: options.failures.slice(0, 4) })
    if (options.retryAfterMs !== undefined) this.retryAfterMs = options.retryAfterMs
    this.name = 'ModelError'
  }
}
function isTransientNetworkFailure(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const own = (value: object, key: string) => Object.getOwnPropertyDescriptor(value, key)?.value
  const code = own(error, 'code')
  if (
    typeof code === 'string' &&
    [
      'ECONNRESET',
      'ECONNREFUSED',
      'ETIMEDOUT',
      'EAI_AGAIN',
      'ENETUNREACH',
      'EHOSTUNREACH',
      'EPIPE',
      'UND_ERR_CONNECT_TIMEOUT',
      'UND_ERR_SOCKET',
    ].includes(code)
  )
    return true
  const cause = own(error, 'cause')
  return (
    !!cause &&
    typeof cause === 'object' &&
    cause !== error &&
    typeof own(cause, 'code') === 'string' &&
    [
      'ECONNRESET',
      'ECONNREFUSED',
      'ETIMEDOUT',
      'EAI_AGAIN',
      'ENETUNREACH',
      'EHOSTUNREACH',
      'EPIPE',
      'UND_ERR_CONNECT_TIMEOUT',
      'UND_ERR_SOCKET',
    ].includes(own(cause, 'code'))
  )
}
export { providerOperationBudget }
type JsonObject = Record<string, unknown>
const object = (value: unknown): JsonObject => {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new ModelError('invalid_provider_response')
  return value as JsonObject
}
const string = (value: unknown): string => {
  if (typeof value !== 'string') throw new ModelError('invalid_provider_response')
  return value
}
const array = (value: unknown): unknown[] => {
  if (!Array.isArray(value)) throw new ModelError('invalid_provider_response')
  return value
}
const validTokenCount = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
const tokenCount = (value: unknown): number => (validTokenCount(value) ? value : 0)
const cacheUsageKeys = [
  'ordinaryInputTokens',
  'cacheReadInputTokens',
  'cacheWriteInputTokens',
  'cacheWrite5mInputTokens',
  'cacheWrite1hInputTokens',
  'cacheWrite30mInputTokens',
  'cacheWrite12hInputTokens',
] as const
const rawCounterPaths = [
  'input_tokens',
  'output_tokens',
  'total_tokens',
  'prompt_tokens',
  'completion_tokens',
  'input_tokens_details.cached_tokens',
  'input_tokens_details.cache_write_tokens',
  'input_tokens_details.cache_write_tokens_details.ttl_30m_tokens',
  'input_tokens_details.cache_write_tokens_details.ttl_12h_tokens',
  'prompt_tokens_details.cached_tokens',
  'prompt_tokens_details.cache_write_tokens',
  'prompt_tokens_details.cache_write_tokens_details.ttl_30m_tokens',
  'prompt_tokens_details.cache_write_tokens_details.ttl_12h_tokens',
  'prompt_cache_hit_tokens',
  'prompt_cache_miss_tokens',
  'cache_read_input_tokens',
  'cache_creation_input_tokens',
  'cache_creation.ephemeral_5m_input_tokens',
  'cache_creation.ephemeral_1h_input_tokens',
] as const
const atPath = (value: unknown, path: string): unknown =>
  path
    .split('.')
    .reduce<unknown>(
      (part, key) =>
        part && typeof part === 'object' && !Array.isArray(part)
          ? (part as Record<string, unknown>)[key]
          : undefined,
      value,
    )
const rawCounters = (value: unknown): Record<string, number> =>
  Object.fromEntries(
    rawCounterPaths
      .map((path) => [path, atPath(value, path)] as const)
      .filter((entry): entry is readonly [(typeof rawCounterPaths)[number], number] =>
        validTokenCount(entry[1]),
      ),
  )

/** Raw provider counts only. Anthropic's exclusive-input convention must be selected explicitly. */
export function normalizeProviderUsage(
  value: unknown,
  convention: 'openai' | 'anthropic' = 'openai',
  embedding = false,
): Usage {
  const usage =
    value && typeof value === 'object' && !Array.isArray(value) ? (value as JsonObject) : {}
  let invalid = false
  const read = (...paths: string[]) => {
    const values = paths.map((path) => atPath(usage, path)).filter((value) => value !== undefined)
    if (values.some((value) => !validTokenCount(value))) invalid = true
    const valid = values.filter(validTokenCount)
    if (new Set(valid).size > 1) invalid = true
    return valid[0]
  }
  const input = read('input_tokens', ...(convention === 'openai' ? ['prompt_tokens'] : []))
  const output = read('output_tokens', ...(convention === 'openai' ? ['completion_tokens'] : []))
  const total = read('total_tokens')
  const result: Usage = { inputTokens: input ?? 0, outputTokens: output ?? 0, totalTokens: 0 }
  if (input === undefined || (!embedding && output === undefined)) invalid = true
  const anthropicShape = [
    'cache_read_input_tokens',
    'cache_creation_input_tokens',
    'cache_creation',
  ].some((key) => Object.hasOwn(usage, key))
  const hasCache =
    anthropicShape ||
    [
      'input_tokens_details',
      'prompt_tokens_details',
      'prompt_cache_hit_tokens',
      'prompt_cache_miss_tokens',
    ].some((key) => Object.hasOwn(usage, key))
  if (hasCache) {
    result.cacheKnown = false
    result.rawTokenCounters = rawCounters(usage)
    let cached: number | undefined, written: number | undefined
    if (convention === 'anthropic') {
      cached = read('cache_read_input_tokens')
      written = read('cache_creation_input_tokens')
      const five = read('cache_creation.ephemeral_5m_input_tokens'),
        hour = read('cache_creation.ephemeral_1h_input_tokens')
      if (five !== undefined) result.cacheWrite5mInputTokens = five
      if (hour !== undefined) result.cacheWrite1hInputTokens = hour
      if (written === undefined && five !== undefined && hour !== undefined) written = five + hour
      if (
        written !== undefined &&
        five !== undefined &&
        hour !== undefined &&
        five + hour !== written
      )
        invalid = true
      if (input !== undefined && cached !== undefined && written !== undefined)
        result.inputTokens = input + cached + written
      else invalid = true // The inclusive input total is unknown if an exclusive component is missing.
    } else {
      if (anthropicShape) invalid = true // Never guess that an OpenAI-compatible wrapper uses exclusive totals.
      cached = read(
        'input_tokens_details.cached_tokens',
        'prompt_tokens_details.cached_tokens',
        'prompt_cache_hit_tokens',
      )
      written = read(
        'input_tokens_details.cache_write_tokens',
        'prompt_tokens_details.cache_write_tokens',
      )
      const halfHour = read(
        'input_tokens_details.cache_write_tokens_details.ttl_30m_tokens',
        'prompt_tokens_details.cache_write_tokens_details.ttl_30m_tokens',
      )
      const halfDay = read(
        'input_tokens_details.cache_write_tokens_details.ttl_12h_tokens',
        'prompt_tokens_details.cache_write_tokens_details.ttl_12h_tokens',
      )
      if (halfHour !== undefined) result.cacheWrite30mInputTokens = halfHour
      if (halfDay !== undefined) result.cacheWrite12hInputTokens = halfDay
      if (
        written !== undefined &&
        halfHour !== undefined &&
        halfDay !== undefined &&
        halfHour + halfDay !== written
      )
        invalid = true
      const miss = read('prompt_cache_miss_tokens')
      if (miss !== undefined && cached !== undefined) {
        if (
          input === undefined ||
          cached + miss !== input ||
          (written !== undefined && written !== 0)
        )
          invalid = true
        written ??= 0 // DeepSeek cache misses are ordinary input, not a separately billed write bucket.
      }
    }
    if (cached !== undefined) result.cacheReadInputTokens = cached
    if (written !== undefined) result.cacheWriteInputTokens = written
    const writes = [
      result.cacheWrite5mInputTokens,
      result.cacheWrite1hInputTokens,
      result.cacheWrite30mInputTokens,
      result.cacheWrite12hInputTokens,
    ]
    const typedWriteSum = writes.reduce<number>((sum, count) => sum + (count ?? 0), 0)
    if (
      !validTokenCount(typedWriteSum) ||
      typedWriteSum > result.inputTokens ||
      (written !== undefined && typedWriteSum > written)
    )
      invalid = true
    if ((cached ?? 0) + (written ?? 0) > result.inputTokens) invalid = true
    if (cached !== undefined && written !== undefined && !invalid) {
      result.ordinaryInputTokens = result.inputTokens - cached - written
      result.cacheKnown = true
    }
  } else if (embedding && convention === 'openai' && !invalid) {
    // Native embedding usage has only prompt/total counts, with no cache billing category.
    result.cacheKnown = true
    result.ordinaryInputTokens = result.inputTokens
    result.cacheReadInputTokens = 0
    result.cacheWriteInputTokens = 0
  } else if (convention === 'anthropic') {
    // Without explicit cache components, exclusive input alone cannot establish the inclusive total.
    invalid = true
  }
  const sum = result.inputTokens + result.outputTokens
  result.totalTokens = validTokenCount(sum) ? sum : 0
  if (
    !validTokenCount(result.inputTokens) ||
    !validTokenCount(sum) ||
    (total !== undefined && total !== sum)
  )
    invalid = true
  if (!validTokenCount(result.inputTokens)) result.inputTokens = 0
  if (invalid) {
    result.known = false
    if (result.cacheKnown !== undefined) result.cacheKnown = false
    delete result.ordinaryInputTokens
  }
  return result
}
const usageOf = (value: unknown, embedding = false): Usage =>
  normalizeProviderUsage(value, 'openai', embedding)

const wireName = (name: string) => name.replaceAll('.', '__')
const abortError = (cause?: unknown) =>
  new ModelError('request_cancelled', undefined, false, { cause })

/** Protocol state is transient, opaque, and never serialized into logs or persisted messages. */
export class Router {
  private readonly state = new WeakMap<ChatMessage, { modelId: string; output: unknown[] }>()
  private readonly continuations = new WeakMap<
    ChatMessage,
    { modelId: string; generation: number; value: unknown }
  >()
  private readonly config: RouterConfig
  constructor(
    config: RouterConfig,
    private readonly log: (entry: ModelLog) => void = () => {},
    private readonly diagnostic: (
      entry: ModelDiagnostic,
      credentialValues: readonly string[],
    ) => void = () => {},
    private readonly protocols?: ModelProtocolRegistry,
    private readonly resolveCredential: (provider: Provider) => string | undefined = (provider) =>
      provider.credentialEnv ? process.env[provider.credentialEnv] : undefined,
    private readonly flowWriter?: FlowWriter,
  ) {
    this.config = structuredClone({ ...config, routes: normalizeRoleRoutes(config.routes) })
  }

  async complete(
    role: Exclude<ModelRole, 'embedding'>,
    messages: ChatMessage[],
    tools: ToolDefinition[] = [],
    signal?: AbortSignal,
  ): Promise<Completion> {
    role = canonicalModelRole(role)
    return observeModel(
      this.flowWriter,
      'model_request',
      {
        role,
        counts: { messages: messages.length, offeredTools: tools.length },
        nextStep: 'select_model',
      },
      signal,
      () =>
        this.withRoleBudget(role, signal, (bounded) =>
          this.completeRequest(role, messages, tools, bounded),
        ),
      completionDetails,
    )
  }

  private async completeRequest(
    role: Exclude<ModelRole, 'embedding'>,
    messages: ChatMessage[],
    tools: ToolDefinition[] = [],
    signal?: AbortSignal,
  ): Promise<Completion> {
    if (signal?.aborted) throw abortError()
    const route = this.config.routes.find((item) => item.role === role)
    if (!route) throw new ModelError('route_not_configured')
    const names = new Map(tools.map((tool) => [wireName(tool.name), tool.name]))
    if (
      names.size !== tools.length ||
      [...names.keys()].some((name) => !/^[a-zA-Z0-9_-]{1,64}$/.test(name))
    )
      throw new ModelError('invalid_tool_names')
    let attempted = false
    let retryable = true
    let lastStatus: number | undefined
    const failures: unknown[] = []
    for (const [index, modelId] of route.orderedModelIds.entries()) {
      if (signal?.aborted) throw abortError()
      const model = this.config.models.find((item) => item.id === modelId)
      if (
        !model ||
        !model.capabilities.includes('text') ||
        (tools.length && !model.capabilities.includes('tools')) ||
        (messages.some((item) => item.images?.length) && !model.capabilities.includes('vision'))
      ) {
        const skipped = startFlow(this.flowWriter ?? currentFlowWriter(), 'model_attempt', {
          role,
          modelId,
          counts: { candidateIndex: index + 1 },
        })
        skipped.finish('skipped', {
          reason: !model ? 'model_not_configured' : 'model_capability_mismatch',
          nextStep: 'select_next_model',
        })
        continue
      }
      const provider = this.config.providers.find((item) => item.id === model.providerId)
      if (!provider) {
        const skipped = startFlow(this.flowWriter ?? currentFlowWriter(), 'model_attempt', {
          role,
          modelId,
          counts: { candidateIndex: index + 1 },
        })
        skipped.finish('skipped', {
          reason: 'provider_not_configured',
          nextStep: 'select_next_model',
        })
        continue
      }
      attempted = true
      const started = Date.now()
      try {
        return await observeModel(
          this.flowWriter,
          'model_attempt',
          {
            role,
            modelId,
            providerId: provider.id,
            apiModel: model.apiModelName,
            counts: {
              candidateIndex: index + 1,
              timeoutMs: providerPolicy(provider).timeoutMs,
              remainingCandidates: route.orderedModelIds.length - index - 1,
            },
            nextStep: 'wait_for_model_slot',
          },
          signal,
          () =>
            this.withProviderAttempts(
              provider,
              { role, modelId, apiModel: model.apiModelName },
              signal,
              async () => {
                if (!isBuiltinProtocol(provider.protocol))
                  return await this.completeProtocol(
                    provider,
                    model,
                    role,
                    messages,
                    tools,
                    providerPolicy(provider).timeoutMs,
                    signal,
                  )
                const responses = provider.protocol === 'openai-responses'
                const body = responses
                  ? {
                      model: model.apiModelName,
                      input: this.responsesInput(messages, modelId),
                      store: false,
                      stream: false,
                      include: ['reasoning.encrypted_content'],
                      ...(tools.length
                        ? {
                            tools: tools.map((tool) => ({
                              type: 'function',
                              name: wireName(tool.name),
                              description: tool.description,
                              parameters: tool.inputSchema,
                              strict: false,
                            })),
                          }
                        : {}),
                    }
                  : {
                      model: model.apiModelName,
                      messages: messages.map((message) => this.chatInput(message)),
                      stream: false,
                      ...(tools.length
                        ? {
                            tools: tools.map((tool) => ({
                              type: 'function',
                              function: {
                                name: wireName(tool.name),
                                description: tool.description,
                                parameters: tool.inputSchema,
                                strict: false,
                              },
                            })),
                          }
                        : {}),
                    }
                const requestSecrets: string[] = []
                const result = await scheduleModel(
                  role === 'planner' || role === 'replyer' ? 0 : 1,
                  signal,
                  () =>
                    this.request(
                      provider,
                      responses ? 'responses' : 'chat/completions',
                      body,
                      providerPolicy(provider).timeoutMs,
                      signal,
                      (secret) => requestSecrets.push(secret),
                    ),
                )
                if (signal?.aborted) throw abortError()
                const parsed = responses
                  ? this.parseResponses(result, names)
                  : this.parseChat(result, names)
                const assistant: ChatMessage = {
                  role: 'assistant',
                  content: parsed.text,
                  toolCalls: parsed.toolCalls,
                }
                if (responses) this.state.set(assistant, { modelId, output: array(result.output) })
                const completion = { ...parsed, modelId, assistant }
                try {
                  this.diagnostic(
                    {
                      role,
                      modelId,
                      messages,
                      output: { text: parsed.text, toolCalls: parsed.toolCalls },
                    },
                    requestSecrets,
                  )
                } catch {
                  /* Optional diagnostics cannot break model execution. */
                }
                this.emit({
                  event: 'model.completed',
                  modelId,
                  providerId: provider.id,
                  role,
                  durationMs: Date.now() - started,
                  usage: completion.usage,
                })
                return completion
              },
            ),
          completionDetails,
        )
      } catch (error) {
        if (signal?.aborted) throw abortError(error)
        preserveErrorContext(error, {
          operation: 'model.complete',
          stage: 'model_attempt',
          role,
          modelId,
          providerId: provider.id,
          apiModel: model.apiModelName,
          timeoutMs: providerPolicy(provider).timeoutMs,
          remainingCandidates: route.orderedModelIds.length - index - 1,
          retryable: error instanceof ModelError && error.retryable,
        })
        if (failures.length === 4) failures.shift()
        failures.push(error)
        retryable &&= error instanceof ModelError && error.retryable
        lastStatus = error instanceof ModelError ? error.status : undefined
        this.emit({
          event: 'model.failed',
          modelId,
          providerId: provider.id,
          role,
          durationMs: Date.now() - started,
          code: error instanceof ModelError ? error.code : 'provider_request_failed',
        })
        if (error instanceof ModelError && error.code === 'protocol_disabled') throw error
      }
    }
    throw new ModelError(
      attempted ? 'all_models_failed' : 'no_compatible_model',
      retryable && attempted ? lastStatus : undefined,
      attempted && retryable,
      { cause: failures.at(-1), failures },
    )
  }

  async embeddings(
    modelId: string,
    inputs: string[],
    signal?: AbortSignal,
  ): Promise<{ vectors: number[][]; usage: Usage; modelId: string }> {
    return observeModel(
      this.flowWriter,
      'model_request',
      {
        role: 'embedding',
        modelId,
        counts: { inputs: inputs.length },
        nextStep: 'prepare_embedding_batches',
      },
      signal,
      () =>
        this.withRoleBudget('embedding', signal, (bounded) =>
          this.embeddingsRequest(modelId, inputs, bounded),
        ),
      (value) => ({
        outputKind: 'embeddings',
        counts: {
          ...modelUsageCounts(value.usage),
          vectors: value.vectors.length,
          dimensions: value.vectors[0]?.length ?? 0,
        },
        nextStep: 'embedding_results_ready',
      }),
    )
  }

  private async embeddingsRequest(
    modelId: string,
    inputs: string[],
    signal?: AbortSignal,
  ): Promise<{ vectors: number[][]; usage: Usage; modelId: string }> {
    if (signal?.aborted) throw abortError()
    const model = this.config.models.find((item) => item.id === modelId)
    if (!model?.capabilities.includes('embedding'))
      throw new ModelError('embedding_model_not_configured')
    if (
      model.embeddingDimensions !== undefined &&
      (!Number.isSafeInteger(model.embeddingDimensions) ||
        model.embeddingDimensions < 1 ||
        model.embeddingDimensions > 16384)
    )
      throw new ModelError('invalid_embedding_dimensions_setting')
    const batchSize = model.embeddingBatchSize ?? 4
    if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 64)
      throw new ModelError('invalid_embedding_batch_size')
    // Snapshot inputs so callers cannot change the remaining batches while one is in flight.
    const pending = [...inputs]
    const vectors: number[][] = []
    const usage: Usage = { inputTokens: 0, outputTokens: 0, totalTokens: 0 }
    for (let offset = 0; offset < pending.length; offset += batchSize) {
      if (signal?.aborted) throw abortError()
      const batch = await this.embeddingBatch(
        modelId,
        pending.slice(offset, offset + batchSize),
        signal,
      )
      if (signal?.aborted) throw abortError()
      // Request accounting is already complete. Reject unusable output here so a
      // single paid response cannot also produce a duplicate model.failed event.
      this.validateEmbeddingVectors(model, batch.vectors)
      if (vectors.length && batch.vectors.some((vector) => vector.length !== vectors[0]!.length))
        throw new ModelError('invalid_embedding_dimensions')
      vectors.push(...batch.vectors)
      for (const key of ['inputTokens', 'outputTokens', 'totalTokens'] as const) {
        const sum = usage[key] + batch.usage[key]
        usage[key] = validTokenCount(sum) ? sum : 0
        if (!validTokenCount(sum)) usage.known = false
      }
      for (const key of cacheUsageKeys) {
        if (offset === 0 && batch.usage[key] !== undefined) usage[key] = batch.usage[key]
        else if (usage[key] !== undefined && batch.usage[key] !== undefined) {
          const sum = usage[key]! + batch.usage[key]!
          if (validTokenCount(sum)) usage[key] = sum
          else {
            delete usage[key]
            usage.known = false
          }
        } else delete usage[key]
      }
      if (offset === 0 && batch.usage.rawTokenCounters)
        usage.rawTokenCounters = { ...batch.usage.rawTokenCounters }
      else if (usage.rawTokenCounters && batch.usage.rawTokenCounters) {
        for (const key of Object.keys(usage.rawTokenCounters)) {
          const count = batch.usage.rawTokenCounters[key]
          const sum = usage.rawTokenCounters[key]! + (count ?? 0)
          if (count === undefined || !validTokenCount(sum)) delete usage.rawTokenCounters[key]
          else usage.rawTokenCounters[key] = sum
        }
      } else delete usage.rawTokenCounters
      if (offset === 0 && batch.usage.cacheKnown !== undefined)
        usage.cacheKnown = batch.usage.cacheKnown
      else if (usage.cacheKnown !== undefined || batch.usage.cacheKnown !== undefined)
        usage.cacheKnown = usage.cacheKnown === true && batch.usage.cacheKnown === true
      if (batch.usage.known === false) usage.known = false
      if (usage.known === false && usage.cacheKnown !== undefined) usage.cacheKnown = false
    }
    return { vectors, usage, modelId }
  }

  private async embeddingBatch(
    modelId: string,
    inputs: string[],
    signal?: AbortSignal,
  ): Promise<{ vectors: number[][]; usage: Usage; modelId: string }> {
    const model = this.config.models.find((item) => item.id === modelId)
    if (!model?.capabilities.includes('embedding'))
      throw new ModelError('embedding_model_not_configured')
    const provider = this.config.providers.find((item) => item.id === model.providerId)
    if (!provider) throw new ModelError('provider_not_configured')
    const timeout = providerPolicy(provider).timeoutMs
    const started = Date.now()
    try {
      return await observeModel(
        this.flowWriter,
        'model_attempt',
        {
          role: 'embedding',
          modelId,
          providerId: provider.id,
          apiModel: model.apiModelName,
          counts: { inputs: inputs.length },
          nextStep: 'wait_for_model_slot',
        },
        signal,
        () =>
          this.withProviderAttempts(
            provider,
            { role: 'embedding', modelId, apiModel: model.apiModelName },
            signal,
            async () => {
              if (!isBuiltinProtocol(provider.protocol))
                return await this.embeddingsProtocol(provider, model, inputs, timeout, signal)
              const result = await scheduleModel(1, signal, () =>
                this.request(
                  provider,
                  'embeddings',
                  {
                    model: model.apiModelName,
                    input: inputs,
                    encoding_format: 'float',
                    ...(model.embeddingDimensions === undefined
                      ? {}
                      : { dimensions: model.embeddingDimensions }),
                  },
                  timeout,
                  signal,
                ),
              )
              if (signal?.aborted) throw abortError()
              const rows = array(result.data)
                .map(object)
                .sort((a, b) => Number(a.index) - Number(b.index))
              if (rows.length !== inputs.length || rows.some((row, index) => row.index !== index))
                throw new ModelError('invalid_embedding_response')
              const vectors = rows.map((row) =>
                array(row.embedding).map((value) => {
                  if (typeof value !== 'number' || !Number.isFinite(value))
                    throw new ModelError('invalid_embedding_response')
                  return value
                }),
              )
              if (vectors.some((vector) => !vector.length || vector.length !== vectors[0]?.length))
                throw new ModelError('invalid_embedding_dimensions')
              const usage = usageOf(result.usage, true)
              this.emit({
                event: 'model.completed',
                modelId,
                providerId: provider.id,
                role: 'embedding',
                durationMs: Date.now() - started,
                usage,
              })
              return { vectors, usage, modelId }
            },
          ),
        (value) => ({
          outputKind: 'embeddings',
          counts: {
            ...modelUsageCounts(value.usage),
            vectors: value.vectors.length,
            dimensions: value.vectors[0]?.length ?? 0,
          },
          nextStep: 'validate_embedding_results',
        }),
      )
    } catch (error) {
      if (signal?.aborted) throw abortError()
      this.emit({
        event: 'model.failed',
        modelId,
        providerId: provider.id,
        role: 'embedding',
        durationMs: Date.now() - started,
        code: error instanceof ModelError ? error.code : 'provider_request_failed',
      })
      throw error instanceof ModelError
        ? error
        : new ModelError('provider_request_failed', undefined, false, { cause: error })
    }
  }

  async listModels(providerId: string, signal?: AbortSignal): Promise<string[]> {
    return observeModel(
      this.flowWriter,
      'model_request',
      { role: 'models', providerId, nextStep: 'discover_models' },
      signal,
      () => this.listModelsRequest(providerId, signal),
      (value) => ({
        outputKind: 'model_list',
        counts: { models: value.length },
        nextStep: 'model_list_ready',
      }),
    )
  }

  private async listModelsRequest(providerId: string, signal?: AbortSignal): Promise<string[]> {
    const provider = this.config.providers.find((item) => item.id === providerId)
    if (!provider) throw new ModelError('provider_not_configured')
    return this.withProviderAttempts(provider, { role: 'models' }, signal, () =>
      this.listModelsOnce(provider, signal),
    )
  }

  private async listModelsOnce(provider: Provider, signal?: AbortSignal): Promise<string[]> {
    if (!isBuiltinProtocol(provider.protocol)) {
      const started = Date.now()
      try {
        const call = await this.invokeProtocol(
          provider,
          undefined,
          'listModels',
          providerPolicy(provider).timeoutMs,
          1,
          signal,
          (context, registration) => registration.adapter.listModels!(context),
        )
        const models = array(call.value).map((value) => {
          const name = string(value)
          if (!name || name.length > 200) throw new ModelError('invalid_provider_response')
          return name
        })
        if (models.length > 10000) throw new ModelError('invalid_provider_response')
        this.assertProtocol(call.registration, signal)
        this.emit({
          event: 'model.completed',
          modelId: '',
          providerId: provider.id,
          role: 'models',
          durationMs: Date.now() - started,
        })
        return [...new Set(models)].sort()
      } catch (error) {
        this.emit({
          event: 'model.failed',
          modelId: '',
          providerId: provider.id,
          role: 'models',
          durationMs: Date.now() - started,
          code: error instanceof ModelError ? error.code : 'provider_request_failed',
        })
        throw error instanceof ModelError
          ? error
          : new ModelError('provider_request_failed', undefined, false, { cause: error })
      }
    }
    const result = await this.request(
      provider,
      'models',
      undefined,
      providerPolicy(provider).timeoutMs,
      signal,
    )
    return [...new Set(array(result.data).map((item) => string(object(item).id)))].sort()
  }

  private withRoleBudget<T>(
    role: ModelRole,
    signal: AbortSignal | undefined,
    task: (signal: AbortSignal | undefined) => Promise<T>,
  ): Promise<T> {
    return withRoleBudget(
      this.config.routes.find((route) => route.role === role)?.timeoutMs,
      signal,
      task,
      (reason, stageBudgetMs) =>
        preserveErrorContext(
          new ModelError('model_stage_timeout', undefined, false, { cause: reason }),
          {
            ...(reason && typeof reason === 'object' ? errorContext(reason) : {}),
            role,
            stageBudgetMs,
          },
        ),
    )
  }

  private async withProviderAttempts<T>(
    provider: Provider,
    details: { role: string; modelId?: string; apiModel?: string },
    signal: AbortSignal | undefined,
    task: () => Promise<T>,
  ): Promise<T> {
    const policy = providerPolicy(provider)
    let waiting: ReturnType<typeof startFlow> | undefined
    return runProviderAttempts(
      provider,
      signal,
      (attempt, timeoutMs) =>
        observeModel(
          this.flowWriter,
          'provider_attempt',
          {
            ...details,
            providerId: provider.id,
            counts: { attempt, maxAttempts: policy.maxAttempts, timeoutMs },
            nextStep: 'call_model_provider',
          },
          signal,
          task,
          () => ({ reason: 'provider_attempt_succeeded', nextStep: 'model_response_ready' }),
        ),
      {
        classify: (error) => ({
          retryable: error instanceof ModelError && error.retryable,
          ...(error instanceof ModelError && error.retryAfterMs !== undefined
            ? { retryAfterMs: error.retryAfterMs }
            : {}),
        }),
        onRetry: ({ attempt, maxAttempts, timeoutMs, delayMs }) => {
          const span = startFlow(this.flowWriter ?? currentFlowWriter(), 'provider_retry_wait', {
            ...details,
            providerId: provider.id,
            counts: { attempt, maxAttempts, timeoutMs, retryDelayMs: delayMs },
            nextStep: 'retry_same_provider',
          })
          span.bindSignal(signal)
          waiting = span
        },
        sleep: async (delayMs, waitSignal) => {
          try {
            await waitRetry(delayMs, waitSignal)
            waiting?.finish('completed', {
              reason: 'provider_retry_ready',
              nextStep: 'retry_same_provider',
            })
          } catch (error) {
            waiting?.finish('cancelled', { reason: 'request_cancelled', nextStep: 'stop' }, error)
            throw error
          } finally {
            waiting = undefined
          }
        },
      },
    )
  }

  private assertProtocol(registration: ProtocolRegistration, signal?: AbortSignal) {
    if (registration.signal.aborted) throw new ModelError('protocol_disabled')
    if (signal?.aborted) throw abortError()
  }

  private async invokeProtocol<T>(
    provider: Provider,
    model: ModelInstance | undefined,
    operation: 'complete' | 'embeddings' | 'listModels',
    timeoutMs: number,
    priority: number,
    parent: AbortSignal | undefined,
    task: (context: ModelProtocolContext, registration: ProtocolRegistration) => Promise<T>,
  ): Promise<{ value: T; registration: ProtocolRegistration; secrets: string[] }> {
    const registration = this.protocols?.lookup(provider.protocol)
    if (!registration) throw new ModelError('protocol_not_registered')
    if (!registration.adapter[operation]) throw new ModelError('protocol_operation_unsupported')
    const timeout = AbortSignal.timeout(Math.max(1, Math.min(timeoutMs, 600000)))
    const signal = AbortSignal.any([registration.signal, timeout, ...(parent ? [parent] : [])])
    const secrets: string[] = []
    const failure = () =>
      registration.signal.aborted
        ? new ModelError('protocol_disabled')
        : parent?.aborted
          ? abortError()
          : timeout.aborted
            ? new ModelError('provider_timeout', undefined, true)
            : new ModelError('provider_request_failed')
    let onAbort: () => void = () => {}
    try {
      if (signal.aborted) throw failure()
      const cancelled = new Promise<never>((_, reject) => {
        onAbort = () => reject(failure())
        signal.addEventListener('abort', onAbort, { once: true })
      })
      const pending = scheduleModel(priority, signal, async () => {
        if (signal.aborted) throw failure()
        let base: URL
        try {
          base = new URL(provider.baseUrl)
        } catch {
          throw new ModelError('invalid_provider_url')
        }
        if (
          !['http:', 'https:'].includes(base.protocol) ||
          base.username ||
          base.password ||
          base.search ||
          base.hash
        )
          throw new ModelError('invalid_provider_url')
        const credential = this.resolveCredential(provider)
        if ((provider.credentialEnv || provider.credentialConfigured) && !credential)
          throw new ModelError('credential_not_configured')
        if (credential) secrets.push(credential)
        // The task may ignore cancellation. Race it inside the scheduler so it cannot retain a queue slot.
        const execution = Promise.resolve().then(() => {
          if (signal.aborted) throw failure()
          return task(
            {
              provider: structuredClone(provider),
              ...(model ? { model: structuredClone(model) } : {}),
              credential,
              signal,
            },
            registration,
          )
        })
        return Promise.race([execution, cancelled])
      })
      const value = await Promise.race([pending, cancelled])
      if (signal.aborted) throw failure()
      return { value, registration, secrets }
    } catch (error) {
      if (signal.aborted) throw failure()
      // Only the explicit protocol error contract grants retry eligibility.
      if (error instanceof ModelProtocolError) {
        const status = error.status
        throw new ModelError(
          status
            ? `provider_http_${status}`
            : error.kind === 'configuration'
              ? 'protocol_configuration_error'
              : error.kind === 'timeout'
                ? 'provider_timeout'
                : 'provider_request_failed',
          status,
          error.kind !== 'configuration' && (status ? status === 429 || status >= 500 : true),
          { cause: error, retryAfterMs: error.retryAfterMs },
        )
      }
      throw new ModelError('provider_request_failed', undefined, isTransientNetworkFailure(error), {
        cause: error,
      })
    } finally {
      signal.removeEventListener('abort', onAbort)
    }
  }

  private normalizedUsage(value: unknown): Usage {
    const usage = object(value)
    if (
      (usage.known !== undefined && typeof usage.known !== 'boolean') ||
      (usage.cacheKnown !== undefined && typeof usage.cacheKnown !== 'boolean')
    )
      throw new ModelError('invalid_provider_usage')
    for (const key of ['inputTokens', 'outputTokens', 'totalTokens'])
      if (!validTokenCount(usage[key])) throw new ModelError('invalid_provider_usage')
    const result: Usage = {
      inputTokens: usage.inputTokens as number,
      outputTokens: usage.outputTokens as number,
      totalTokens: usage.totalTokens as number,
      ...(usage.known === false ? { known: false } : {}),
      ...(usage.cacheKnown !== undefined ? { cacheKnown: usage.cacheKnown as boolean } : {}),
    }
    if (
      !validTokenCount(result.inputTokens + result.outputTokens) ||
      result.inputTokens + result.outputTokens !== result.totalTokens
    )
      throw new ModelError('invalid_provider_usage')
    for (const key of cacheUsageKeys) {
      if (usage[key] === undefined) continue
      if (!validTokenCount(usage[key])) throw new ModelError('invalid_provider_usage')
      result[key] = usage[key] as number
    }
    const read = result.cacheReadInputTokens,
      write = result.cacheWriteInputTokens
    const ttlTotal = [
      result.cacheWrite5mInputTokens,
      result.cacheWrite1hInputTokens,
      result.cacheWrite30mInputTokens,
      result.cacheWrite12hInputTokens,
    ].reduce<number>((sum, count) => sum + (count ?? 0), 0)
    if (
      !validTokenCount(ttlTotal) ||
      (write !== undefined && ttlTotal > write) ||
      (read ?? 0) + (write ?? 0) > result.inputTokens
    )
      throw new ModelError('invalid_provider_usage')
    if (result.cacheKnown === true) {
      if (read === undefined || write === undefined) throw new ModelError('invalid_provider_usage')
      result.ordinaryInputTokens ??= result.inputTokens - read - write
    }
    if (
      result.ordinaryInputTokens !== undefined &&
      read !== undefined &&
      write !== undefined &&
      result.ordinaryInputTokens + read + write !== result.inputTokens
    )
      throw new ModelError('invalid_provider_usage')
    if (usage.rawTokenCounters !== undefined) {
      const raw = object(usage.rawTokenCounters)
      if (Object.keys(raw).length > rawCounterPaths.length)
        throw new ModelError('invalid_provider_usage')
      result.rawTokenCounters = {}
      for (const [key, count] of Object.entries(raw)) {
        if (
          !rawCounterPaths.includes(key as (typeof rawCounterPaths)[number]) ||
          !validTokenCount(count)
        )
          throw new ModelError('invalid_provider_usage')
        result.rawTokenCounters[key] = count
      }
    }
    return result
  }

  private async completeProtocol(
    provider: Provider,
    model: ModelInstance,
    role: Exclude<ModelRole, 'embedding'>,
    messages: ChatMessage[],
    tools: ToolDefinition[],
    timeout: number,
    signal?: AbortSignal,
  ): Promise<Completion> {
    const started = Date.now()
    const last = messages.findLast((message) => message.role === 'assistant')
    const previous = last ? this.continuations.get(last) : undefined
    const call = await this.invokeProtocol(
      provider,
      model,
      'complete',
      timeout,
      role === 'planner' || role === 'replyer' ? 0 : 1,
      signal,
      (context, registration) =>
        registration.adapter.complete(
          context,
          structuredClone(
            messages.map((message) => ({
              role: message.role,
              content: message.content,
              ...(message.images ? { images: message.images } : {}),
              ...(message.toolCalls ? { toolCalls: message.toolCalls } : {}),
              ...(message.toolCallId ? { toolCallId: message.toolCallId } : {}),
            })),
          ),
          structuredClone(tools),
          previous?.modelId === model.id && previous.generation === registration.generation
            ? previous.value
            : undefined,
        ),
    )
    const value = object(call.value)
    // Only visible normalized output crosses this boundary. Opaque continuation is never serialized.
    let encoded: string
    try {
      encoded = JSON.stringify({ text: value.text, toolCalls: value.toolCalls, usage: value.usage })
    } catch {
      throw new ModelError('invalid_provider_response')
    }
    if (Buffer.byteLength(encoded) > 8 * 1024 * 1024)
      throw new ModelError('provider_response_too_large')
    const visible = object(JSON.parse(encoded)),
      text = string(visible.text)
    const names = new Map(tools.map((tool) => [tool.name, tool.name]))
    const toolCalls = array(visible.toolCalls).map((item) => {
      const call = object(item)
      return this.toolCall(call.id, call.name, JSON.stringify(call.arguments), names)
    })
    if (new Set(toolCalls.map((call) => call.id)).size !== toolCalls.length)
      throw new ModelError('duplicate_tool_call_id')
    const usage = this.normalizedUsage(visible.usage)
    const continuation = value.continuation
    this.assertProtocol(call.registration, signal)
    const assistant: ChatMessage = { role: 'assistant', content: text, toolCalls }
    this.continuations.set(assistant, {
      modelId: model.id,
      generation: call.registration.generation,
      value: continuation,
    })
    try {
      this.diagnostic(
        { role, modelId: model.id, messages, output: { text, toolCalls } },
        call.secrets,
      )
    } catch {
      /* Diagnostics are optional. */
    }
    this.emit({
      event: 'model.completed',
      modelId: model.id,
      providerId: provider.id,
      role,
      durationMs: Date.now() - started,
      usage,
    })
    return { text, toolCalls, usage, modelId: model.id, assistant }
  }

  private async embeddingsProtocol(
    provider: Provider,
    model: ModelInstance,
    inputs: string[],
    timeout: number,
    signal?: AbortSignal,
  ) {
    const started = Date.now()
    const call = await this.invokeProtocol(
      provider,
      model,
      'embeddings',
      timeout,
      1,
      signal,
      (context, registration) => registration.adapter.embeddings!(context, [...inputs]),
    )
    let encoded: string
    try {
      const raw = object(call.value)
      encoded = JSON.stringify({ vectors: raw.vectors, usage: raw.usage })
    } catch {
      throw new ModelError('invalid_embedding_response')
    }
    if (Buffer.byteLength(encoded) > 8 * 1024 * 1024)
      throw new ModelError('provider_response_too_large')
    const value = object(JSON.parse(encoded))
    const vectors = array(value.vectors).map((vector) =>
      array(vector).map((n) => {
        if (typeof n !== 'number' || !Number.isFinite(n))
          throw new ModelError('invalid_embedding_response')
        return n
      }),
    )
    if (
      vectors.length !== inputs.length ||
      vectors.some((vector) => !vector.length || vector.length !== vectors[0]?.length)
    )
      throw new ModelError('invalid_embedding_dimensions')
    const usage = this.normalizedUsage(value.usage)
    this.assertProtocol(call.registration, signal)
    this.emit({
      event: 'model.completed',
      modelId: model.id,
      providerId: provider.id,
      role: 'embedding',
      durationMs: Date.now() - started,
      usage,
    })
    return { vectors, usage, modelId: model.id }
  }

  private validateEmbeddingVectors(model: ModelInstance, vectors: number[][]) {
    if (
      vectors.some(
        (vector) =>
          vector.length > 16384 ||
          vector.some((n) => !Number.isFinite(Math.fround(n))) ||
          vector.every((n) => Math.fround(n) === 0),
      )
    )
      throw new ModelError('invalid_embedding_vector')
    if (
      model.embeddingDimensions !== undefined &&
      vectors.some((vector) => vector.length !== model.embeddingDimensions)
    )
      throw new ModelError('embedding_dimensions_mismatch')
  }

  private emit(entry: ModelLog) {
    const model = this.config.models.find((model) => model.id === entry.modelId)
    const pricing: ModelPriceSnapshot | undefined = model
      ? { apiModelName: model.apiModelName }
      : undefined
    if (pricing && model)
      for (const key of [
        'inputUsdPerMillion',
        'outputUsdPerMillion',
        'cacheReadUsdPerMillion',
        'cacheWriteUsdPerMillion',
        'cacheWrite5mUsdPerMillion',
        'cacheWrite1hUsdPerMillion',
        'cacheWrite30mUsdPerMillion',
        'cacheWrite12hUsdPerMillion',
      ] as const)
        if (model[key] !== undefined) pricing[key] = model[key]
    try {
      this.log({ ...entry, ...(pricing ? { pricing } : {}) })
    } catch {
      /* Observability must not repeat a successful model operation. */
    }
  }

  private chatInput(message: ChatMessage): JsonObject {
    if (message.role === 'tool') {
      if (!message.toolCallId) throw new ModelError('missing_tool_call_id')
      return { role: 'tool', content: message.content, tool_call_id: message.toolCallId }
    }
    return {
      role: message.role,
      content: message.images?.length
        ? [
            { type: 'text', text: message.content },
            ...message.images.map((url) => ({ type: 'image_url', image_url: { url } })),
          ]
        : message.content,
      ...(message.toolCalls?.length
        ? {
            tool_calls: message.toolCalls.map((call) => ({
              id: call.id,
              type: 'function',
              function: { name: wireName(call.name), arguments: JSON.stringify(call.arguments) },
            })),
          }
        : {}),
    }
  }

  private responsesInput(messages: ChatMessage[], modelId: string): JsonObject[] {
    return messages.flatMap((message) => {
      if (message.role === 'tool') {
        if (!message.toolCallId) throw new ModelError('missing_tool_call_id')
        return [
          { type: 'function_call_output', call_id: message.toolCallId, output: message.content },
        ]
      }
      const previous = this.state.get(message)
      if (previous?.modelId === modelId) return previous.output.map(object)
      const result: JsonObject[] = []
      if (message.content || message.images?.length)
        result.push({
          role: message.role,
          content: message.images?.length
            ? [
                { type: 'input_text', text: message.content },
                ...message.images.map((image_url) => ({
                  type: 'input_image',
                  image_url,
                  detail: 'auto',
                })),
              ]
            : message.content,
        })
      for (const call of message.toolCalls ?? [])
        result.push({
          type: 'function_call',
          call_id: call.id,
          name: wireName(call.name),
          arguments: JSON.stringify(call.arguments),
        })
      return result
    })
  }

  private toolCall(
    id: unknown,
    name: unknown,
    args: unknown,
    names: Map<string, string>,
  ): ToolCall {
    const toolName = names.get(string(name))
    if (!toolName || !string(id)) throw new ModelError('unknown_tool_call')
    let parsed: unknown
    try {
      parsed = JSON.parse(string(args))
    } catch {
      throw new ModelError('invalid_tool_arguments')
    }
    return { id: string(id), name: toolName, arguments: object(parsed) }
  }

  private parseChat(result: JsonObject, names: Map<string, string>) {
    const choice = object(array(result.choices)[0])
    if (choice.finish_reason !== 'stop' && choice.finish_reason !== 'tool_calls')
      throw new ModelError('incomplete_model_response')
    const message = object(choice.message)
    const toolCalls =
      message.tool_calls === undefined
        ? []
        : array(message.tool_calls).map((item) => {
            const call = object(item)
            if (call.type !== 'function') throw new ModelError('unsupported_tool_call')
            const fn = object(call.function)
            return this.toolCall(call.id, fn.name, fn.arguments, names)
          })
    if (new Set(toolCalls.map((call) => call.id)).size !== toolCalls.length)
      throw new ModelError('duplicate_tool_call_id')
    return {
      text: message.content == null ? '' : string(message.content),
      toolCalls,
      usage: usageOf(result.usage),
    }
  }

  private parseResponses(result: JsonObject, names: Map<string, string>) {
    if (result.status !== 'completed' || result.error || result.incomplete_details)
      throw new ModelError('incomplete_model_response')
    const toolCalls: ToolCall[] = []
    const text: string[] = []
    for (const item of array(result.output).map(object)) {
      if (item.type === 'function_call') {
        if (item.status && item.status !== 'completed') throw new ModelError('incomplete_tool_call')
        toolCalls.push(this.toolCall(item.call_id, item.name, item.arguments, names))
      } else if (item.type === 'message') {
        if (item.status && item.status !== 'completed')
          throw new ModelError('incomplete_model_response')
        for (const part of array(item.content).map(object))
          if (part.type === 'output_text') text.push(string(part.text))
      } else if (item.type !== 'reasoning') throw new ModelError('unsupported_response_item')
    }
    if (new Set(toolCalls.map((call) => call.id)).size !== toolCalls.length)
      throw new ModelError('duplicate_tool_call_id')
    return { text: text.join('\n'), toolCalls, usage: usageOf(result.usage) }
  }

  private async request(
    provider: Provider,
    path: string,
    body: unknown,
    timeoutMs: number,
    parentSignal?: AbortSignal,
    credentialUsed?: (secret: string) => void,
  ): Promise<JsonObject> {
    if (parentSignal?.aborted) throw abortError()
    let base: URL
    try {
      base = new URL(provider.baseUrl)
    } catch {
      throw new ModelError('invalid_provider_url')
    }
    if (
      !['http:', 'https:'].includes(base.protocol) ||
      base.username ||
      base.password ||
      base.search ||
      base.hash
    )
      throw new ModelError('invalid_provider_url')
    const credential = this.resolveCredential(provider)
    if ((provider.credentialEnv || provider.credentialConfigured) && !credential)
      throw new ModelError('credential_not_configured')
    if (credential) credentialUsed?.(credential)
    const timeoutSignal = AbortSignal.timeout(Math.max(1, Math.min(timeoutMs, 600_000)))
    const signal = parentSignal ? AbortSignal.any([parentSignal, timeoutSignal]) : timeoutSignal
    try {
      const response = await fetch(`${base.href.replace(/\/$/, '')}/${path}`, {
        method: body === undefined ? 'GET' : 'POST',
        headers: {
          Accept: 'application/json',
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
          ...(credential ? { Authorization: `Bearer ${credential}` } : {}),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        signal,
        redirect: 'error',
      })
      if (!response.ok) {
        await response.body?.cancel()
        throw new ModelError(
          `provider_http_${response.status}`,
          response.status,
          response.status === 429 || response.status >= 500,
          { retryAfterMs: parseRetryAfter(response.headers.get('retry-after')) },
        )
      }
      if (!response.headers.get('content-type')?.includes('application/json')) {
        await response.body?.cancel()
        throw new ModelError('invalid_provider_content_type')
      }
      const reader = response.body?.getReader()
      if (!reader) throw new ModelError('empty_provider_response')
      let size = 0
      const chunks: Uint8Array[] = []
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        size += value.byteLength
        if (size > 8 * 1024 * 1024) {
          await reader.cancel()
          throw new ModelError('provider_response_too_large')
        }
        chunks.push(value)
      }
      if (signal.aborted) throw abortError()
      try {
        return object(JSON.parse(Buffer.concat(chunks).toString('utf8')))
      } catch (error) {
        throw new ModelError('invalid_provider_json', undefined, false, { cause: error })
      }
    } catch (error) {
      if (parentSignal?.aborted) throw abortError(error)
      if (timeoutSignal.aborted)
        throw preserveErrorContext(
          new ModelError('provider_timeout', undefined, true, { cause: error }),
          { timeoutMs },
        )
      if (error instanceof ModelError) throw error
      throw new ModelError('provider_request_failed', undefined, isTransientNetworkFailure(error), {
        cause: error,
      })
    }
  }
}
