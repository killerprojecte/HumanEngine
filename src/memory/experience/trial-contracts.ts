import { z } from 'zod'
import { qualificationHash } from '../recovery/domain-contracts.ts'
const id = z.string().min(1).max(512)
const n = z.number().int().nonnegative().safe()
const hash = z.string().regex(/^[a-f0-9]{64}$/)
export const TrialReceiptSchema = z
  .object({
    schemaVersion: z.literal(1),
    policyId: id,
    sceneId: id,
    version: n.min(1),
    policyVersion: n.min(1),
    evidencePolicyVersion: n.min(1),
    policyHash: hash,
    requesterId: id,
    settingsRevision: n,
    phase: z.enum(['trial', 'promoted', 'suspended', 'expired']),
    createdAt: z.iso.datetime({ offset: true }),
    expiresAt: z.iso.datetime({ offset: true }),
    instruction: z.object({ id, version: n.min(1) }).strict(),
    validationId: id,
    positives: z
      .array(
        z
          .object({
            outcomeId: id,
            outcomeVersion: n.min(1),
            sourceRoot: hash,
            targetRoot: hash,
            turnId: id,
          })
          .strict(),
      )
      .max(2),
    reason: z
      .enum([
        'explicit_instruction_trial',
        'user_reported_helpfulness',
        'negative_feedback',
        'instruction_withdrawn',
        'expiry',
        'qualification_changed',
      ])
      .optional(),
  })
  .strict()
  .superRefine((v, ctx) => {
    if (
      Date.parse(v.expiresAt) <= Date.parse(v.createdAt) ||
      Date.parse(v.expiresAt) - Date.parse(v.createdAt) > 7 * 86400000
    )
      ctx.addIssue({ code: 'custom', message: 'experience_trial_expiry_invalid' })
    for (const key of ['outcomeId', 'sourceRoot', 'targetRoot', 'turnId'] as const)
      if (new Set(v.positives.map((p) => p[key])).size !== v.positives.length)
        ctx.addIssue({ code: 'custom', message: 'experience_trial_independence_invalid' })
    if (v.phase === 'promoted' && v.positives.length !== 2)
      ctx.addIssue({ code: 'custom', message: 'experience_trial_support_missing' })
  })
export type TrialReceipt = z.infer<typeof TrialReceiptSchema>
export interface TrialMutation {
  operation: 'start' | 'collect' | 'promote' | 'suspend' | 'expire' | 'erase'
  policyId: string
  sceneId: string
  expectedPolicyVersion: number
  expectedReceiptVersion: number | null
  nextReceiptVersion: number | null
  nextPolicyVersion: number
  beforeHash: string | null
  afterHash: string | null
  nextPhase?: TrialReceipt['phase']
  nextReason?: TrialReceipt['reason']
}
export const trialHash = (value: TrialReceipt) => qualificationHash(TrialReceiptSchema.parse(value))
