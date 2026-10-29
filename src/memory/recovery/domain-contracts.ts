import { createHash } from 'node:crypto'
import { z } from 'zod'
const id = z.string().min(1).max(2000)
const hash = z.string().regex(/^[a-f0-9]{64}$/)
const base = { schemaVersion: z.literal(1) }
export const DomainRecoveryEventSchema = z.discriminatedUnion('kind', [
  z.object({ ...base, kind: z.literal('habit_delete'), id, fingerprint: hash }).strict(),
  z
    .object({
      ...base,
      kind: z.literal('meme_delete'),
      id: hash,
      contentHash: hash,
      qualificationHash: hash,
      format: z.enum(['png', 'jpeg', 'gif', 'webp']),
    })
    .strict(),
  z
    .object({
      ...base,
      kind: z.literal('meme_state'),
      id: hash,
      contentHash: hash,
      qualificationHash: hash,
      state: z
        .object({
          available: z.boolean(),
          disabled: z.boolean(),
          knownOnly: z.boolean(),
          shared: z.boolean(),
          privacyReviewed: z.boolean(),
        })
        .strict(),
    })
    .strict(),
  z.object({ ...base, kind: z.literal('experience_delete'), sceneId: id, id }).strict(),
  z
    .object({
      ...base,
      kind: z.literal('experience_state'),
      sceneId: id,
      id,
      qualificationHash: hash,
      version: z.number().int().positive(),
      status: z.enum(['active', 'suspended', 'retired']),
      validationId: id.optional(),
      activationBasis: z.enum(['explicit_preference', 'administrator_review']).optional(),
      reason: z.enum([
        'experimental_mechanical_hint',
        'review_suspended',
        'review_retired',
        'explicit_counterexample',
        'explicit_instruction_withdrawn',
        'support_or_expiry_invalid',
      ]),
    })
    .strict(),
])
export type DomainRecoveryEvent = z.infer<typeof DomainRecoveryEventSchema>
export function parseDomainRecoveryEvent(value: unknown): DomainRecoveryEvent {
  return DomainRecoveryEventSchema.parse(value)
}
/** Hash includes real evidence content, but never copies it into the external ledger. */
export function qualificationHash(value: unknown): string {
  const canonical = (v: unknown): unknown =>
    Array.isArray(v)
      ? v.map(canonical)
      : v && typeof v === 'object'
        ? Object.fromEntries(
            Object.entries(v)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, x]) => [k, canonical(x)]),
          )
        : v
  return createHash('sha256')
    .update(JSON.stringify(canonical(value)))
    .digest('hex')
}
