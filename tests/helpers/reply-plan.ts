import type { ChatMessage, Completion } from '../../src/models.ts'
import type { ReplyPlan } from '../../src/runtime/reply-plan.ts'

/** Test-only minimal plans; production must require an actual planner response. */
export function fixtureReplyPlan(
  messages: ChatMessage[],
  patch: Partial<ReplyPlan> = {},
): ReplyPlan | undefined {
  const user = messages.find((message) => message.role === 'user')
  if (!user) return
  let data: { planContract?: { targetMessageIds: string[]; contextMessageIds: string[] } }
  try {
    data = JSON.parse(user.content)
  } catch {
    return
  }
  if (!data.planContract) return
  return {
    action: 'reply',
    deliveryTool: 'messages.send',
    ...data.planContract,
    strategy: 'Answer the selected fixture context.',
    constraints: [],
    facts: [],
    assets: [],
    emoji: [],
    memorySelections: [],
    expressionGoals: ['Compose a concise fixture reply.'],
    actions: [],
    ...patch,
  }
}
export function fixturePlanCompletion(
  role: string,
  messages: ChatMessage[],
  patch: Partial<ReplyPlan> = {},
): Completion | undefined {
  if (role !== 'planner') return
  const plan = fixtureReplyPlan(messages, patch)
  if (!plan) return
  const toolCalls = [{ id: 'fixture-plan', name: 'reply.plan', arguments: plan }]
  return {
    text: '',
    toolCalls,
    modelId: 'fixture',
    usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
    assistant: { role: 'assistant', content: '', toolCalls },
  }
}
export function withFixturePlan(
  complete: (role: string, messages: ChatMessage[]) => Promise<Completion>,
) {
  return async (role: string, messages: ChatMessage[]) =>
    fixturePlanCompletion(role, messages) ?? complete(role, messages)
}
