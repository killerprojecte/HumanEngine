import { z } from 'zod'
import { WorkingSetPinPolicySchema } from '../working-set/contracts.ts'
const schema = z
  .object({
    kind: z.literal('working_set_pin'),
    schemaVersion: z.literal(1),
    policy: WorkingSetPinPolicySchema,
  })
  .strict()
export type WorkingSetPinRecoveryEvent = z.infer<typeof schema>
export function parseWorkingSetPinRecoveryEvent(value: unknown): WorkingSetPinRecoveryEvent {
  const result = schema.safeParse(value)
  if (!result.success) throw new Error('recovery_working_set_pin_invalid')
  return result.data
}
