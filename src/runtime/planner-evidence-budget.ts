import { retrievalCoverage } from './evidence-scope.ts'
import { workspaceStateTools } from './workspace-state-evidence.ts'
import type { ChatMessage } from '../models.ts'
import type { PlanEvidence } from './reply-plan.ts'

export interface EvidenceCoverage {
  scope?: 'returned_tool_result'
  status: 'complete' | 'limited'
  returnedRecords: number
  omittedRecords: number
  reason?: 'planner_evidence_budget'
}
/** Counts serialized ChatMessage[] bytes, not tokens or provider tool-schema overhead. */
export class PlannerEvidenceBudget {
  constructor(readonly maxBytes = 131072) {
    if (!Number.isSafeInteger(maxBytes) || maxBytes < 1)
      throw new Error('planner_invalid_evidence_budget')
  }
  measure(messages: readonly ChatMessage[]): number {
    return Buffer.byteLength(JSON.stringify(messages), 'utf8')
  }
  assertWithin(messages: readonly ChatMessage[]): void {
    if (this.measure(messages) > this.maxBytes) throw new Error('planner_evidence_budget_exhausted')
  }
  receipt(
    messages: readonly ChatMessage[],
    receipt: PlanEvidence,
    toolCallId: string,
    serialize: (value: unknown) => string = JSON.stringify,
  ): { receipt: PlanEvidence; message: ChatMessage } {
    // Scope is server-derived, not accepted from result fields or model arguments.
    receipt = { ...receipt, retrievalCoverage: retrievalCoverage(receipt.toolName) }
    const envelope = (value: PlanEvidence): ChatMessage => ({
      role: 'tool',
      toolCallId,
      content: serialize(value),
    })
    const fits = (message: ChatMessage) => this.measure([...messages, message]) <= this.maxBytes
    if (
      ![
        ...workspaceStateTools,
        'memory.search',
        'memory.person',
        'memory.relations',
        'memory.commitments',
        'memory.commitmentHistory',
        'memory.history',
        'experience.outcomes',
      ].includes(receipt.toolName ?? '') ||
      !Array.isArray(receipt.value)
    ) {
      if (Buffer.byteLength(JSON.stringify(receipt.value) ?? '', 'utf8') > 65536)
        throw new Error('planner_evidence_too_large')
      const message = envelope(receipt)
      this.assertWithin([...messages, message])
      return { receipt, message }
    }
    const upstream = receipt.coverage
    const records = receipt.value
    const candidate = (count: number): PlanEvidence => ({
      ...receipt,
      value: records.slice(0, count),
      coverage: {
        scope: 'returned_tool_result',
        status: count === records.length && upstream?.status !== 'limited' ? 'complete' : 'limited',
        returnedRecords: count,
        omittedRecords: (upstream?.omittedRecords ?? 0) + records.length - count,
        ...(count < records.length || upstream?.reason === 'planner_evidence_budget'
          ? { reason: 'planner_evidence_budget' as const }
          : {}),
      },
    })
    // The receipt may contain conflicting/qualifying records with no explicit closure edges.
    // Preserve the entire candidate set, or explicitly omit it so the planner can narrow its query.
    const full = candidate(records.length)
    const fullMessage = envelope(full)
    if (fits(fullMessage)) return { receipt: full, message: fullMessage }
    const omitted = candidate(0)
    const omittedMessage = envelope(omitted)
    this.assertWithin([...messages, omittedMessage])
    return { receipt: omitted, message: omittedMessage }
  }
}
