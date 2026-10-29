import { z } from 'zod'
import { EndpointSchema } from '../identities/contracts.ts'
const id = z.string().min(1).max(160)
const revision = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER)
const hash = z.string().regex(/^[a-f0-9]{64}$/)
const schema = z
  .object({
    kind: z.literal('identity_mutation'),
    schemaVersion: z.literal(1),
    operation: z.enum(['link', 'unlink', 'split', 'alias_put', 'alias_remove', 'source_recall']),
    operationId: id,
    expectedRevision: revision,
    nextRevision: revision,
    beforeHash: hash,
    qualificationHash: hash,
    targets: z
      .object({
        endpoints: z.array(EndpointSchema).max(32),
        aliasIds: z.array(id).max(4096),
        source: z.object({ sceneId: id, messageId: id }).strict().optional(),
      })
      .strict(),
  })
  .strict()
  .superRefine((event, ctx) => {
    const t = event.targets
    const valid =
      event.nextRevision === event.expectedRevision + 1 &&
      ((['link', 'unlink'].includes(event.operation) &&
        t.endpoints.length === 2 &&
        !t.aliasIds.length &&
        !t.source) ||
        (event.operation === 'split' &&
          t.endpoints.length > 0 &&
          !t.aliasIds.length &&
          !t.source) ||
        (event.operation === 'alias_put' && t.endpoints.length === 1 && t.aliasIds.length === 1) ||
        (event.operation === 'alias_remove' &&
          !t.endpoints.length &&
          t.aliasIds.length === 1 &&
          !t.source) ||
        (event.operation === 'source_recall' && !t.endpoints.length && !!t.source))
    if (!valid) ctx.addIssue({ code: 'custom', message: 'invalid_identity_transition' })
  })
export type IdentityRecoveryEvent = z.infer<typeof schema>
export function parseIdentityRecoveryEvent(value: unknown): IdentityRecoveryEvent {
  const result = schema.safeParse(value)
  if (!result.success) throw new Error('recovery_identity_event_invalid')
  return result.data
}
