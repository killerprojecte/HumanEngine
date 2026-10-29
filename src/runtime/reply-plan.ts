import {
  stateEvidenceTools,
  workspaceStateTools,
  validWorkspaceState,
} from './workspace-state-evidence.ts'
import { validateRelationSelection } from './relation-evidence.ts'
import { isDeepStrictEqual } from 'node:util'
import { PlannerEvidenceBudget, type EvidenceCoverage } from './planner-evidence-budget.ts'
import type { ContextProjection } from './context-projection.ts'
import { z } from '@hono/zod-openapi'
import type { ChatMessage, Completion, ToolDefinition } from '../models.ts'

const id = z.string().min(1).max(200)
/** Public instructions and cited evidence only, never private model reasoning. */
export const ReplyPlanSchema = z
  .object({
    action: z.enum(['reply', 'noReply']),
    deliveryTool: id.default('messages.send'),
    targetMessageIds: z.array(id).max(20),
    contextMessageIds: z.array(id).max(24),
    strategy: z.string().max(1000),
    constraints: z.array(z.string().max(500)).max(12),
    facts: z
      .array(
        z
          .object({ text: z.string().min(1).max(1000), evidenceIds: z.array(id).min(1).max(8) })
          .strict(),
      )
      .max(24),
    assets: z.array(z.object({ assetId: id, evidenceId: id }).strict()).max(8),
    emoji: z.array(z.string().min(1).max(32)).max(8),
    expressionGoals: z.array(z.string().min(1).max(500)).max(12).default([]),
    memorySelections: z
      .array(z.object({ evidenceId: id, memoryIds: z.array(id).min(1).max(12) }).strict())
      .max(6)
      .default([]),
    stateEvidenceIds: z.array(id).max(4).default([]),
    replySuggestion: z.string().min(1).max(2000).optional(),
    actions: z
      .array(z.object({ toolName: id, arguments: z.record(z.string(), z.unknown()) }).strict())
      .max(4),
  })
  .strict()
export type ReplyPlan = Omit<z.infer<typeof ReplyPlanSchema>, 'stateEvidenceIds'> & {
  stateEvidenceIds?: string[]
}
export interface PlanEvidence {
  coverage?: EvidenceCoverage
  id: string
  toolName?: string
  value: unknown
}
export const PLANNER_MAX_ROUNDS = 6
export const PLANNER_MAX_TOOL_CALLS = 12
export const DIALOGUE_MAX_ROUNDS = 3
const reads = new Set([
  'messages.get',
  'messages.around',
  'messages.byUser',
  'memory.search',
  'memory.person',
  'memory.relations',
  ...workspaceStateTools,
  'memory.commitments',
  'memory.commitmentHistory',
  'memory.history',
  'memory.source',
  'memes.search',
  'rules.list',
])
const effects = new Set(['memes.feedback', 'habits.feedback', 'habits.propose', 'learning.propose'])
export const plannerReadTools = (tools: ToolDefinition[]) =>
  tools.filter(
    (t) =>
      t.name !== 'messages.send' &&
      !effects.has(t.name) &&
      t.effect !== 'send' &&
      (reads.has(t.name) || t.effect === 'read'),
  )
export const plannerActionTools = (tools: ToolDefinition[]) =>
  tools.filter(
    (t) =>
      t.name !== 'messages.send' &&
      t.effect !== 'send' &&
      (effects.has(t.name) || t.effect === 'learningCandidate'),
  )
const planTool = (suggestions: boolean): ToolDefinition => ({
  name: 'reply.plan',
  description:
    'Finalize a grounded composition plan, or noReply. This does not send or execute actions. Include only public strategy, necessary cited facts and expression goals, never hidden reasoning or an exact answer unless optional reply suggestions are enabled. Receipt coverage marked limited means evidence was omitted for the byte budget, not that the search found no records; narrow the query when needed and never cite omitted records. Select whole memory.summaries (qualified attributed summaries with every original declaration, not independently verified facts), memory.identities, memory.workingSet, memory.episodes, memory.commitments, memory.history, memory.commitmentHistory or memory.commitmentTimeline receipts using stateEvidenceIds; they are qualified state/history, not current verified facts.',
  inputSchema: z.toJSONSchema(
    suggestions ? ReplyPlanSchema : ReplyPlanSchema.omit({ replySuggestion: true }),
  ),
})
export function validateReplyPlan(
  value: unknown,
  input: {
    targetMessageIds: string[]
    contextMessageIds: string[]
    evidence: PlanEvidence[]
    actionTools: ToolDefinition[]
    deliveryTools?: ToolDefinition[]
    replySuggestionsEnabled?: boolean
  },
): ReplyPlan {
  const parsed = (
    input.replySuggestionsEnabled
      ? ReplyPlanSchema
      : ReplyPlanSchema.omit({ replySuggestion: true })
  ).safeParse(value)
  if (!parsed.success) throw new Error('planner_invalid_plan')
  const plan = parsed.data
  if (
    plan.deliveryTool !== 'messages.send' &&
    !input.deliveryTools?.some((t) => t.name === plan.deliveryTool && t.effect === 'send')
  )
    throw new Error('planner_invalid_delivery_tool')
  const contexts = new Set(input.contextMessageIds)
  const targets = new Set(
    input.targetMessageIds.length ? input.targetMessageIds : input.contextMessageIds,
  )
  for (const source of input.evidence) {
    if (!['messages.get', 'messages.around', 'messages.byUser'].includes(source.toolName ?? ''))
      continue
    const value = source.value as { items?: Array<{ id?: unknown; status?: unknown }> } | undefined
    for (const item of Array.isArray(value?.items) ? value.items : [])
      if (typeof item.id === 'string' && item.status === 'available') {
        contexts.add(item.id)
        targets.add(item.id)
      }
  }
  if (new Set(input.evidence.map((e) => e.id)).size !== input.evidence.length)
    throw new Error('planner_duplicate_evidence')
  if (
    plan.targetMessageIds.some((id) => !targets.has(id)) ||
    plan.contextMessageIds.some((id) => !contexts.has(id)) ||
    plan.targetMessageIds.some((id) => !plan.contextMessageIds.includes(id))
  )
    throw new Error('planner_invalid_target')
  const evidence = new Map(input.evidence.map((e) => [e.id, e]))
  if (
    plan.memorySelections.reduce((count, selection) => count + selection.memoryIds.length, 0) > 12
  )
    throw new Error('planner_unknown_memory_evidence')
  if (
    new Set(plan.memorySelections.map((selection) => selection.evidenceId)).size !==
      plan.memorySelections.length ||
    plan.memorySelections.some(
      (selection) => new Set(selection.memoryIds).size !== selection.memoryIds.length,
    )
  )
    throw new Error('planner_unknown_memory_evidence')
  for (const selection of plan.memorySelections) {
    const receipt = evidence.get(selection.evidenceId)
    const records = Array.isArray(receipt?.value) ? receipt.value : []
    if (
      !['memory.search', 'memory.person', 'memory.relations'].includes(receipt?.toolName ?? '') ||
      !Array.isArray(receipt?.value) ||
      selection.memoryIds.some(
        (memoryId) =>
          !records.some(
            (item: unknown) =>
              item && typeof item === 'object' && 'id' in item && item.id === memoryId,
          ),
      )
    )
      throw new Error('planner_unknown_memory_evidence')
    if (receipt?.toolName === 'memory.relations')
      validateRelationSelection(records, selection.memoryIds)
  }
  if (new Set(plan.stateEvidenceIds).size !== plan.stateEvidenceIds.length)
    throw new Error('planner_unknown_state_evidence')
  for (const stateId of plan.stateEvidenceIds) {
    const receipt = evidence.get(stateId)
    if (
      !receipt ||
      !stateEvidenceTools.includes(receipt.toolName ?? '') ||
      !Array.isArray(receipt.value) ||
      receipt.value.length === 0 ||
      receipt.value.length >
        (receipt.toolName === 'memory.commitments'
          ? 12
          : receipt.toolName === 'memory.commitmentHistory'
            ? 1
            : 20)
    )
      throw new Error('planner_unknown_state_evidence')
    if (workspaceStateTools.includes(receipt.toolName ?? '')) {
      if (receipt.value.length !== 1 || !validWorkspaceState(receipt.toolName!, receipt.value[0]))
        throw new Error('planner_unknown_state_evidence')
      continue
    }
    if (
      receipt.value.some(
        (item) =>
          !item ||
          typeof item !== 'object' ||
          (receipt.toolName === 'memory.commitments'
            ? item.stateKind !== 'commitment' || !item.view || typeof item.view !== 'object'
            : item.stateKind !==
                (receipt.toolName === 'memory.commitmentHistory'
                  ? 'commitment_history'
                  : 'assertion_history') ||
              !Array.isArray(item.revisions) ||
              item.revisions.length === 0 ||
              item.revisions.length > 20),
      )
    )
      throw new Error('planner_unknown_state_evidence')
    if (
      receipt.toolName === 'memory.commitmentHistory' &&
      receipt.value.some(
        (item) =>
          !item.anchor ||
          typeof item.anchor !== 'object' ||
          item.revisions.some(
            (view: unknown) => !view || typeof view !== 'object' || !('ref' in view),
          ),
      )
    )
      throw new Error('planner_unknown_state_evidence')
    if (
      ['memory.history', 'memory.commitmentHistory'].includes(receipt.toolName ?? '') &&
      receipt.value.reduce((sum, item) => sum + item.revisions.length, 0) > 20
    )
      throw new Error('planner_unknown_state_evidence')
  }
  if (plan.facts.some((f) => f.evidenceIds.some((id) => !evidence.has(id))))
    throw new Error('planner_unknown_evidence')
  for (const asset of plan.assets) {
    const source = evidence.get(asset.evidenceId)
    const items =
      source?.value && typeof source.value === 'object' && 'items' in source.value
        ? source.value.items
        : undefined
    if (
      source?.toolName !== 'memes.search' ||
      !Array.isArray(items) ||
      !items.some((item) => item && typeof item === 'object' && item.id === asset.assetId)
    )
      throw new Error('planner_unknown_asset')
  }
  const actionKeys = plan.actions.map((action) =>
    JSON.stringify(action, (_key, value) =>
      value && typeof value === 'object' && !Array.isArray(value)
        ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)))
        : value,
    ),
  )
  if (new Set(actionKeys).size !== actionKeys.length) throw new Error('planner_duplicate_action')
  if (
    plan.actions.some((action) => !input.actionTools.some((tool) => tool.name === action.toolName))
  )
    throw new Error('planner_forbidden_action')
  if (
    plan.action === 'noReply' &&
    (plan.actions.some(
      (action) => !['learning.propose', 'experience.propose'].includes(action.toolName),
    ) ||
      plan.assets.length)
  )
    throw new Error('planner_invalid_no_reply')
  return plan
}

/** A bounded, side-effect-free planning loop. Tool receipts, not summaries, carry provenance. */
export async function buildReplyPlan(input: {
  messages: ChatMessage[]
  replySuggestionsEnabled?: boolean
  evidenceMaxBytes?: number
  projection?: ContextProjection
  readTools: ToolDefinition[]
  actionTools: ToolDefinition[]
  deliveryTools?: ToolDefinition[]
  evidence: PlanEvidence[]
  targetMessageIds: string[]
  contextMessageIds: string[]
  signal: AbortSignal
  eligible(): boolean
  complete(
    messages: ChatMessage[],
    tools: ToolDefinition[],
    signal: AbortSignal,
  ): Promise<Completion>
  deliver?(name: string, value: unknown, input?: Record<string, unknown>): unknown
  execute(name: string, args: Record<string, unknown>, signal: AbortSignal): Promise<unknown>
}) {
  const finalTool = planTool(input.replySuggestionsEnabled === true)
  const readTools = plannerReadTools(input.readTools)
  const messages = [...input.messages]
  const budget = new PlannerEvidenceBudget(input.evidenceMaxBytes)
  const evidence = [...input.evidence]
  let toolCalls = 0
  const check = () => {
    input.signal.throwIfAborted()
    if (!input.eligible()) throw new Error('planner_context_ineligible')
  }
  for (let round = 0; round < PLANNER_MAX_ROUNDS; round++) {
    check()
    budget.assertWithin(messages)
    const result = await withinSignal(
      input.complete(messages, [...readTools, finalTool], input.signal),
      input.signal,
    )
    check()
    const final = result.toolCalls.filter((call) => call.name === finalTool.name)
    if (final.length) {
      if (final.length !== 1 || result.toolCalls.length !== 1)
        throw new Error('planner_ambiguous_plan')
      return {
        plan: validateReplyPlan(
          input.projection?.restoreArguments(final[0]!.arguments) ?? final[0]!.arguments,
          { ...input, evidence },
        ),
        evidence,
        rounds: round + 1,
        toolCalls,
      }
    }
    if (!result.toolCalls.length) throw new Error('planner_missing_plan')
    if (toolCalls + result.toolCalls.length > PLANNER_MAX_TOOL_CALLS)
      throw new Error('planner_tool_budget_exhausted')
    messages.push(result.assistant)
    budget.assertWithin(messages)
    for (const call of result.toolCalls) {
      check()
      if (!readTools.some((tool) => tool.name === call.name))
        throw new Error('planner_forbidden_tool')
      toolCalls++
      const value = await withinSignal(
        input.execute(
          call.name,
          input.projection?.restoreArguments(call.arguments) ?? call.arguments,
          input.signal,
        ),
        input.signal,
      )
      check()
      const delivered = budget.receipt(
        messages,
        { id: `tool:${toolCalls}`, toolName: call.name, value },
        call.id,
        (receipt) => input.projection?.stringify(receipt) ?? JSON.stringify(receipt),
      )
      // Commit only measured, actually delivered evidence to contextual learning authority.
      // Snapshot also detects a callback mutating its input object in place.
      const expectedValue = structuredClone(delivered.receipt.value)
      const acceptedValue = input.deliver
        ? input.deliver(
            call.name,
            delivered.receipt.value,
            input.projection?.restoreArguments(call.arguments) ?? call.arguments,
          )
        : delivered.receipt.value
      if (
        !isDeepStrictEqual(acceptedValue, expectedValue) ||
        !isDeepStrictEqual(delivered.receipt.value, expectedValue)
      )
        throw new Error('planner_context_ineligible')
      // Only delivered members authorize model citations; full receipts remain private upstream.
      evidence.push(delivered.receipt)
      messages.push(delivered.message)
    }
  }
  throw new Error('planner_round_budget_exhausted')
}

/** Stop awaiting promptly even if a trusted extension neglects its cancellation signal. */
export async function withinSignal<T>(work: Promise<T>, signal: AbortSignal): Promise<T> {
  signal.throwIfAborted()
  let abort: () => void = () => {}
  try {
    return await Promise.race([
      work,
      new Promise<never>((_, reject) => {
        abort = () => reject(signal.reason ?? new Error('stage_cancelled'))
        signal.addEventListener('abort', abort, { once: true })
        if (signal.aborted) abort()
      }),
    ])
  } finally {
    signal.removeEventListener('abort', abort)
  }
}
