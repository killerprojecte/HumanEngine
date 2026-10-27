import {
  ModelProtocolRegistry,
  isBuiltinProtocol,
  type ModelProtocolContext,
  type ProtocolRegistration,
} from './model-protocols.ts'
import { modelScheduler } from './scheduler.ts'
export type ModelRole = 'planner' | 'thinker' | 'learning' | 'vision' | 'embedding'
export interface Provider {
  id: string
  name: string
  protocol: string
  baseUrl: string
  credentialEnv: string
  credentialConfigured?: boolean
}
export interface ModelInstance {
  id: string
  displayName: string
  providerId: string
  apiModelName: string
  capabilities: Array<'text' | 'tools' | 'vision' | 'embedding'>
  inputUsdPerMillion?: number
  outputUsdPerMillion?: number
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
export interface ModelLog {
  event: 'model.completed' | 'model.failed'
  modelId: string
  providerId: string
  role: ModelRole | 'models'
  durationMs: number
  code?: string
  usage?: Usage
}
export class ModelError extends Error {
  constructor(
    public readonly code: string,
    public readonly status?: number,
    public readonly retryable = status !== undefined && [429, 502, 503, 504].includes(status),
  ) {
    super(code)
    this.name = 'ModelError'
  }
}
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
const tokenCount = (value: unknown): number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
const usageOf = (value: unknown, embedding = false): Usage => {
  const usage = value && typeof value === 'object' ? (value as JsonObject) : {}
  const inputTokens = tokenCount(usage.input_tokens ?? usage.prompt_tokens)
  const outputTokens = tokenCount(usage.output_tokens ?? usage.completion_tokens)
  const valid = (n: unknown) => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0
  const known =
    valid(usage.input_tokens ?? usage.prompt_tokens) &&
    (embedding || valid(usage.output_tokens ?? usage.completion_tokens))
  return {
    ...(!known ? { known: false } : {}),
    inputTokens,
    outputTokens,
    totalTokens: tokenCount(usage.total_tokens) || inputTokens + outputTokens,
  }
}
const wireName = (name: string) => name.replaceAll('.', '__')
const abortError = () => new ModelError('request_cancelled')

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
  ) {
    this.config = structuredClone(config)
  }

  async complete(
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
    for (const modelId of route.orderedModelIds) {
      if (signal?.aborted) throw abortError()
      const model = this.config.models.find((item) => item.id === modelId)
      if (
        !model ||
        !model.capabilities.includes('text') ||
        (tools.length && !model.capabilities.includes('tools')) ||
        (messages.some((item) => item.images?.length) && !model.capabilities.includes('vision'))
      )
        continue
      const provider = this.config.providers.find((item) => item.id === model.providerId)
      if (!provider) continue
      attempted = true
      const started = Date.now()
      try {
        if (!isBuiltinProtocol(provider.protocol))
          return await this.completeProtocol(
            provider,
            model,
            role,
            messages,
            tools,
            route.timeoutMs,
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
        const result = await modelScheduler.run(
          role === 'planner' || role === 'thinker' ? 0 : 1,
          signal,
          () =>
            this.request(
              provider,
              responses ? 'responses' : 'chat/completions',
              body,
              route.timeoutMs,
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
      } catch (error) {
        if (signal?.aborted) throw abortError()
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
    )
  }

  async embeddings(
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
      usage.inputTokens += batch.usage.inputTokens
      usage.outputTokens += batch.usage.outputTokens
      usage.totalTokens += batch.usage.totalTokens
      if (batch.usage.known === false) usage.known = false
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
    const timeout =
      this.config.routes.find((route) => route.role === 'embedding')?.timeoutMs ?? 30_000
    const started = Date.now()
    try {
      if (!isBuiltinProtocol(provider.protocol))
        return await this.embeddingsProtocol(provider, model, inputs, timeout, signal)
      const result = await modelScheduler.run(1, signal, () =>
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
      throw error instanceof ModelError ? error : new ModelError('provider_request_failed')
    }
  }

  async listModels(providerId: string, signal?: AbortSignal): Promise<string[]> {
    const provider = this.config.providers.find((item) => item.id === providerId)
    if (!provider) throw new ModelError('provider_not_configured')
    if (!isBuiltinProtocol(provider.protocol)) {
      const started = Date.now()
      try {
        const call = await this.invokeProtocol(
          provider,
          undefined,
          'listModels',
          15000,
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
        throw error instanceof ModelError ? error : new ModelError('provider_request_failed')
      }
    }
    const result = await this.request(provider, 'models', undefined, 15_000, signal)
    return [...new Set(array(result.data).map((item) => string(object(item).id)))].sort()
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
            ? new ModelError('provider_timeout')
            : new ModelError('provider_request_failed')
    let onAbort: () => void = () => {}
    try {
      if (signal.aborted) throw failure()
      const cancelled = new Promise<never>((_, reject) => {
        onAbort = () => reject(failure())
        signal.addEventListener('abort', onAbort, { once: true })
      })
      const pending = modelScheduler.run(priority, signal, async () => {
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
      // Never trust exception text or even a ModelError thrown by external plugin code.
      throw new ModelError('provider_request_failed')
    } finally {
      signal.removeEventListener('abort', onAbort)
    }
  }

  private normalizedUsage(value: unknown): Usage {
    const usage = object(value)
    if (usage.known !== undefined && typeof usage.known !== 'boolean')
      throw new ModelError('invalid_provider_usage')
    for (const key of ['inputTokens', 'outputTokens', 'totalTokens'])
      if (
        typeof usage[key] !== 'number' ||
        !Number.isSafeInteger(usage[key]) ||
        (usage[key] as number) < 0
      )
        throw new ModelError('invalid_provider_usage')
    return {
      inputTokens: usage.inputTokens as number,
      outputTokens: usage.outputTokens as number,
      totalTokens: usage.totalTokens as number,
      ...(usage.known === false ? { known: false } : {}),
    }
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
      role === 'planner' || role === 'thinker' ? 0 : 1,
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
    try {
      this.log(entry)
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
        throw new ModelError(`provider_http_${response.status}`, response.status)
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
      } catch {
        throw new ModelError('invalid_provider_json')
      }
    } catch (error) {
      if (parentSignal?.aborted) throw abortError()
      if (timeoutSignal.aborted) throw new ModelError('provider_timeout')
      if (error instanceof ModelError) throw error
      throw new ModelError('provider_request_failed')
    }
  }
}
