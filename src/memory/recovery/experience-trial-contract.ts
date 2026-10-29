import { z } from 'zod'
const id = z.string().min(1).max(512)
const version = z.number().int().positive().safe()
const hash = z.string().regex(/^[a-f0-9]{64}$/)
const schema = z
  .object({
    kind: z.literal('experience_trial'),
    schemaVersion: z.literal(1),
    operation: z.enum(['start', 'collect', 'promote', 'suspend', 'expire', 'erase']),
    policyId: id,
    sceneId: id,
    expectedPolicyVersion: version,
    nextPolicyVersion: version,
    expectedReceiptVersion: version.nullable(),
    nextReceiptVersion: version.nullable(),
    beforeHash: hash.nullable(),
    afterHash: hash.nullable(),
    nextPhase: z.enum(['trial', 'promoted', 'suspended', 'expired']).optional(),
    nextReason: z
      .enum([
        'explicit_instruction_trial',
        'user_reported_helpfulness',
        'negative_feedback',
        'expiry',
        'qualification_changed',
        'instruction_withdrawn',
      ])
      .optional(),
  })
  .strict()
  .superRefine((e, ctx) => {
    const first = e.operation === 'start',
      erased = e.operation === 'erase'
    if (
      (first
        ? e.expectedReceiptVersion !== null || e.nextReceiptVersion !== 1
        : e.expectedReceiptVersion === null ||
          (!erased && e.nextReceiptVersion !== e.expectedReceiptVersion + 1)) ||
      erased !== (e.nextReceiptVersion === null) ||
      (e.expectedReceiptVersion === null) !== (e.beforeHash === null) ||
      (e.nextReceiptVersion === null) !== (e.afterHash === null) ||
      (e.nextReceiptVersion !== null) !== !!e.nextPhase ||
      (e.operation === 'promote'
        ? e.nextPolicyVersion !== e.expectedPolicyVersion + 1 || e.nextPhase !== 'promoted'
        : e.nextPolicyVersion !== e.expectedPolicyVersion) ||
      (first && e.nextPhase !== 'trial') ||
      (e.operation === 'collect' && e.nextPhase !== 'trial') ||
      (e.operation === 'suspend' && e.nextPhase !== 'suspended') ||
      (e.operation === 'expire' && e.nextPhase !== 'expired')
    )
      ctx.addIssue({ code: 'custom', message: 'invalid_experience_trial_transition' })
  })
export type ExperienceTrialRecoveryEvent = z.infer<typeof schema>
export function parseExperienceTrialRecoveryEvent(value: unknown): ExperienceTrialRecoveryEvent {
  const result = schema.safeParse(value)
  if (!result.success) throw new Error('recovery_experience_trial_event_invalid')
  return result.data
}
