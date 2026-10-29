import type { RuntimeMessage } from '../../runtime.ts'
import type { TrustedFeedback, ExperienceReference } from '../domain/experience.ts'

const requests = new Set(['请先说结论', '先说结论', '先给结论再给细节', '请先给结论再给细节'])
const exactPersonRequests = new Set(['涉及我的问题先查我的记忆', '涉及我的问题请先查我的记忆'])
const planRequests = new Set(['回答计划问题前先核对条件', '回答计划问题前请先核对条件'])
const personWithdrawals = new Set(['取消先查我的记忆', '不要再先查我的记忆'])
const planWithdrawals = new Set(['取消先核对计划条件', '不要再先核对计划条件'])
const withdrawals = new Set([
  '不要先说结论',
  '取消先说结论',
  '不要再先说结论',
  '别先说结论',
  '取消先给结论再给细节',
  '不要再先给结论再给细节',
])
export interface AppliedPolicyFeedback {
  policyRef: ExperienceReference
  operation: 'memory_person' | 'plan_qualifiers_checked'
}
const positiveOperations = new Map<string, AppliedPolicyFeedback['operation']>([
  ['这次先查我的记忆有帮助', 'memory_person'],
  ['这次先核对计划条件有帮助', 'plan_qualifiers_checked'],
])
const negativeOperations = new Map<string, AppliedPolicyFeedback['operation']>([
  ['这次先查我的记忆不合适', 'memory_person'],
  ['这次先核对计划条件不合适', 'plan_qualifiers_checked'],
])
const validReference = (value: ExperienceReference | undefined) =>
  Boolean(
    value &&
    typeof value.id === 'string' &&
    value.id.length > 0 &&
    Number.isSafeInteger(value.version) &&
    value.version > 0,
  )
const clock = (value: string) =>
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
  Number.isFinite(Date.parse(value))

/** Caller supplies an authorized prior bot message and its exact exposed policy; withdrawal is not an effectiveness verdict. Service rechecks both authorities. */
export function deriveExplicitPolicyFeedback(
  sceneId: string,
  message: RuntimeMessage,
  authorizedPriorBot: RuntimeMessage | undefined,
  selectedPolicy?: ExperienceReference,
  appliedPolicy?: AppliedPolicyFeedback,
): TrustedFeedback | undefined {
  if (
    !authorizedPriorBot ||
    message.sceneId !== sceneId ||
    authorizedPriorBot.sceneId !== sceneId ||
    message.sourceEventRegistrationLimited ||
    authorizedPriorBot.sourceEventRegistrationLimited ||
    message.origin !== 'live' ||
    message.status !== 'available' ||
    message.senderId === 'bot' ||
    authorizedPriorBot.origin !== 'live' ||
    authorizedPriorBot.status !== 'available' ||
    authorizedPriorBot.senderId !== 'bot' ||
    message.replyToId !== authorizedPriorBot.id ||
    message.id === authorizedPriorBot.id
  )
    return
  if (
    !clock(message.receivedAt) ||
    !clock(authorizedPriorBot.receivedAt) ||
    Date.parse(authorizedPriorBot.receivedAt) > Date.parse(message.receivedAt)
  )
    return
  if (!message.parts.length || message.parts.some((part) => part.type !== 'text')) return
  // Exact bounded imperatives only: quotes, thanks, ACK, silence and vague sentiment are not evidence.
  const text = message.parts.map((part) => (part.type === 'text' ? part.text : '')).join('')
  if (text.length > 80) return
  const normalized = text
    .normalize('NFKC')
    .trim()
    .replace(/[\s，,]/gu, '')
    .replace(/[。.!！?？]+$/u, '')
  const positiveOperation = positiveOperations.get(normalized)
  const negativeOperation = negativeOperations.get(normalized)
  if ((positiveOperation || negativeOperation) && /[?？]/u.test(text)) return
  const code = positiveOperation
    ? 'user_reported_helpful'
    : negativeOperation
      ? 'counterexample'
      : requests.has(normalized)
        ? 'request_conclusion_first'
        : exactPersonRequests.has(normalized)
          ? 'request_exact_person_lookup'
          : planRequests.has(normalized)
            ? 'request_verify_plan_qualifiers'
            : personWithdrawals.has(normalized)
              ? 'withdraw_person_instruction'
              : planWithdrawals.has(normalized)
                ? 'withdraw_plan_instruction'
                : withdrawals.has(normalized)
                  ? 'withdraw_instruction'
                  : undefined
  if (!code) return
  const withdrawal =
    code === 'withdraw_instruction' ||
    code === 'withdraw_person_instruction' ||
    code === 'withdraw_plan_instruction'
  if (withdrawal && !validReference(selectedPolicy)) return
  if (
    (code === 'counterexample' || code === 'user_reported_helpful') &&
    (!appliedPolicy ||
      appliedPolicy.operation !== (positiveOperation ?? negativeOperation) ||
      !validReference(appliedPolicy.policyRef))
  )
    return

  return {
    source: {
      messageId: message.id,
      senderId: message.senderId,
      ...(message.sourceEventRef ? { eventRef: structuredClone(message.sourceEventRef) } : {}),
    },
    target: {
      messageId: authorizedPriorBot.id,
      ...(authorizedPriorBot.sourceEventRef
        ? { eventRef: structuredClone(authorizedPriorBot.sourceEventRef) }
        : {}),
    },
    code,
    ...(withdrawal ? { policyRef: { ...selectedPolicy! } } : {}),
    ...(code === 'counterexample' || code === 'user_reported_helpful'
      ? { policyRef: { ...appliedPolicy!.policyRef } }
      : {}),
    observedAt: message.receivedAt,
  }
}
