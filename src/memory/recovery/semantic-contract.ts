import { z } from 'zod'
const id = z.string().min(1).max(512),
  version = z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
  hash = z.string().regex(/^[a-f0-9]{64}$/)
const schema = z
  .object({
    kind: z.literal('semantic_mutation'),
    schemaVersion: z.literal(1),
    operation: z.enum(['admit', 'review', 'privacy', 'attempt']),
    id,
    sceneId: id,
    previousVersion: version.nullable(),
    nextVersion: version.nullable(),
    beforeHash: hash.nullable(),
    afterHash: hash.nullable(),
    nextState: z
      .object({
        status: z.enum(['candidate', 'admitted', 'rejected']),
        admission: z.enum([
          'qualified_candidate',
          'structural_low_risk_summary',
          'administrator_review',
        ]),
      })
      .strict()
      .optional(),
  })
  .strict()
  .superRefine((e, ctx) => {
    const pair =
      (e.previousVersion === null) === (e.beforeHash === null) &&
      (e.nextVersion === null) === (e.afterHash === null)
    const transition =
      e.operation === 'admit'
        ? e.previousVersion === null && e.nextVersion === 1
        : e.operation === 'review'
          ? e.previousVersion !== null && e.nextVersion === e.previousVersion + 1
          : e.operation === 'attempt'
            ? (e.previousVersion === null
                ? e.nextVersion === 1
                : e.nextVersion === e.previousVersion + 1) && /^[a-f0-9]{64}$/.test(e.id)
            : e.previousVersion !== null &&
              (e.nextVersion === null || e.nextVersion === e.previousVersion + 1)
    const state =
      (e.operation === 'admit' || e.operation === 'review') === !!e.nextState &&
      (e.operation !== 'review' ||
        (e.nextState?.admission === 'administrator_review' && e.nextState.status !== 'candidate'))
    if (!pair || !transition || !state)
      ctx.addIssue({ code: 'custom', message: 'invalid_semantic_transition' })
  })
export type SemanticRecoveryEvent = z.infer<typeof schema>
export function parseSemanticRecoveryEvent(value: unknown): SemanticRecoveryEvent {
  const result = schema.safeParse(value)
  if (!result.success) throw new Error('recovery_semantic_event_invalid')
  return result.data
}
