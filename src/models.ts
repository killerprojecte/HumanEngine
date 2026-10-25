export type ModelRole = 'planner' | 'thinker' | 'learning' | 'vision' | 'embedding'
export interface Provider {
  id: string
  name: string
  protocol: 'openai-chat' | 'openai-responses'
  baseUrl: string
  credentialEnv: string
}
export interface ModelInstance {
  id: string
  displayName: string
  providerId: string
  apiModelName: string
  capabilities: Array<'text' | 'tools' | 'vision' | 'embedding'>
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
export interface ToolCall { id: string; name: string; arguments: Record<string, unknown> }
export interface ChatMessage {
  role: 'system' | 'user' | 'assistant' | 'tool'
  content: string
  images?: string[]
  toolCalls?: ToolCall[]
  toolCallId?: string
}
export interface Usage { inputTokens: number; outputTokens: number; totalTokens: number }
export interface Completion {
  text: string
  toolCalls: ToolCall[]
  usage: Usage
  modelId: string
  assistant: ChatMessage
}
export interface RouterConfig { providers: Provider[]; models: ModelInstance[]; routes: RoleRoute[] }
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
  constructor(public readonly code: string, public readonly status?: number) {
    super(code)
    this.name = 'ModelError'
  }
}
type JsonObject = Record<string, unknown>
const object = (value: unknown): JsonObject => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new ModelError('invalid_provider_response')
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
const tokenCount = (value: unknown): number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
const usageOf = (value: unknown): Usage => {
  const usage = value && typeof value === 'object' ? value as JsonObject : {}
  const inputTokens = tokenCount(usage.input_tokens ?? usage.prompt_tokens)
  const outputTokens = tokenCount(usage.output_tokens ?? usage.completion_tokens)
  return { inputTokens, outputTokens, totalTokens: tokenCount(usage.total_tokens) || inputTokens + outputTokens }
}
const wireName = (name: string) => name.replaceAll('.', '__')
const abortError = () => new ModelError('request_cancelled')

/** Protocol state is transient, opaque, and never serialized into logs or persisted messages. */
export class Router {
  private readonly state = new WeakMap<ChatMessage, { modelId: string; output: unknown[] }>()
  private readonly config: RouterConfig
  constructor(config: RouterConfig, private readonly log: (entry: ModelLog) => void = () => {}) {
    this.config = structuredClone(config)
  }

  async complete(role: Exclude<ModelRole, 'embedding'>, messages: ChatMessage[], tools: ToolDefinition[] = [], signal?: AbortSignal): Promise<Completion> {
    if (signal?.aborted) throw abortError()
    const route = this.config.routes.find(item => item.role === role)
    if (!route) throw new ModelError('route_not_configured')
    const names = new Map(tools.map(tool => [wireName(tool.name), tool.name]))
    if (names.size !== tools.length || [...names.keys()].some(name => !/^[a-zA-Z0-9_-]{1,64}$/.test(name))) throw new ModelError('invalid_tool_names')
    let attempted = false
    for (const modelId of route.orderedModelIds) {
      if (signal?.aborted) throw abortError()
      const model = this.config.models.find(item => item.id === modelId)
      if (!model || !model.capabilities.includes('text') || (tools.length && !model.capabilities.includes('tools')) || (messages.some(item => item.images?.length) && !model.capabilities.includes('vision'))) continue
      const provider = this.config.providers.find(item => item.id === model.providerId)
      if (!provider) continue
      attempted = true
      const started = Date.now()
      try {
        const responses = provider.protocol === 'openai-responses'
        const body = responses
          ? { model: model.apiModelName, input: this.responsesInput(messages, modelId), store: false, stream: false, include: ['reasoning.encrypted_content'], ...(tools.length ? { tools: tools.map(tool => ({ type: 'function', name: wireName(tool.name), description: tool.description, parameters: tool.inputSchema, strict: false })) } : {}) }
          : { model: model.apiModelName, messages: messages.map(message => this.chatInput(message)), stream: false, ...(tools.length ? { tools: tools.map(tool => ({ type: 'function', function: { name: wireName(tool.name), description: tool.description, parameters: tool.inputSchema, strict: false } })) } : {}) }
        const result = await this.request(provider, responses ? 'responses' : 'chat/completions', body, route.timeoutMs, signal)
        if (signal?.aborted) throw abortError()
        const parsed = responses ? this.parseResponses(result, names) : this.parseChat(result, names)
        const assistant: ChatMessage = { role: 'assistant', content: parsed.text, toolCalls: parsed.toolCalls }
        if (responses) this.state.set(assistant, { modelId, output: array(result.output) })
        const completion = { ...parsed, modelId, assistant }
        this.emit({ event: 'model.completed', modelId, providerId: provider.id, role, durationMs: Date.now() - started, usage: completion.usage })
        return completion
      } catch (error) {
        if (signal?.aborted) throw abortError()
        this.emit({ event: 'model.failed', modelId, providerId: provider.id, role, durationMs: Date.now() - started, code: error instanceof ModelError ? error.code : 'provider_request_failed' })
      }
    }
    throw new ModelError(attempted ? 'all_models_failed' : 'no_compatible_model')
  }

  async embeddings(modelId: string, inputs: string[], signal?: AbortSignal): Promise<{ vectors: number[][]; usage: Usage; modelId: string }> {
    const model = this.config.models.find(item => item.id === modelId)
    if (!model?.capabilities.includes('embedding')) throw new ModelError('embedding_model_not_configured')
    const provider = this.config.providers.find(item => item.id === model.providerId)
    if (!provider) throw new ModelError('provider_not_configured')
    const timeout = this.config.routes.find(route => route.role === 'embedding')?.timeoutMs ?? 30_000
    const result = await this.request(provider, 'embeddings', { model: model.apiModelName, input: inputs, encoding_format: 'float' }, timeout, signal)
    const rows = array(result.data).map(object).sort((a, b) => Number(a.index) - Number(b.index))
    if (rows.length !== inputs.length || rows.some((row, index) => row.index !== index)) throw new ModelError('invalid_embedding_response')
    const vectors = rows.map(row => array(row.embedding).map(value => {
      if (typeof value !== 'number' || !Number.isFinite(value)) throw new ModelError('invalid_embedding_response')
      return value
    }))
    if (vectors.some(vector => !vector.length || vector.length !== vectors[0]?.length)) throw new ModelError('invalid_embedding_dimensions')
    return { vectors, usage: usageOf(result.usage), modelId }
  }

  async listModels(providerId: string, signal?: AbortSignal): Promise<string[]> {
    const provider = this.config.providers.find(item => item.id === providerId)
    if (!provider) throw new ModelError('provider_not_configured')
    const result = await this.request(provider, 'models', undefined, 15_000, signal)
    return [...new Set(array(result.data).map(item => string(object(item).id)))].sort()
  }

  private emit(entry: ModelLog) {
    try { this.log(entry) } catch { /* Observability must not repeat a successful model operation. */ }
  }

  private chatInput(message: ChatMessage): JsonObject {
    if (message.role === 'tool') {
      if (!message.toolCallId) throw new ModelError('missing_tool_call_id')
      return { role: 'tool', content: message.content, tool_call_id: message.toolCallId }
    }
    return {
      role: message.role,
      content: message.images?.length ? [{ type: 'text', text: message.content }, ...message.images.map(url => ({ type: 'image_url', image_url: { url } }))] : message.content,
      ...(message.toolCalls?.length ? { tool_calls: message.toolCalls.map(call => ({ id: call.id, type: 'function', function: { name: wireName(call.name), arguments: JSON.stringify(call.arguments) } })) } : {})
    }
  }

  private responsesInput(messages: ChatMessage[], modelId: string): JsonObject[] {
    return messages.flatMap(message => {
      if (message.role === 'tool') {
        if (!message.toolCallId) throw new ModelError('missing_tool_call_id')
        return [{ type: 'function_call_output', call_id: message.toolCallId, output: message.content }]
      }
      const previous = this.state.get(message)
      if (previous?.modelId === modelId) return previous.output.map(object)
      const result: JsonObject[] = []
      if (message.content || message.images?.length) result.push({ role: message.role, content: message.images?.length ? [{ type: 'input_text', text: message.content }, ...message.images.map(image_url => ({ type: 'input_image', image_url, detail: 'auto' }))] : message.content })
      for (const call of message.toolCalls ?? []) result.push({ type: 'function_call', call_id: call.id, name: wireName(call.name), arguments: JSON.stringify(call.arguments) })
      return result
    })
  }

  private toolCall(id: unknown, name: unknown, args: unknown, names: Map<string, string>): ToolCall {
    const toolName = names.get(string(name))
    if (!toolName || !string(id)) throw new ModelError('unknown_tool_call')
    let parsed: unknown
    try { parsed = JSON.parse(string(args)) } catch { throw new ModelError('invalid_tool_arguments') }
    return { id: string(id), name: toolName, arguments: object(parsed) }
  }

  private parseChat(result: JsonObject, names: Map<string, string>) {
    const choice = object(array(result.choices)[0])
    if (choice.finish_reason !== 'stop' && choice.finish_reason !== 'tool_calls') throw new ModelError('incomplete_model_response')
    const message = object(choice.message)
    const toolCalls = message.tool_calls === undefined ? [] : array(message.tool_calls).map(item => {
      const call = object(item)
      if (call.type !== 'function') throw new ModelError('unsupported_tool_call')
      const fn = object(call.function)
      return this.toolCall(call.id, fn.name, fn.arguments, names)
    })
    if (new Set(toolCalls.map(call => call.id)).size !== toolCalls.length) throw new ModelError('duplicate_tool_call_id')
    return { text: message.content == null ? '' : string(message.content), toolCalls, usage: usageOf(result.usage) }
  }

  private parseResponses(result: JsonObject, names: Map<string, string>) {
    if (result.status !== 'completed' || result.error || result.incomplete_details) throw new ModelError('incomplete_model_response')
    const toolCalls: ToolCall[] = []
    const text: string[] = []
    for (const item of array(result.output).map(object)) {
      if (item.type === 'function_call') {
        if (item.status && item.status !== 'completed') throw new ModelError('incomplete_tool_call')
        toolCalls.push(this.toolCall(item.call_id, item.name, item.arguments, names))
      } else if (item.type === 'message') {
        if (item.status && item.status !== 'completed') throw new ModelError('incomplete_model_response')
        for (const part of array(item.content).map(object)) if (part.type === 'output_text') text.push(string(part.text))
      } else if (item.type !== 'reasoning') throw new ModelError('unsupported_response_item')
    }
    if (new Set(toolCalls.map(call => call.id)).size !== toolCalls.length) throw new ModelError('duplicate_tool_call_id')
    return { text: text.join('\n'), toolCalls, usage: usageOf(result.usage) }
  }

  private async request(provider: Provider, path: string, body: unknown, timeoutMs: number, parentSignal?: AbortSignal): Promise<JsonObject> {
    if (parentSignal?.aborted) throw abortError()
    let base: URL
    try { base = new URL(provider.baseUrl) } catch { throw new ModelError('invalid_provider_url') }
    if (!['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) throw new ModelError('invalid_provider_url')
    const credential = provider.credentialEnv ? process.env[provider.credentialEnv] : undefined
    if (provider.credentialEnv && !credential) throw new ModelError('credential_not_configured')
    const timeoutSignal = AbortSignal.timeout(Math.max(1, Math.min(timeoutMs, 600_000)))
    const signal = parentSignal ? AbortSignal.any([parentSignal, timeoutSignal]) : timeoutSignal
    try {
      const response = await fetch(`${base.href.replace(/\/$/, '')}/${path}`, {
        method: body === undefined ? 'GET' : 'POST',
        headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...(credential ? { Authorization: `Bearer ${credential}` } : {}) },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal, redirect: 'error'
      })
      if (!response.ok) { await response.body?.cancel(); throw new ModelError(`provider_http_${response.status}`, response.status) }
      if (!response.headers.get('content-type')?.includes('application/json')) { await response.body?.cancel(); throw new ModelError('invalid_provider_content_type') }
      const reader = response.body?.getReader()
      if (!reader) throw new ModelError('empty_provider_response')
      let size = 0
      const chunks: Uint8Array[] = []
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        size += value.byteLength
        if (size > 8 * 1024 * 1024) { await reader.cancel(); throw new ModelError('provider_response_too_large') }
        chunks.push(value)
      }
      if (signal.aborted) throw abortError()
      try { return object(JSON.parse(Buffer.concat(chunks).toString('utf8'))) } catch { throw new ModelError('invalid_provider_json') }
    } catch (error) {
      if (parentSignal?.aborted) throw abortError()
      if (timeoutSignal.aborted) throw new ModelError('provider_timeout')
      if (error instanceof ModelError) throw error
      throw new ModelError('provider_request_failed')
    }
  }
}
