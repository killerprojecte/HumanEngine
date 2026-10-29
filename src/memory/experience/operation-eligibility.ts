import type { ExperienceAction, ExperienceCondition } from '../domain/experience.ts'

/** Observed authorized read eligibility, not expression correctness or task success. */
export function appliedExperienceOperation(input: {
  action: ExperienceAction
  condition: ExperienceCondition
  tool: string
  arguments: Record<string, unknown>
  value: unknown
  semanticRead: boolean
  turnValid: boolean
}): 'memory_person' | 'plan_qualifiers_checked' | undefined {
  if (!input.turnValid || !Array.isArray(input.value) || !input.value.length) return
  if (
    input.tool === 'memory.person' &&
    !input.semanticRead &&
    input.action === 'exact_person_before_semantic' &&
    input.condition.kind === 'person_question' &&
    input.arguments.personId === input.condition.personId
  )
    return 'memory_person'
  if (
    input.tool === 'memory.relations' &&
    input.action === 'verify_plan_qualifiers' &&
    input.condition.kind === 'plan_question' &&
    input.value.every(
      (row) =>
        row &&
        typeof row === 'object' &&
        row.relationCoverage === 'complete' &&
        Array.isArray(row.relationEvidence),
    ) &&
    input.value.some((row) => row.relationEvidence.length > 0)
  )
    return 'plan_qualifiers_checked'
  // No operation receipt for style: a transport ACK cannot establish prose ordering.
}
