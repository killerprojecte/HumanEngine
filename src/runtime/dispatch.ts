import { outcomeError, preserveOutcomeError } from '../diagnostics/error-context.ts'
import { knownDiagnosticCode } from '../diagnostics/error-codes.ts'
import { randomUUID } from 'node:crypto'
import { startFlow, type FlowWriter } from '../diagnostics/flow.ts'
import type { createTypingDelay } from '../typing-delay.ts'
import type { RuntimeAdapter, TurnContext, ReplyGrounding } from '../runtime.ts'
import type { SendSegment, SendResult, SceneRef } from '../onebot.ts'

/** Observe cancellation even if an extension ignores its signal; consume late settlement. */
export function awaitSendStage<T>(action: () => Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    let invoking = true,
      abortQueued = false,
      settled = false
    const cleanup = () => signal.removeEventListener('abort', abort)
    const abort = () => {
      if (invoking || abortQueued || settled) return
      abortQueued = true
      queueMicrotask(() => {
        if (settled) return
        settled = true
        cleanup()
        reject(signal.reason ?? new Error('request_cancelled'))
      })
    }
    if (signal.aborted) return reject(signal.reason ?? new Error('request_cancelled'))
    signal.addEventListener('abort', abort, { once: true })
    let pending: Promise<T>
    try {
      pending = Promise.resolve(action())
    } catch (error) {
      settled = true
      cleanup()
      reject(error)
      return
    } finally {
      invoking = false
    }
    // Observe a receipt already settled by the invocation before its synchronous abort.
    // A still-pending promise cannot win by settling after an awaited disable/cleanup.
    pending.then(
      (value) => {
        if (!settled) {
          settled = true
          cleanup()
          resolve(value)
        }
      },
      (error) => {
        if (!settled) {
          settled = true
          cleanup()
          reject(error)
        }
      },
    )
    if (signal.aborted) abort()
  })
}

const abortContainedAdapters = new WeakSet<RuntimeAdapter>()
/** Core installation only: the wrapper owns cancellation across its complete send scope. */
export function abortContainedAdapter(adapter: RuntimeAdapter): RuntimeAdapter {
  abortContainedAdapters.add(adapter)
  return adapter
}

/** Explicit runtime ports preserve source validation and authoritative adapter receipts. */
export interface DispatchServices {
  store: FlowWriter
  eligible(context: TurnContext): boolean
  isRecalled(sceneId: string, messageId: string): boolean
  adapter(id: string): RuntimeAdapter | undefined
}

export async function dispatchSegment(
  services: DispatchServices,
  context: TurnContext,
  segment: SendSegment,
  operationId: string,
  guard: {
    grounding: ReplyGrounding
    captureTransmitGuard?: () => () => boolean
    validate(request: unknown, signal: AbortSignal): Promise<boolean>
  },
  typingDelay: ReturnType<typeof createTypingDelay> | undefined,
  index: number,
  total: number,
): Promise<SendResult> {
  const dispatch = startFlow(services.store, 'send_segment', {
    counts: {
      segmentIndex: index + 1,
      segmentCount: total,
      parts: segment.parts.length,
    },
    nextStep: 'typing_delay',
  })
  dispatch.bindSignal(context.signal)
  let receipt: SendResult | undefined, failure: unknown
  return dispatch.run(async () => {
    try {
      if (
        !services.eligible(context) ||
        (segment.replyToId && services.isRecalled(context.scene.id, segment.replyToId))
      )
        return (receipt = { status: 'cancelled', detail: 'send_invalidated' })
      const typing = startFlow(services.store, 'typing_delay', {
        nextStep: 'send_validation',
      })
      typing.bindSignal(context.signal)
      if (typingDelay) {
        try {
          await typing.run(() => typingDelay.wait(segment, context.signal))
          typing.finish('completed', {
            reason: 'typing_wait_finished',
            nextStep: 'send_validation',
          })
        } catch (error) {
          typing.finish('cancelled', { reason: 'typing_delay_cancelled', nextStep: 'none' }, error)
          return (receipt = {
            status: 'cancelled',
            detail: 'typing_delay_cancelled',
          })
        }
      } else
        typing.finish('skipped', {
          reason: context.mode === 'simulation' ? 'simulation_mode' : 'typing_delay_disabled',
          nextStep: 'send_validation',
        })
      if (
        !services.eligible(context) ||
        (segment.replyToId && services.isRecalled(context.scene.id, segment.replyToId)) ||
        guard.grounding.check([segment])
      )
        return (receipt = { status: 'cancelled', detail: 'send_invalidated' })
      const validation = startFlow(services.store, 'send_validation', {
        nextStep: 'adapter_send',
      })
      validation.bindSignal(context.signal)
      let valid = false
      try {
        valid = await validation.run(() =>
          awaitSendStage(
            () => guard.validate({ operationId, segments: [segment] }, context.signal),
            context.signal,
          ),
        )
        validation.finish(valid ? 'completed' : 'cancelled', {
          reason: valid ? 'send_validated' : 'send_invalidated',
          nextStep: valid ? 'adapter_send' : 'none',
        })
      } catch (error) {
        validation.finish(
          context.signal.aborted ? 'cancelled' : 'failed',
          { reason: 'send_validation_failed', nextStep: 'none' },
          error,
        )
        // Validation runs before physical dispatch: cancellation proves this segment
        // was not sent, even when the validator aborts before its false result settles.
        if (context.signal.aborted)
          return (receipt = preserveOutcomeError(
            { status: 'cancelled' as const, detail: 'send_invalidated' },
            error,
            { operation: 'send.validate', stage: 'send_validation' },
          ))
        throw error
      }
      if (
        !valid ||
        !services.eligible(context) ||
        (segment.replyToId && services.isRecalled(context.scene.id, segment.replyToId))
      )
        return (receipt = { status: 'cancelled', detail: 'send_invalidated' })
      if (context.mode === 'simulation')
        return (receipt = { status: 'sent', messageId: randomUUID() })
      const transmitGuard = guard.captureTransmitGuard?.()
      const sending = startFlow(services.store, 'adapter_send', {
        nextStep: 'replyer',
      })
      sending.bindSignal(context.signal)
      let invoked = false
      try {
        const adapter = services.adapter(context.scene.adapterId)
        receipt = adapter
          ? await sending.run(() => {
              const send = () => {
                invoked = true
                return adapter.send(context.scene as SceneRef, segment, context.signal, () => {
                  if (
                    !services.eligible(context) ||
                    services.adapter(context.scene.adapterId) !== adapter ||
                    (segment.replyToId &&
                      services.isRecalled(context.scene.id, segment.replyToId)) ||
                    (transmitGuard && !transmitGuard()) ||
                    guard.grounding.check([segment])
                  )
                    throw new Error('send_invalidated')
                })
              }
              return abortContainedAdapters.has(adapter)
                ? send()
                : awaitSendStage(send, context.signal)
            })
          : preserveOutcomeError(
              { status: 'failed' as const, detail: 'adapter_unavailable' },
              new Error('adapter_unavailable'),
              { operation: 'adapter.send', stage: 'adapter_send' },
            )
      } catch (error) {
        // Once invoked, neither cancellation nor an exception proves non-delivery.
        receipt = preserveOutcomeError(
          {
            status: invoked ? ('unknown' as const) : ('cancelled' as const),
            detail: context.signal.aborted
              ? context.signal.reason?.name === 'TimeoutError'
                ? 'request_timeout'
                : 'request_cancelled'
              : 'delivery_unknown',
          },
          error,
          { operation: 'adapter.send', stage: 'adapter_send' },
        )
      } finally {
        sending.finish(
          receipt?.status === 'sent'
            ? 'completed'
            : receipt?.status === 'unknown'
              ? 'partial'
              : receipt?.status === 'cancelled'
                ? 'cancelled'
                : 'failed',
          {
            receiptState: receipt?.status ?? 'failed',
            reason:
              knownDiagnosticCode(receipt?.detail) ??
              (receipt?.status === 'sent'
                ? 'segment_delivered'
                : receipt?.status === 'unknown'
                  ? 'delivery_unknown'
                  : receipt?.status === 'cancelled'
                    ? 'send_invalidated'
                    : 'delivery_failed'),
            nextStep: receipt?.status === 'sent' && index + 1 < total ? 'send_segment' : 'replyer',
          },
          receipt ? outcomeError(receipt) : undefined,
        )
      }
      return receipt!
    } catch (error) {
      failure = error
      throw error
    } finally {
      dispatch.finish(
        failure
          ? context.signal.aborted
            ? 'cancelled'
            : 'failed'
          : receipt?.status === 'sent'
            ? 'completed'
            : receipt?.status === 'unknown'
              ? 'partial'
              : receipt?.status === 'cancelled'
                ? 'cancelled'
                : 'failed',
        {
          receiptState: receipt?.status ?? 'failed',
          reason: failure
            ? 'segment_send_failed'
            : receipt?.status === 'sent'
              ? context.mode === 'simulation'
                ? 'simulated_delivery'
                : 'segment_delivered'
              : receipt?.status === 'unknown'
                ? 'delivery_unknown'
                : receipt?.status === 'cancelled'
                  ? 'send_invalidated'
                  : 'delivery_failed',
          counts: { segmentIndex: index + 1, segmentCount: total },
          nextStep: receipt?.status === 'sent' && index + 1 < total ? 'send_segment' : 'replyer',
        },
        failure ?? (receipt ? outcomeError(receipt) : undefined),
      )
    }
  })
}
