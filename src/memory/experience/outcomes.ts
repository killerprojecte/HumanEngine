import { createHash } from 'node:crypto'
import type { ExperienceContext, ExperienceOutcome, TrustedFeedback } from '../domain/experience.ts'
export const experienceKey = (...parts: string[]) =>
  createHash('sha256').update(JSON.stringify(parts)).digest('hex')
export const sourceKey = (scene: string, message: string) => experienceKey('source', scene, message)
export function createOutcome(
  ctx: ExperienceContext,
  input: TrustedFeedback,
  id: string,
): ExperienceOutcome {
  const codes = [
    'request_exact_person_lookup',
    'request_verify_plan_qualifiers',
    'request_conclusion_first',
    'counterexample',
    'withdraw_instruction',
    'user_reported_helpful',
    'withdraw_person_instruction',
    'withdraw_plan_instruction',
  ]
  if (
    !codes.includes(input.code) ||
    !input.source.messageId ||
    !input.source.senderId ||
    !input.target.messageId ||
    !Number.isFinite(Date.parse(input.observedAt)) ||
    !/(?:Z|[+-]\d\d:\d\d)$/.test(input.observedAt)
  )
    throw new Error('experience_invalid_feedback')
  if (
    [
      'counterexample',
      'withdraw_instruction',
      'user_reported_helpful',
      'withdraw_person_instruction',
      'withdraw_plan_instruction',
    ].includes(input.code) &&
    (!input.policyRef ||
      !Number.isSafeInteger(input.policyRef.version) ||
      input.policyRef.version < 1)
  )
    throw new Error('experience_target_required')
  return {
    ...structuredClone(input),
    id,
    version: 1,
    sceneId: ctx.sceneId,
    rootId: experienceKey(ctx.sceneId, input.source.messageId, input.target.messageId),
    rootFamily: `feedback:${input.code}`,
  }
}
