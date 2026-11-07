// Generated from src/model-protocols.ts; SHA-256 95e45d005e786fcd2ef957354ffe7608cb5d827bc7e16cca5344a7905b193222
/** Explicit retry-safe failure contract; response bodies and URLs are not accepted. */
export class ModelProtocolError extends Error {
  kind
  status
  retryAfterMs
  constructor(kind, options = {}) {
    super('model_protocol_failure', { cause: options.cause })
    this.kind = kind
    if (!['network', 'timeout', 'http', 'configuration'].includes(kind))
      throw new Error('invalid_protocol_failure_kind')
    if (
      kind === 'http' &&
      (!Number.isInteger(options.status) || options.status < 400 || options.status > 599)
    )
      throw new Error('invalid_protocol_failure_status')
    if (kind === 'http') this.status = options.status
    if (typeof options.retryAfterMs === 'number' && Number.isFinite(options.retryAfterMs))
      this.retryAfterMs = Math.max(0, Math.min(10000, options.retryAfterMs))
  }
}
const builtinMetadata = [
  {
    id: 'openai-chat',
    label: 'OpenAI Chat Completions',
    operations: ['complete', 'embeddings', 'listModels'],
  },
  {
    id: 'openai-responses',
    label: 'OpenAI Responses',
    operations: ['complete', 'embeddings', 'listModels'],
  },
]
export const isBuiltinProtocol = (id) => id === 'openai-chat' || id === 'openai-responses'
/** One registry per engine. Trusted code is not a sandbox; lifecycle guards still fail closed. */
export class ModelProtocolRegistry {
  entries = new Map()
  generation = 0
  register(id, label, adapter, signal) {
    if (!/^[a-z0-9-]+:[a-z0-9-]+$/.test(id) || id.length > 160 || this.entries.has(id))
      throw new Error('invalid_or_duplicate_protocol_id')
    if (
      !label ||
      label.length > 100 ||
      typeof adapter.complete !== 'function' ||
      (adapter.embeddings !== undefined && typeof adapter.embeddings !== 'function') ||
      (adapter.listModels !== undefined && typeof adapter.listModels !== 'function')
    )
      throw new Error('invalid_protocol_definition')
    if (signal.aborted) throw new Error('protocol_disabled')
    const controller = new AbortController()
    const entry = Object.freeze({
      label,
      generation: ++this.generation,
      signal: controller.signal,
      adapter: Object.freeze({
        complete: adapter.complete.bind(adapter),
        ...(adapter.embeddings ? { embeddings: adapter.embeddings.bind(adapter) } : {}),
        ...(adapter.listModels ? { listModels: adapter.listModels.bind(adapter) } : {}),
      }),
    })
    const dispose = () => {
      if (this.entries.get(id) === entry) this.entries.delete(id)
      signal.removeEventListener('abort', dispose)
      controller.abort('protocol_disabled')
    }
    this.entries.set(id, entry)
    signal.addEventListener('abort', dispose, { once: true })
    return dispose
  }
  lookup(id) {
    return this.entries.get(id)
  }
  list() {
    return [
      ...builtinMetadata.map((row) => ({ ...row, operations: [...row.operations] })),
      ...[...this.entries].map(([id, entry]) => ({
        id,
        label: entry.label,
        operations: [
          'complete',
          ...(entry.adapter.embeddings ? ['embeddings'] : []),
          ...(entry.adapter.listModels ? ['listModels'] : []),
        ],
      })),
    ]
  }
}
