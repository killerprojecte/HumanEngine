import type { ExperienceAction, ExperienceCondition } from '../domain/experience.ts'

/** Fixed low-priority hints, not permissions, tools, factual evidence or user-authored templates. */
const hints: Record<ExperienceAction, string> = {
  exact_person_before_semantic:
    'For this resolved person question, prefer the existing exact scoped person lookup before semantic expansion. Existing authorization and evidence rules still apply.',
  verify_plan_qualifiers:
    'For this plan question, verify the available agreement, negation and conditions before summarizing. Preserve uncertainty; do not infer consent or permission.',
  conclusion_before_detail:
    'When appropriate, state the supported conclusion briefly before supporting details. Do not omit essential uncertainty, negation, conditions or safety constraints.',
}
export function renderExperienceAction(action: ExperienceAction): string {
  if (!Object.hasOwn(hints, action)) throw new Error('experience_action_unsupported')
  return hints[action]
}
export function isExperienceActionCondition(
  action: ExperienceAction,
  condition: ExperienceCondition,
): boolean {
  if (!condition || typeof condition !== 'object') return false
  const keys = Object.keys(condition).sort().join(',')
  if (action === 'verify_plan_qualifiers')
    return condition.kind === 'plan_question' && keys === 'kind'
  const kind =
    action === 'exact_person_before_semantic'
      ? 'person_question'
      : action === 'conclusion_before_detail'
        ? 'response_style'
        : undefined
  return Boolean(
    kind &&
    condition.kind === kind &&
    'personId' in condition &&
    typeof condition.personId === 'string' &&
    condition.personId.length > 0 &&
    condition.personId.length <= 512 &&
    keys === 'kind,personId',
  )
}
