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
    draftGoals: z.array(z.string().min(1).max(500)).max(12),
    actions: z
      .array(z.object({ toolName: id, arguments: z.record(z.string(), z.unknown()) }).strict())
      .max(4),
  })
  .strict()
export type ReplyPlan = z.infer<typeof ReplyPlanSchema>
export interface PlanEvidence {
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
const planTool: ToolDefinition = {
  name: 'reply.plan',
  description:
    'Finalize a grounded composition plan, or noReply. This does not send or execute actions. Include only public strategy, necessary cited facts and draft goals, never hidden reasoning.',
  inputSchema: z.toJSONSchema(ReplyPlanSchema),
}
export function validateReplyPlan(
  value: unknown,
  input: {
    targetMessageIds: string[]
    contextMessageIds: string[]
    evidence: PlanEvidence[]
    actionTools: ToolDefinition[]
    deliveryTools?: ToolDefinition[]
  },
): ReplyPlan {
  const parsed = ReplyPlanSchema.safeParse(value)
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
  if (plan.action === 'noReply' && (plan.actions.length || plan.assets.length))
    throw new Error('planner_invalid_no_reply')
  return plan
}

/** A bounded, side-effect-free planning loop. Tool receipts, not summaries, carry provenance. */
export async function buildReplyPlan(input: {
  messages: ChatMessage[]
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
  execute(name: string, args: Record<string, unknown>, signal: AbortSignal): Promise<unknown>
}) {
  const readTools = plannerReadTools(input.readTools)
  const messages = [...input.messages]
  const evidence = [...input.evidence]
  let toolCalls = 0
  const check = () => {
    input.signal.throwIfAborted()
    if (!input.eligible()) throw new Error('planner_context_ineligible')
  }
  for (let round = 0; round < PLANNER_MAX_ROUNDS; round++) {
    check()
    const result = await withinSignal(
      input.complete(messages, [...readTools, planTool], input.signal),
      input.signal,
    )
    check()
    const final = result.toolCalls.filter((call) => call.name === planTool.name)
    if (final.length) {
      if (final.length !== 1 || result.toolCalls.length !== 1)
        throw new Error('planner_ambiguous_plan')
      return {
        plan: validateReplyPlan(final[0]!.arguments, { ...input, evidence }),
        evidence,
        rounds: round + 1,
        toolCalls,
      }
    }
    if (!result.toolCalls.length) throw new Error('planner_missing_plan')
    if (toolCalls + result.toolCalls.length > PLANNER_MAX_TOOL_CALLS)
      throw new Error('planner_tool_budget_exhausted')
    messages.push(result.assistant)
    for (const call of result.toolCalls) {
      check()
      if (!readTools.some((tool) => tool.name === call.name))
        throw new Error('planner_forbidden_tool')
      toolCalls++
      const value = await withinSignal(
        input.execute(call.name, call.arguments, input.signal),
        input.signal,
      )
      check()
      if (Buffer.byteLength(JSON.stringify(value) ?? '') > 65536)
        throw new Error('planner_evidence_too_large')
      const receipt = { id: `tool:${toolCalls}`, toolName: call.name, value }
      evidence.push(receipt)
      messages.push({ role: 'tool', toolCallId: call.id, content: JSON.stringify(receipt) })
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
