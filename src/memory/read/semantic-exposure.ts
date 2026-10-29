import { isDeepStrictEqual } from 'node:util'
import { z } from 'zod'
import {
  SemanticAnchorQualificationSchema,
  SemanticRefSchema,
  SemanticProofSchema,
  type SemanticView,
} from '../semantic/contracts.ts'
import { parentBindingState } from '../domain/proofs.ts'
import { SourceEventRefSchema } from '../domain/events.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'
const source = z.object({
  sceneId: z.string(),
  messageId: z.string(),
  senderId: z.string(),
  eventRef: SourceEventRefSchema.optional(),
})
const record = z.object({
  id: z.string(),
  scope: z.object({
    kind: z.literal('scene'),
    sceneId: z.string(),
    personId: z.string().optional(),
  }),
  content: z.string(),
  kind: z.string(),
  epistemicKind: z.string().optional(),
  status: z.enum(['active', 'disputed']),
  origin: z.string(),
  learnedAt: z.string(),
  observedAt: z.string().optional(),
  occurredAt: z.string().optional(),
  validFrom: z.string().optional(),
  validUntil: z.string().optional(),
  internalUse: z.string().optional(),
  lineageStatus: z.enum(['verified', 'unverified', 'quarantined']).optional(),
  parentBinding: z.enum(['bound', 'legacy_unknown']).optional(),
  supersedesId: z.string().optional(),
  sources: z.array(source),
})
const {
  proofId: _proofIdSchema,
  invalidatedBy: _invalidatedSchema,
  ...proofShape
} = SemanticProofSchema.shape
const proof = z
  .object(proofShape)
  .strict()
  .refine((value) => parentBindingState(value) !== 'invalid')
const item = z
  .object({
    id: z.string(),
    version: z.number().int().positive(),
    personId: z.string().nullable(),
    kind: z.enum(['summary_candidate', 'duplicate_candidate', 'episode_candidate']),
    text: z.string().min(1).max(6000),
    selectedParentIds: z.array(z.string()).min(2).max(12),
    status: z.enum(['candidate', 'admitted']),
    admission: z.enum([
      'qualified_candidate',
      'structural_low_risk_summary',
      'administrator_review',
    ]),
    hypothesis: z.boolean(),
    anchorQualifications: z.array(SemanticAnchorQualificationSchema).max(40),
    createdAt: z.string(),
    applicableUntil: z.string().nullable(),
    episodeAnchor: z
      .object({ id: z.string(), version: z.number().int().positive() })
      .strict()
      .optional(),
    parents: z
      .array(z.object({ ref: SemanticRefSchema, proofs: z.array(proof).min(1).max(128) }).strict())
      .min(2)
      .max(12),
    declarations: z
      .array(z.object({ ref: SemanticRefSchema, record }).strict())
      .min(2)
      .max(12),
    rootCount: z.number().int().nonnegative(),
    applicability: z.enum(['current', 'review_needed']),
    interpretation: z.enum([
      'attributed_summary_not_verified_truth',
      'possible_duplicate_not_merged',
      'grouped_declarations_not_observed_event',
    ]),
  })
  .strict()
  .superRefine((v, ctx) => {
    if ((v.kind === 'episode_candidate') !== v.anchorQualifications.length > 0)
      ctx.addIssue({ code: 'custom', message: 'semantic_anchor_qualifications_missing' })
    if (v.kind === 'summary_candidate' && v.status !== 'admitted')
      ctx.addIssue({ code: 'custom', message: 'semantic_summary_not_admitted' })
    const expected =
      v.kind === 'summary_candidate'
        ? 'attributed_summary_not_verified_truth'
        : v.kind === 'duplicate_candidate'
          ? 'possible_duplicate_not_merged'
          : 'grouped_declarations_not_observed_event'
    if (
      v.interpretation !== expected ||
      new Set(v.parents.map((p) => p.ref.id)).size !== v.parents.length ||
      v.parents.length !== v.declarations.length ||
      v.declarations.some(
        (d) => d.ref.id !== d.record.id || !v.parents.some((p) => isDeepStrictEqual(p.ref, d.ref)),
      ) ||
      v.selectedParentIds.some((id) => !v.parents.some((p) => p.ref.id === id))
    )
      ctx.addIssue({ code: 'custom', message: 'semantic_declarations_incomplete' })
  })
export const SemanticStateEvidenceSchema = z
  .object({
    stateKind: z.literal('semantic_view'),
    page: z
      .object({
        coverage: z.enum(['complete', 'limited']),
        sourceBodyCoverage: z.literal('unavailable'),
        items: z.array(item).max(12),
      })
      .strict(),
  })
  .strict()
export type SemanticStateEvidence = z.infer<typeof SemanticStateEvidenceSchema>
export interface SemanticExposurePort {
  query(ctx: ModelMemoryContext): { items: SemanticView[]; coverage: 'complete' | 'limited' }
  allowed(ctx: ModelMemoryContext): boolean
  policyIdentity(ctx: ModelMemoryContext): string | number
  onDelivered?(ctx: ModelMemoryContext, values: readonly SemanticStateEvidence[]): void
}
export function semanticModelPage(
  raw: ReturnType<SemanticExposurePort['query']>,
): SemanticStateEvidence {
  return SemanticStateEvidenceSchema.parse({
    stateKind: 'semantic_view',
    page: {
      coverage: raw.coverage,
      sourceBodyCoverage: 'unavailable',
      items: raw.items.map((v) => ({
        id: v.candidate.id,
        version: v.candidate.version,
        personId: v.candidate.personId,
        kind: v.candidate.kind,
        text: v.candidate.text,
        selectedParentIds: v.candidate.selectedParentIds,
        status: v.candidate.status,
        admission: v.candidate.admission,
        hypothesis: v.candidate.hypothesis,
        anchorQualifications: v.candidate.anchorQualifications,
        createdAt: v.candidate.createdAt,
        applicableUntil: v.candidate.applicableUntil,
        episodeAnchor: v.candidate.episodeAnchor,
        parents: v.candidate.parents.map((p) => ({
          ref: p.ref,
          proofs: p.proofs.map(
            ({ proofId: _proofId, invalidatedBy: _invalidatedBy, ...body }) => body,
          ),
        })),
        declarations: v.declarations.map((d) => ({ ref: d.ref, record: record.parse(d.record) })),
        rootCount: v.roots.length,
        applicability: v.applicability,
        interpretation: v.interpretation,
      })),
    },
  })
}
/** A whole first-read page, not a new ordinary assertion or permission to learn from it. */
export class SemanticExposure {
  private readonly prepared = new WeakMap<
    ModelMemoryContext,
    StateExposure<SemanticStateEvidence, undefined>[]
  >()
  constructor(private readonly port: SemanticExposurePort) {}
  query(ctx: ModelMemoryContext): readonly SemanticStateEvidence[] {
    if (ctx.mode !== 'real' || ctx.signal.aborted || !this.port.allowed(ctx))
      return Object.freeze([])
    const prior = this.captureGuard(ctx)
    if (prior && !prior()) return Object.freeze([])
    const raw = structuredClone(this.port.query(ctx)),
      policy = this.port.policyIdentity(ctx),
      first = semanticModelPage(raw)
    if (
      first.page.items.some(
        (v) =>
          v.declarations.some(
            (d) =>
              d.record.scope.sceneId !== ctx.scene.id ||
              d.record.sources.some((source) => source.sceneId !== ctx.scene.id),
          ) ||
          v.parents.some((parent) =>
            parent.proofs.some((proof) =>
              proof.sources.some((source) => source.sceneId !== ctx.scene.id),
            ),
          ),
      )
    )
      throw new Error('semantic_exposure_scope')
    const exposure = new StateExposure<SemanticStateEvidence, undefined>({
      query: () => [first],
      valid: (c, values) => {
        try {
          return (
            this.port.allowed(c) &&
            this.port.policyIdentity(c) === policy &&
            (!prior || prior()) &&
            values.length === 1 &&
            isDeepStrictEqual(this.port.query(c), raw) &&
            isDeepStrictEqual(values[0], first)
          )
        } catch {
          return false
        }
      },
      sourceIds: (values) => [
        ...new Set([
          ...raw.items.flatMap((v) =>
            (v.candidate.anchorDependencies ?? []).flatMap((d) =>
              d.proofs.flatMap((p) => p.sources.map((s) => s.messageId)),
            ),
          ),
          ...values.flatMap((v) =>
            v.page.items.flatMap((i) => [
              ...i.declarations.flatMap((d) => d.record.sources.map((s) => s.messageId)),
              ...i.parents.flatMap((p) =>
                p.proofs.flatMap((g) => g.sources.map((s) => s.messageId)),
              ),
            ]),
          ),
        ]),
      ],
      onDelivered: (c, values) => this.port.onDelivered?.(c, values),
    })
    const values = exposure.query(ctx, undefined)
    if (values.length) this.prepared.set(ctx, [...(this.prepared.get(ctx) ?? []), exposure])
    return values
  }
  deliver(ctx: ModelMemoryContext, values: readonly unknown[]): readonly SemanticStateEvidence[] {
    for (const e of this.prepared.get(ctx) ?? []) {
      const result = e.deliver(ctx, values)
      if (result.length) return result
    }
    return Object.freeze([])
  }
  readDelivered(ctx: ModelMemoryContext): readonly SemanticStateEvidence[] {
    const guards = (this.prepared.get(ctx) ?? []).flatMap((e) => e.captureGuard(ctx) ?? [])
    if (guards.some((g) => !g())) return Object.freeze([])
    return Object.freeze((this.prepared.get(ctx) ?? []).flatMap((e) => e.readDelivered(ctx)))
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    const guards = (this.prepared.get(ctx) ?? []).flatMap((e) => e.captureGuard(ctx) ?? [])
    return guards.length ? () => guards.every((g) => g()) : undefined
  }
}
