import { z } from 'zod'
import { ExternalCostReceiptSchema } from '../../budget/external-cost/contracts.ts'
const schema = z
  .object({ kind: z.literal('external_cost_receipt'), receipt: ExternalCostReceiptSchema })
  .strict()
  .superRefine((e, ctx) => {
    if ((e.receipt.revision === 1) !== (e.receipt.correctionId === null))
      ctx.addIssue({ code: 'custom', message: 'invalid_external_cost_revision' })
  })
export type ExternalCostRecoveryEvent = z.infer<typeof schema>
export function parseExternalCostRecoveryEvent(value: unknown): ExternalCostRecoveryEvent {
  const parsed = schema.safeParse(value)
  if (!parsed.success) throw new Error('recovery_external_cost_event_invalid')
  return parsed.data
}
