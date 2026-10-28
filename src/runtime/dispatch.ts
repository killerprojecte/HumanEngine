import { randomUUID } from 'node:crypto'
import { startFlow, type FlowWriter } from '../diagnostics/flow.ts'
import type { createTypingDelay } from '../typing-delay.ts'
import type { RuntimeAdapter, TurnContext, ReplyGrounding } from '../runtime.ts'
import type { SendSegment, SendResult, SceneRef } from '../onebot.ts'

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
    validate(request: unknown, signal: AbortSignal): Promise<boolean>
  },
  typingDelay: ReturnType<typeof createTypingDelay> | undefined,
  index: number,
  total: number,
): Promise<SendResult> {
  const dispatch = startFlow(services.store, 'send_segment', {
    counts: { segmentIndex: index + 1, segmentCount: total, parts: segment.parts.length },
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
      const typing = startFlow(services.store, 'typing_delay', { nextStep: 'send_validation' })
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
          return (receipt = { status: 'cancelled', detail: 'typing_delay_cancelled' })
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
      const validation = startFlow(services.store, 'send_validation', { nextStep: 'adapter_send' })
      validation.bindSignal(context.signal)
      let valid = false
      try {
        valid = await validation.run(() =>
          guard.validate({ operationId, segments: [segment] }, context.signal),
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
      const adapter = services.adapter(context.scene.adapterId)
      return (receipt = adapter
        ? await adapter.send(context.scene as SceneRef, segment, context.signal)
        : { status: 'failed', detail: 'adapter_unavailable' })
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
          nextStep: receipt?.status === 'sent' && index + 1 < total ? 'send_segment' : 'thinker',
        },
        failure,
      )
    }
  })
}
