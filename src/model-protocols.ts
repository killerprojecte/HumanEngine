import type {
  ChatMessage,
  ModelInstance,
  Provider,
  ToolCall,
  ToolDefinition,
  Usage,
} from './models.ts'

export interface ModelProtocolContext {
  provider: Provider
  model?: ModelInstance
  /** Server-only request credential. Never include this value in public metadata or errors. */
  credential?: string
  signal: AbortSignal
}
export interface ModelProtocol {
  complete(
    context: ModelProtocolContext,
    messages: ChatMessage[],
    tools: ToolDefinition[],
    continuation?: unknown,
  ): Promise<{ text: string; toolCalls: ToolCall[]; usage: Usage; continuation?: unknown }>
  embeddings?(
    context: ModelProtocolContext,
    inputs: string[],
  ): Promise<{ vectors: number[][]; usage: Usage }>
  listModels?(context: ModelProtocolContext): Promise<string[]>
}
export interface ProtocolMetadata {
  id: string
  label: string
  operations: Array<'complete' | 'embeddings' | 'listModels'>
}
export interface ProtocolRegistration {
  readonly adapter: ModelProtocol
  readonly signal: AbortSignal
  readonly generation: number
}
const builtinMetadata: ProtocolMetadata[] = [
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
export const isBuiltinProtocol = (id: string) => id === 'openai-chat' || id === 'openai-responses'

/** One registry per engine. Trusted code is not a sandbox; lifecycle guards still fail closed. */
export class ModelProtocolRegistry {
  private readonly entries = new Map<string, ProtocolRegistration & { label: string }>()
  private generation = 0
  register(id: string, label: string, adapter: ModelProtocol, signal: AbortSignal): () => void {
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
  lookup(id: string): ProtocolRegistration | undefined {
    return this.entries.get(id)
  }
  list(): ProtocolMetadata[] {
    return [
      ...builtinMetadata.map((row) => ({ ...row, operations: [...row.operations] })),
      ...[...this.entries].map(([id, entry]) => ({
        id,
        label: entry.label,
        operations: [
          'complete',
          ...(entry.adapter.embeddings ? ['embeddings'] : []),
          ...(entry.adapter.listModels ? ['listModels'] : []),
        ] as ProtocolMetadata['operations'],
      })),
    ]
  }
}
