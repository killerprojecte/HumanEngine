/**
 * Bounded SSE transport. Never expose provider payloads in errors.
 * Protocol references:
 * https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events
 * https://developers.openai.com/api/reference/resources/responses/streaming-events
 */
type Json = Record<string, unknown>
export type StreamProgressKind = 'text' | 'reasoning' | 'tool'
type Progress = (kind: StreamProgressKind) => void
type StreamFailureCode =
  | 'invalid_provider_stream'
  | 'incomplete_model_response'
  | 'provider_response_too_large'
  | 'provider_stream_error'
  | 'provider_stream_invalid_json'
  | 'provider_stream_protocol_mismatch'
  | 'provider_stream_event_mismatch'
  | 'provider_stream_upstream_error'
  | 'provider_stream_transport_error'
  | 'provider_stream_invalid_utf8'
export class StreamResponseError extends Error {
  constructor(
    message: StreamFailureCode,
    readonly code: StreamFailureCode = message,
  ) {
    super(message)
  }
}
function fail(message: StreamFailureCode = 'invalid_provider_stream', code = message): never {
  throw new StreamResponseError(message, code)
}
const obj = (value: unknown): Json =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as Json) : fail()
const idx = (value: unknown): number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value < 65536
    ? value
    : fail()
const str = (value: unknown): string => (typeof value === 'string' ? value : fail())
const list = (value: unknown): unknown[] => (Array.isArray(value) ? value : fail())
const append = (target: Json, key: string, value: unknown, progress: () => void) => {
  const text = str(value)
  target[key] = (target[key] == null ? '' : str(target[key])) + text
  if (text.trim()) progress()
}

// Completed snapshots must contain everything observed so far. Status may advance;
// opaque fields (including encrypted reasoning) must never silently disappear.
function contains(final: unknown, partial: unknown): boolean {
  if (partial === null) return true // Some added snapshots use null until completion.
  if (typeof partial === 'string') return typeof final === 'string' && final.startsWith(partial)
  if (Array.isArray(partial))
    return Array.isArray(final) && partial.every((part, i) => contains(final[i], part))
  if (partial !== null && typeof partial === 'object') {
    if (final === null || typeof final !== 'object' || Array.isArray(final)) return false
    return Object.entries(partial).every(
      ([key, value]) =>
        key === 'status' ||
        (['id', 'call_id', 'type', 'name', 'role'].includes(key)
          ? (final as Json)[key] === value
          : contains((final as Json)[key], value)),
    )
  }
  return partial === final
}

class ChatStream {
  result: Json = {}
  choices = new Map<number, Json>()
  tools = new Map<number, Map<number, Json>>()
  constructor(private progress: Progress) {}
  event(data: Json) {
    if (data.error) fail('provider_stream_error', 'provider_stream_upstream_error')
    if (
      !Array.isArray(data.choices) &&
      typeof data.type === 'string' &&
      data.type.startsWith('response.')
    )
      fail('invalid_provider_stream', 'provider_stream_protocol_mismatch')
    for (const key of ['id', 'model', 'created', 'system_fingerprint', 'service_tier']) {
      if (data[key] !== undefined) {
        if (key === 'id' && this.result.id !== undefined && this.result.id !== data.id) fail()
        this.result[key] = data[key]
      }
    }
    if (data.usage != null) this.result.usage = obj(data.usage)
    for (const raw of list(data.choices)) {
      const chunk = obj(raw),
        index = idx(chunk.index)
      let choice = this.choices.get(index)
      if (!choice) {
        choice = { index, message: { role: 'assistant' }, finish_reason: null }
        this.choices.set(index, choice)
      }
      if (choice.finish_reason !== null) fail()
      const delta = obj(chunk.delta),
        message = obj(choice.message)
      if (delta.role !== undefined && delta.role !== 'assistant') fail()
      for (const key of ['content', 'reasoning_content', 'reasoning', 'refusal']) {
        if (delta[key] != null)
          append(message, key, delta[key], () =>
            this.progress(key.startsWith('reasoning') ? 'reasoning' : 'text'),
          )
        else if (key === 'content' && delta.content === null && message.content === undefined)
          message.content = null
      }
      if (delta.tool_calls !== undefined) {
        let calls = this.tools.get(index)
        if (!calls) this.tools.set(index, (calls = new Map()))
        for (const rawCall of list(delta.tool_calls)) {
          const part = obj(rawCall),
            callIndex = idx(part.index)
          let call = calls.get(callIndex)
          if (!call) calls.set(callIndex, (call = { function: {} }))
          for (const key of ['id', 'type']) {
            if (part[key] !== undefined) {
              if (call[key] !== undefined && call[key] !== part[key]) fail()
              call[key] = str(part[key])
            }
          }
          if (part.function !== undefined) {
            const fn = obj(part.function),
              target = obj(call.function)
            if (fn.name !== undefined) append(target, 'name', fn.name, () => {})
            if (fn.arguments !== undefined)
              append(target, 'arguments', fn.arguments, () => this.progress('tool'))
          }
        }
      }
      if (chunk.finish_reason != null) choice.finish_reason = str(chunk.finish_reason)
    }
  }
  finish(): Json {
    if (!this.choices.size || !this.choices.has(0)) fail('incomplete_model_response')
    const choices = [...this.choices.entries()]
      .sort(([a], [b]) => a - b)
      .map(([index, choice]) => {
        if (!['stop', 'tool_calls'].includes(String(choice.finish_reason)))
          fail('incomplete_model_response')
        const calls = this.tools.get(index)
        if (calls) {
          const sorted = [...calls.entries()].sort(([a], [b]) => a - b)
          sorted.forEach(([i, call], position) => {
            if (i !== position || !call.id || call.type !== 'function' || !obj(call.function).name)
              fail()
            validateArguments(obj(call.function).arguments)
          })
          obj(choice.message).tool_calls = sorted.map(([, call]) => call)
        }
        if (choice.finish_reason === 'tool_calls' && !calls?.size) fail('incomplete_model_response')
        return choice
      })
    return { ...this.result, object: 'chat.completion', choices }
  }
}
function validateArguments(value: unknown) {
  try {
    obj(JSON.parse(str(value)))
  } catch {
    fail('incomplete_model_response')
  }
}

class ResponsesStream {
  // Snapshot and delta events share lengths so repeated done events cannot keep
  // the idle watchdog alive. Opaque encrypted reasoning is never counted as text.
  private observedLengths = new Map<string, number>()
  private observe(index: number, item: Json) {
    const record = (path: string, value: unknown, kind: StreamProgressKind) => {
      if (typeof value !== 'string' || !value.length) return
      const key = `${index}:${path}`
      const previousLength = this.observedLengths.get(key) ?? 0
      if (value.length <= previousLength) return
      this.observedLengths.set(key, value.length)
      if (value.slice(previousLength).trim()) this.progress(kind)
    }
    if (item.type === 'function_call') record('arguments', item.arguments, 'tool')
    if (item.type !== 'message' && item.type !== 'reasoning') return
    for (const key of ['content', 'summary']) {
      if (!Array.isArray(item[key])) continue
      item[key].forEach((raw, partIndex) => {
        if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) return
        const part = raw as Json
        const kind = item.type === 'reasoning' ? 'reasoning' : 'text'
        record(`${key}:${partIndex}:text`, part.text, kind)
        record(`${key}:${partIndex}:refusal`, part.refusal, kind)
      })
    }
  }
  items = new Map<number, Json>()
  responseId: unknown
  constructor(private progress: Progress) {}
  event(data: Json): Json | undefined {
    if (data.type === undefined && Array.isArray(data.choices))
      fail('invalid_provider_stream', 'provider_stream_protocol_mismatch')
    const type = str(data.type)
    if (['error', 'response.failed', 'response.incomplete'].includes(type))
      fail('provider_stream_error', 'provider_stream_upstream_error')
    if (
      type === 'response.created' ||
      type === 'response.in_progress' ||
      type === 'response.completed'
    ) {
      const response = obj(data.response)
      if (this.responseId !== undefined && this.responseId !== response.id) fail()
      this.responseId = response.id
      if (type !== 'response.completed') return
      if (response.status !== 'completed' || response.error || response.incomplete_details)
        fail('incomplete_model_response')
      const output = list(response.output)
      for (const [index, partial] of this.items) if (!contains(output[index], partial)) fail()
      for (const raw of output) {
        const item = obj(raw)
        if (item.type === 'function_call') validateArguments(item.arguments)
        if (item.status !== undefined && item.status !== 'completed')
          fail('incomplete_model_response')
      }
      output.forEach((item, index) => this.observe(index, obj(item)))
      return response
    }
    if (type === 'response.output_item.added' || type === 'response.output_item.done') {
      const index = idx(data.output_index),
        item = obj(data.item),
        previous = this.items.get(index)
      if (previous && (type.endsWith('.added') || !contains(item, previous))) fail()
      this.items.set(index, item)
      this.observe(index, item)
      return
    }
    const deltaFields: Record<string, [string, string, string | undefined]> = {
      'response.output_text.delta': ['content', 'text', 'content_index'],
      'response.refusal.delta': ['content', 'refusal', 'content_index'],
      'response.reasoning_summary_text.delta': ['summary', 'text', 'summary_index'],
      'response.reasoning_text.delta': ['content', 'text', 'content_index'],
      'response.function_call_arguments.delta': ['', 'arguments', undefined],
    }
    const doneField = type.endsWith('.done')
      ? deltaFields[type.replace(/\.done$/, '.delta')]
      : undefined
    const field = deltaFields[type] || doneField
    const partAdded =
      type === 'response.content_part.added' || type === 'response.reasoning_summary_part.added'
    const partDone =
      type === 'response.content_part.done' || type === 'response.reasoning_summary_part.done'
    if (!field && !partAdded && !partDone) return // Forward-compatible metadata events do not count as output.
    const outputIndex = idx(data.output_index)
    const item = this.items.get(outputIndex)
    if (!item || (data.item_id !== undefined && item.id !== data.item_id)) fail()
    if (partAdded || partDone) {
      const key = type.includes('summary') ? 'summary' : 'content'
      const indexKey = key === 'summary' ? 'summary_index' : 'content_index'
      const parts = item[key] === undefined ? [] : list(item[key])
      const index = idx(data[indexKey])
      if (partDone) {
        if (!contains(data.part, parts[index])) fail()
        parts[index] = obj(data.part)
      } else {
        if (index !== parts.length) fail()
        parts.push(obj(data.part))
      }
      item[key] = parts
    } else if (field) {
      const [key, property, indexKey] = field
      const target = indexKey ? obj(list(item[key])[idx(data[indexKey])]) : item
      if (doneField) {
        const value = data[property]
        if (!contains(value, target[property])) fail()
        target[property] = str(value)
        this.observe(outputIndex, item)
        return
      }
      append(target, property, data.delta, () => {})
    }
    this.observe(outputIndex, item)
  }
}

export async function readStreamingResponse(
  response: Response,
  protocol: 'openai-chat' | 'openai-responses',
  onMeaningful: Progress,
): Promise<Record<string, unknown>> {
  if (!response.body) fail('incomplete_model_response')
  let reader: ReadableStreamDefaultReader<Uint8Array>
  try {
    reader = response.body.getReader()
  } catch {
    return fail()
  }
  const decoder = new TextDecoder('utf-8', { fatal: true })
  const decode = (bytes?: Uint8Array, stream = false) => {
    try {
      return decoder.decode(bytes, { stream })
    } catch {
      return fail('provider_stream_error', 'provider_stream_invalid_utf8')
    }
  }
  const chat = new ChatStream(onMeaningful),
    responses = new ResponsesStream(onMeaningful)
  let bytes = 0,
    line = '',
    data: string[] = [],
    eventName = '',
    skipLF = false
  let result: Json | undefined
  const dispatch = () => {
    if (!data.length) {
      eventName = ''
      return
    }
    const payload = data.join('\n')
    data = []
    const event = eventName
    eventName = ''
    if (event === 'error') fail('provider_stream_error', 'provider_stream_upstream_error')
    if (payload === '[DONE]') {
      if (protocol !== 'openai-chat') fail('incomplete_model_response')
      result = chat.finish()
      return
    }
    let raw: unknown
    try {
      raw = JSON.parse(payload)
    } catch {
      fail('invalid_provider_stream', 'provider_stream_invalid_json')
    }
    const parsed = obj(raw)
    if (protocol === 'openai-chat') chat.event(parsed!)
    else {
      if (event && event !== parsed!.type)
        fail('invalid_provider_stream', 'provider_stream_event_mismatch')
      result = responses.event(parsed!)
    }
  }
  const consumeLine = () => {
    const current = line
    line = ''
    if (!current) {
      dispatch()
      return
    }
    if (current.startsWith(':')) return
    const colon = current.indexOf(':')
    const field = colon < 0 ? current : current.slice(0, colon)
    let value = colon < 0 ? '' : current.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'data') data.push(value)
    else if (field === 'event') eventName = value
  }
  try {
    while (!result) {
      const chunk = await reader.read()
      if (chunk.done) {
        decode()
        fail('incomplete_model_response')
      }
      bytes += chunk.value.byteLength
      if (bytes > 8 * 1024 * 1024) fail('provider_response_too_large')
      const text = decode(chunk.value, true)
      for (const character of text) {
        if (skipLF && character === '\n') {
          skipLF = false
          continue
        }
        skipLF = false
        if (character === '\r' || character === '\n') {
          skipLF = character === '\r'
          consumeLine()
          if (result) break
        } else line += character
      }
    }
    return result
  } catch (error) {
    if (error instanceof StreamResponseError) throw error
    // Transport/decoder/callback exceptions can contain provider secrets.
    return fail('provider_stream_error', 'provider_stream_transport_error')
  } finally {
    try {
      await reader.cancel()
    } catch {
      /* Preserve the original safe error. */
    }
    reader.releaseLock()
  }
}
