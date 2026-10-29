import { KnowledgeProposalSchema } from '../knowledge/contracts.ts'
import { CommitmentCommandSchema } from '../domain/commitments.ts'
import { RelationProposalSchema } from '../domain/relations.ts'
import { z } from '@hono/zod-openapi'
import { publicTechnicalDomains } from '../../learning-policy.ts'
import type { ToolDefinition } from '../../models.ts'
export const CandidateSubjectSchema = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('self'), sourceMessageId: z.string().min(1) }).strict(),
  z
    .object({
      kind: z.literal('mentioned'),
      personId: z.string().min(1),
      sourceMessageId: z.string().min(1),
      mentionPartIndex: z.number().int().min(0),
    })
    .strict(),
  z.object({ kind: z.literal('unresolved') }).strict(),
])
const CandidateBaseSchema = z
  .object({
    content: z
      .string()
      .min(1)
      .max(10000)
      .refine((value) => value.trim().length > 0),
    kind: z.enum(['fact', 'claim', 'term', 'experience', 'summary']),
    personId: z.string().optional(),
    subject: CandidateSubjectSchema.optional(),
    relations: z.array(RelationProposalSchema).max(3).optional(),
    knowledge: z.array(KnowledgeProposalSchema).min(1).max(3).optional(),
    commitment: CommitmentCommandSchema.optional(),
    validFrom: z.iso.datetime({ offset: true }).optional(),
    epistemicKind: z
      .enum(['explicit', 'reported', 'observed', 'inferred', 'hypothesis'])
      .optional(),
    sourceMessageIds: z.array(z.string()).min(1).max(20),
    importance: z.enum(['low', 'medium', 'high']).default('medium'),
    confidence: z.number().min(0).max(1).default(0.5),
    supersedesId: z.string().optional(),
    mergeIntoId: z.string().optional(),
    publicTechnicalDomain: z.enum(publicTechnicalDomains).optional(),
    occurredAt: z
      .string()
      .refine((value) => !value || Number.isFinite(Date.parse(value)))
      .optional(),
    validUntil: z
      .string()
      .refine((value) => !value || Number.isFinite(Date.parse(value)))
      .optional(),
  })
  .strict()

const refineCandidate = (value: z.infer<typeof CandidateBaseSchema>, ctx: z.RefinementCtx) => {
  if (value.knowledge !== undefined && value.publicTechnicalDomain !== undefined)
    ctx.addIssue({
      code: 'custom',
      path: ['knowledge'],
      message: 'Structured knowledge is scene-scoped and cannot request public review',
    })
  for (const [index, proposal] of (value.knowledge ?? []).entries())
    if (proposal.sourceSpans.some((span) => !value.sourceMessageIds.includes(span.messageId)))
      ctx.addIssue({
        code: 'custom',
        path: ['knowledge', index, 'sourceSpans'],
        message: 'Knowledge span source must belong to the candidate source group',
      })
  if (
    value.knowledge?.some((proposal) =>
      proposal.sourceSpans.some((span) => span.stance === 'quoted' || span.stance === 'reported'),
    ) &&
    (value.kind !== 'claim' ||
      !['reported', 'inferred', 'hypothesis'].includes(value.epistemicKind ?? ''))
  )
    ctx.addIssue({
      code: 'custom',
      path: ['knowledge'],
      message: 'Quoted or reported knowledge must remain a qualified claim',
    })
  if (!value.subject) return
  for (const key of ['occurredAt', 'validUntil'] as const) {
    if (value[key] !== undefined && !z.iso.datetime({ offset: true }).safeParse(value[key]).success)
      ctx.addIssue({
        code: 'custom',
        path: [key],
        message: 'Explicit subject events require an offset ISO instant or omitted unknown time',
      })
  }
}
export const CandidateSchema = CandidateBaseSchema.superRefine(refineCandidate)
export function buildLearningCandidateSchema(
  relationsEnabled: boolean,
  commitmentsEnabled = false,
  knowledgeEnabled = false,
  sourceOnly = false,
): z.ZodType<z.infer<typeof CandidateSchema>> {
  const schema = CandidateBaseSchema.omit({
    ...(!relationsEnabled ? { relations: true as const } : {}),
    ...(!commitmentsEnabled ? { commitment: true as const } : {}),
    ...(!knowledgeEnabled ? { knowledge: true as const } : {}),
  })
  return schema.superRefine((value, ctx) => {
    refineCandidate(value, ctx)
    const knowledge = (value as z.infer<typeof CandidateBaseSchema>).knowledge
    if (
      sourceOnly &&
      knowledge?.some(
        (proposal) => proposal.subject.kind === 'existing' || proposal.object.kind === 'existing',
      )
    )
      ctx.addIssue({
        code: 'custom',
        path: ['knowledge'],
        message: 'Source-only knowledge cannot reference existing entities',
      })
  })
}

export const definition = (
  name: string,
  description: string,
  schema: z.ZodType,
): ToolDefinition => ({
  name,
  description,
  inputSchema: z.toJSONSchema(schema),
})
export const HabitProposalSchema = z.discriminatedUnion('kind', [
  z
    .object({
      kind: z.literal('sceneRule'),
      content: z.string().min(1).max(1000),
      sourceMessageIds: z.array(z.string()).min(1).max(20),
    })
    .strict(),
  z
    .object({
      kind: z.literal('botExpression'),
      preference: z.enum(['concise', 'detail', 'questions', 'emoji', 'meme_frequency']),
      direction: z.union([z.literal(1), z.literal(-1)]),
      sourceMessageIds: z.array(z.string()).min(1).max(20),
    })
    .strict(),
])
