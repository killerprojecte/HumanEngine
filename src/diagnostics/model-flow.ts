import { preserveErrorContext, bindAbortErrorContext } from './error-context.ts'
import { modelScheduler } from '../scheduler.ts'
import type { Completion, Usage } from '../models.ts'
import { startFlow, currentFlowWriter, type FlowWriter, type FlowDetails } from './flow.ts'

const safeCodes = new Set([
  'request_cancelled',
  'protocol_disabled',
  'route_not_configured',
  'invalid_tool_names',
  'all_models_failed',
  'no_compatible_model',
  'embedding_model_not_configured',
  'invalid_embedding_dimensions_setting',
  'invalid_embedding_batch_size',
  'invalid_embedding_response',
  'invalid_embedding_dimensions',
  'invalid_embedding_vector',
  'embedding_dimensions_mismatch',
  'provider_not_configured',
  'provider_timeout',
  'provider_request_failed',
  'invalid_provider_url',
  'credential_not_configured',
  'invalid_provider_response',
  'invalid_provider_content_type',
  'empty_provider_response',
  'provider_response_too_large',
  'invalid_provider_json',
  'protocol_not_registered',
  'protocol_operation_unsupported',
  'work_queue_full',
  'invalid_provider_usage',
  'duplicate_tool_call_id',
  'incomplete_model_response',
  'incomplete_tool_call',
  'unsupported_response_item',
  'invalid_tool_arguments',
  'unknown_tool_call',
])
export function modelFailureReason(error: unknown): string {
  if (!error || typeof error !== 'object') return 'provider_request_failed'
  try {
    const code = Object.getOwnPropertyDescriptor(error, 'code')?.value
    if (typeof code === 'string' && (safeCodes.has(code) || /^provider_http_[1-5]\d\d$/.test(code)))
      return code
    const message = Object.getOwnPropertyDescriptor(error, 'message')?.value
    return message === 'work_queue_full' || message === 'request_cancelled'
      ? message
      : 'provider_request_failed'
  } catch {
    return 'provider_request_failed'
  }
}

export function modelUsageCounts(usage: Usage): Record<string, number> {
  return usage.known === false
    ? { usageKnown: 0 }
    : {
        usageKnown: 1,
        inputTokens: usage.inputTokens,
        outputTokens: usage.outputTokens,
        totalTokens: usage.totalTokens,
      }
}
export function completionDetails(value: Completion): FlowDetails {
  return {
    outputKind: value.toolCalls.length
      ? value.text
        ? 'text_and_tools'
        : 'tool_calls'
      : value.text
        ? 'text'
        : 'empty',
    counts: {
      ...modelUsageCounts(value.usage),
      textCharacters: value.text.length,
      toolCalls: value.toolCalls.length,
    },
    nextStep: 'model_response_ready',
  }
}
export async function observeModel<T>(
  writer: FlowWriter | undefined,
  stage: string,
  details: FlowDetails,
  signal: AbortSignal | undefined,
  task: () => Promise<T>,
  summary: (value: T) => FlowDetails,
): Promise<T> {
  const span = startFlow(writer ?? currentFlowWriter(), stage, details)
  const releaseContext =
    stage === 'model_attempt'
      ? bindAbortErrorContext(signal, {
          operation: 'model.request',
          stage,
          role: details.role,
          modelId: details.modelId,
          providerId: details.providerId,
          apiModel: details.apiModel,
          attempt: details.counts?.attempt,
          timeoutMs: details.counts?.timeoutMs,
          remainingCandidates: details.counts?.remainingCandidates,
        })
      : () => {}
  span.bindSignal(signal)
  return span.run(async () => {
    try {
      const value = await task()
      if (signal?.aborted) {
        span.finish('cancelled', { reason: 'request_cancelled', nextStep: 'stop' })
        return value
      }
      span.finish('completed', summary(value))
      return value
    } catch (error) {
      preserveErrorContext(error, {
        operation: 'model.request',
        stage,
        role: details.role,
        modelId: details.modelId,
        providerId: details.providerId,
        apiModel: details.apiModel,
        attempt: details.counts?.attempt,
        timeoutMs: details.counts?.timeoutMs,
        remainingCandidates: details.counts?.remainingCandidates,
      })
      const reason = modelFailureReason(error)
      span.finish(
        signal?.aborted || reason === 'request_cancelled' || reason === 'protocol_disabled'
          ? 'cancelled'
          : 'failed',
        {
          reason,
          nextStep:
            signal?.aborted || reason === 'request_cancelled' || reason === 'protocol_disabled'
              ? 'stop'
              : stage === 'model_attempt' && (details.counts?.remainingCandidates ?? 0) > 0
                ? 'select_next_model'
                : 'caller_handles_failure',
        },
        error,
      )
      throw error
    } finally {
      releaseContext()
    }
  })
}
/** Bind the originating span when the shared scheduler later starts this task. */
export async function scheduleModel<T>(
  priority: number,
  signal: AbortSignal | undefined,
  task: () => Promise<T>,
): Promise<T> {
  const span = startFlow(currentFlowWriter(), 'model_queue', {
    counts: { queued: modelScheduler.status.queued, running: modelScheduler.status.running },
    nextStep: 'wait_for_model_slot',
  })
  span.bindSignal(signal)
  let entered = false
  try {
    return await modelScheduler.run(priority, signal, () =>
      span.run(() => {
        entered = true
        span.finish('completed', {
          reason: 'model_slot_available',
          nextStep: 'call_model_provider',
        })
        return task()
      }),
    )
  } catch (error) {
    if (!entered)
      span.finish(
        signal?.aborted ? 'cancelled' : 'failed',
        {
          reason: modelFailureReason(error),
          nextStep: signal?.aborted ? 'stop' : 'caller_handles_failure',
        },
        error,
      )
    throw error
  }
}
