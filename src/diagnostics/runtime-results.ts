import type { FlowDetails, FlowStatus } from './flow.ts'

const own = (value: unknown, key: string): unknown =>
  value && typeof value === 'object'
    ? Object.getOwnPropertyDescriptor(value, key)?.value
    : undefined

/** Summarize typed outcome metadata, never arbitrary tool content or arguments. */
export function toolOutcome(output: unknown): {
  status: Exclude<FlowStatus, 'started' | 'cancel_requested'>
  details: FlowDetails
} {
  const items = own(output, 'items'),
    results = own(output, 'results')
  const counts: Record<string, number> = {
    results: Array.isArray(output)
      ? output.length
      : Array.isArray(items)
        ? items.length
        : Array.isArray(results)
          ? results.length
          : 0,
  }
  if (
    Array.isArray(output) &&
    output.length &&
    output.every((item) => {
      const status = own(item, 'status')
      return (
        typeof status === 'string' && ['sent', 'failed', 'unknown', 'cancelled'].includes(status)
      )
    })
  ) {
    for (const state of ['sent', 'failed', 'unknown', 'cancelled'])
      counts[`${state}Segments`] = output.filter((item) => own(item, 'status') === state).length
    if (counts.unknownSegments || counts.failedSegments)
      return {
        status: counts.sentSegments ? 'partial' : 'failed',
        details: {
          counts,
          reason: counts.unknownSegments ? 'delivery_unknown' : 'delivery_failed',
        },
      }
    if (counts.cancelledSegments)
      return {
        status: counts.sentSegments ? 'partial' : 'cancelled',
        details: { counts, reason: 'send_cancelled' },
      }
    return { status: 'completed', details: { counts, reason: 'segments_delivered' } }
  }
  const status = own(output, 'status')
  if (status === 'cancelled')
    return { status: 'cancelled', details: { counts, reason: 'context_cancelled' } }
  if (
    status === 'unavailable' ||
    status === 'failed' ||
    own(output, 'error') !== undefined ||
    own(output, 'ok') === false
  )
    return { status: 'failed', details: { counts, reason: 'tool_rejected' } }
  return {
    status: 'completed',
    details: { counts, reason: status === 'no_results' ? 'query_no_results' : 'tool_returned' },
  }
}
