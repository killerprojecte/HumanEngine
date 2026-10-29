import { z } from 'zod'

const id = z.string().min(1).max(512)
const counter = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER)
const instant = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER)
const fields = { id, version: counter.min(1), deletionEpoch: counter }
export const WorkingSetReferenceSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('assertion'), ...fields }).strict(),
  z.object({ kind: z.literal('commitment'), ...fields, mutationVersion: counter.min(1) }).strict(),
])
export type WorkingSetReference = z.infer<typeof WorkingSetReferenceSchema>
export const WorkingSetSelectionSchema = z
  .object({
    ref: WorkingSetReferenceSchema,
    dependencyKey: z.string().regex(/^[a-f0-9]{64}$/),
  })
  .strict()
export type WorkingSetSelection = z.infer<typeof WorkingSetSelectionSchema>
export const WorkingSetSnapshotSchema = WorkingSetSelectionSchema.extend({
  sceneId: id,
  personId: id.optional(),
  recordedAt: instant.nullable(),
  epistemicKind: z.enum(['explicit', 'reported', 'observed', 'inferred', 'hypothesis']).optional(),
  protection: z
    .enum(['open_commitment', 'needs_review_commitment', 'unresolved_commitment_coverage'])
    .nullable(),
}).strict()
export type WorkingSetSnapshot = z.infer<typeof WorkingSetSnapshotSchema>
export interface WorkingSetContext {
  sceneId: string
  simulation: boolean
}
export interface WorkingSetPolicy {
  coldAfterMs: number
  reviewAfterMs: number
  leaseMs: number
  maxBatch: number
  maxReceipts: number
  receiptRetentionMs: number
  selectionMaxAgeMs: number
}
export const DEFAULT_WORKING_SET_POLICY: Readonly<WorkingSetPolicy> = Object.freeze({
  coldAfterMs: 30 * 86400_000,
  reviewAfterMs: 14 * 86400_000,
  leaseMs: 30_000,
  maxBatch: 100,
  maxReceipts: 10_000,
  receiptRetentionMs: 86400_000,
  selectionMaxAgeMs: 3600_000,
})
export const WorkingSetPinPolicySchema = z
  .object({
    schemaVersion: z.literal(1),
    sceneId: id,
    selection: WorkingSetSelectionSchema,
    policyRevision: counter.min(1),
    pinned: z.boolean(),
    terminal: z.boolean(),
  })
  .strict()
  .refine((value) => !value.terminal || !value.pinned, { message: 'terminal policy cannot pin' })
export type WorkingSetPinPolicy = z.infer<typeof WorkingSetPinPolicySchema>
export interface WorkingSetPorts {
  assertReady(): void
  enabled(): boolean
  /** One bounded authorized batch. Missing/stale refs must be omitted, never upgraded. */
  resolveCurrent(
    ctx: WorkingSetContext,
    refs: readonly WorkingSetReference[],
  ): readonly WorkingSetSnapshot[]
  /** Reader/schema enrollment hook only, synchronous in the same local transaction. */
  beforeWrite(): void
  /** Explicit policy mutation: external durable intent before the local policy row. */
  beforePinWrite(policy: WorkingSetPinPolicy): void
  now?(): number
  policy?: Partial<WorkingSetPolicy>
}
export const WorkingSetItemSchema = z
  .object({
    schemaVersion: z.literal(1),
    snapshot: WorkingSetSnapshotSchema,
    metadataRevision: counter.min(1),
    contextSelectedCount: counter,
    lastSelectedAt: instant.nullable(),
    projectedAt: instant,
  })
  .strict()
export type WorkingSetItem = z.infer<typeof WorkingSetItemSchema>
export const WorkingSetJobSchema = z
  .object({
    schemaVersion: z.literal(1),
    snapshot: WorkingSetSnapshotSchema,
    generation: z.string().uuid(),
    enqueuedAt: instant,
    lease: z.object({ token: z.string().uuid(), expiresAt: instant }).strict().nullable(),
  })
  .strict()
export type WorkingSetJob = z.infer<typeof WorkingSetJobSchema>
export interface WorkingSetLease {
  sceneId: string
  ref: WorkingSetReference
  dependencyKey: string
  generation: string
  token: string
  expiresAt: number
}
export const WorkingSetReceiptSchema = z
  .object({
    schemaVersion: z.literal(1),
    sceneId: id,
    turnId: id,
    selection: WorkingSetSelectionSchema,
    selectedAt: instant,
  })
  .strict()
export type WorkingSetReceipt = z.infer<typeof WorkingSetReceiptSchema>
export interface WorkingSetViewItem extends WorkingSetSelection {
  state: 'unknown' | 'hot' | 'cold'
  reviewDue: boolean
  pinned: boolean
  pinIntent: boolean
  pinRevision: number
  pinStatus: 'none' | 'current' | 'stale' | 'terminal'
  policyCoverage: 'complete' | 'unknown'
  metadataRevision: number
  contextSelectedCount: number
  lastSelectedAt: number | null
  protection: WorkingSetSnapshot['protection']
}
export const WORKING_SET_TABLES = Object.freeze({
  memory_working_set_pins: ['scene_id', 'kind', 'entity_id', 'value'],
  memory_working_set_items: ['scene_id', 'kind', 'entity_id', 'value'],
  memory_working_set_jobs: ['scene_id', 'kind', 'entity_id', 'value'],
  memory_working_set_receipts: ['receipt_key', 'selected_at', 'value'],
} as const)
