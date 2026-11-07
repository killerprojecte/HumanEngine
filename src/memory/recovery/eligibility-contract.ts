import { createHash } from 'node:crypto'
import { z } from 'zod'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { SupportProof } from '../domain/proofs.ts'

const hash = z.string().regex(/^[a-f0-9]{64}$/)
const state = z
  .object({
    status: z.enum(['active', 'disputed', 'superseded', 'invalidated']),
    lineageStatus: z.enum(['verified', 'unverified', 'quarantined']).nullable(),
  })
  .strict()
const change = z
  .object({
    id: z.string().min(1),
    incarnationHash: hash,
    state,
    materialHash: hash.nullable(),
  })
  .strict()
  .superRefine((value, ctx) => {
    if (eligibilityDenied(value.state) !== (value.materialHash === null))
      ctx.addIssue({ code: 'custom', message: 'invalid_eligibility_material' })
  })
const schema = z
  .object({
    kind: z.literal('assertion_eligibility'),
    schemaVersion: z.literal(1),
    operationId: z.uuid(),
    mode: z.enum(['transition', 'baseline']),
    changes: z.array(change),
  })
  .strict()
  .superRefine((value, ctx) => {
    if (
      new Set(value.changes.map((item) => item.id)).size !== value.changes.length ||
      (value.mode === 'transition' && value.changes.length === 0) ||
      (value.mode === 'baseline' && value.changes.some((item) => !eligibilityDenied(item.state)))
    )
      ctx.addIssue({ code: 'custom', message: 'invalid_eligibility_changes' })
  })

export type AssertionEligibilityState = z.infer<typeof state>
export type AssertionEligibilityChange = z.infer<typeof change>
export type AssertionEligibilityEvent = z.infer<typeof schema>

export function parseAssertionEligibilityEvent(value: unknown): AssertionEligibilityEvent {
  const parsed = schema.safeParse(value)
  if (!parsed.success) throw new Error('recovery_eligibility_event_invalid')
  return parsed.data
}

export function eligibilityState(record: MemoryRecord): AssertionEligibilityState {
  return { status: record.status, lineageStatus: record.lineageStatus ?? null }
}

export function eligibilityDenied(value: AssertionEligibilityState): boolean {
  return value.status === 'invalidated' || value.lineageStatus === 'quarantined'
}

// Stable object-key order; array order and all stored proof fields remain significant.
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  if (value !== null && typeof value === 'object')
    return `{${Object.entries(value)
      .filter(([, item]) => item !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([key, item]) => `${JSON.stringify(key)}:${canonical(item)}`)
      .join(',')}}`
  return JSON.stringify(value)
}
const digest = (value: unknown) => createHash('sha256').update(canonical(value)).digest('hex')

/** IDs cannot be reused after terminal deletion. learnedAt binds the original admission;
 * deletionEpoch is a redaction counter, not an assertion incarnation. */
export function eligibilityIncarnation(record: MemoryRecord): string {
  return digest([record.id, record.learnedAt])
}

/** A witness qualifies supplied trusted material; it cannot reconstruct content or proofs.
 * Only fields explicitly carried by the event are removed from the material. */
export function eligibilityMaterial(record: MemoryRecord, proofs: readonly SupportProof[]): string {
  const { status: _status, lineageStatus: _lineage, ...material } = record
  return digest([material, proofs])
}
