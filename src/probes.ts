import type { ModelProtocolRegistry } from './model-protocols.ts'
import { Router, ModelError, type ModelInstance } from './models.ts'
import type { Store } from './store.ts'

export type ProbeCapability = ModelInstance['capabilities'][number]
export interface ProbeResult {
  ok: boolean
  capability: ProbeCapability
  detail: string
  durationMs: number
}
// Fixed one-pixel PNG: no user images, identifiers, or conversation data leave the host.
const image =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAACXBIWXMAAAPoAAAD6AG1e1JrAAAADElEQVQImWP4//8/AAX+Av5Y8msOAAAAAElFTkSuQmCC'
const capabilities = new Set(['text', 'tools', 'vision', 'embedding'])

/** Explicit administrator-triggered paid request; never auto-enables capabilities. */
export async function probe(
  store: Store,
  modelId: string,
  capability: ProbeCapability,
  registry?: ModelProtocolRegistry,
): Promise<ProbeResult> {
  const started = Date.now()
  const result = (ok: boolean, detail: string): ProbeResult => ({
    ok,
    capability,
    detail,
    durationMs: Date.now() - started,
  })
  if (!capabilities.has(capability)) return result(false, 'invalid_capability')
  const config = store.models
  const model = config.models.find((m) => m.id === modelId)
  if (!model) return result(false, 'model_not_found')
  const provider = config.providers.find((p) => p.id === model.providerId)
  if (!provider) return result(false, 'provider_not_configured')
  const role =
    capability === 'vision' ? 'vision' : capability === 'embedding' ? 'embedding' : 'thinker'
  // Advertised capability flags are hypotheses being tested, not permission to fallback.
  const router = new Router(
    {
      providers: [provider],
      models: [
        {
          ...model,
          capabilities: [...new Set<ModelInstance['capabilities'][number]>(['text', capability])],
        },
      ],
      routes: [{ role, orderedModelIds: [model.id], timeoutMs: 30000 }],
    },
    (entry) => store.recordModel(entry, config),
    undefined,
    registry,
    store.resolveCredential,
  )
  const signal = AbortSignal.timeout(30000)
  try {
    if (capability === 'embedding') {
      const response = await router.embeddings(model.id, ['HumanEngine capability probe.'], signal)
      if (
        response.vectors.length !== 1 ||
        !response.vectors[0]?.length ||
        response.vectors[0].some((n) => !Number.isFinite(n)) ||
        response.vectors[0].every((n) => n === 0)
      )
        return result(false, 'embedding_validation_failed')
      return result(true, 'embedding_vector_validated')
    }
    const completion = await router.complete(
      capability === 'vision' ? 'vision' : 'thinker',
      [
        {
          role: 'user',
          content:
            capability === 'tools'
              ? 'Call probe.echo exactly once with value "humanengine-probe". Do not answer with text.'
              : capability === 'vision'
                ? 'This is a protocol image-input acceptance test. Acknowledge this request briefly.'
                : 'This is a text protocol test. Reply briefly with OK.',
          ...(capability === 'vision' ? { images: [image] } : {}),
        },
      ],
      capability === 'tools'
        ? [
            {
              name: 'probe.echo',
              description:
                'Return the supplied fixed probe value. This has no external side effects.',
              inputSchema: {
                type: 'object',
                properties: { value: { type: 'string', enum: ['humanengine-probe'] } },
                required: ['value'],
                additionalProperties: false,
              },
            },
          ]
        : [],
      signal,
    )
    if (capability === 'tools') {
      const call = completion.toolCalls[0]
      const valid =
        completion.toolCalls.length === 1 &&
        call?.name === 'probe.echo' &&
        Object.keys(call.arguments).length === 1 &&
        call.arguments.value === 'humanengine-probe'
      return result(valid, valid ? 'tool_call_validated' : 'tool_call_validation_failed')
    }
    if (!completion.text.trim() || completion.toolCalls.length)
      return result(false, 'text_response_missing')
    return result(
      true,
      capability === 'vision'
        ? 'vision_input_accepted_accuracy_unverified'
        : 'text_response_received',
    )
  } catch (error) {
    // Preserve the probe API diagnostic; dimension mismatch remains a distinct error.
    if (
      capability === 'embedding' &&
      error instanceof ModelError &&
      error.code === 'invalid_embedding_vector'
    )
      return result(false, 'embedding_validation_failed')
    // Router codes are fixed diagnostics; never relay provider text, credentials, or URLs.
    return result(false, error instanceof ModelError ? error.code : 'probe_failed')
  }
}
