import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { MemoryService } from '../../memory.ts'
import type { Store } from '../../store.ts'
import { ErrorSchema } from '../../schema.ts'
import { WorkingSetSelectionSchema, WorkingSetPinPolicySchema } from '../working-set/contracts.ts'
import type { CommitmentView } from '../domain/commitments.ts'
import { CommitmentTermsSchema } from '../domain/commitments.ts'
const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const id = z.string().min(1).max(512)
const source = z.object({ sceneId: id, messageId: id, senderId: id })
const record = z.object({
  id,
  content: z.string(),
  kind: z.string(),
  epistemicKind: z.string().optional(),
  status: z.string(),
  learnedAt: z.string(),
  occurredAt: z.string().optional(),
  validFrom: z.string().optional(),
  validUntil: z.string().optional(),
  sources: z.array(source),
})
const plan = z.object({
  sourceMessageIds: z.array(id),
  terms: CommitmentTermsSchema.optional(),
  recordedAt: z.string().optional(),
  confirmation: z.enum(['proposed', 'confirmed', 'incomplete']),
  lifecycle: z.enum(['pending', 'done', 'cancelled', 'needs_review']),
  qualification: z.literal('declared_not_verified'),
  sourceBodyCoverage: z.literal('unavailable'),
  confirmedParticipantIds: z.array(id),
  missingParticipantIds: z.array(id),
  completedParticipantIds: z.array(id),
  cancelledParticipantIds: z.array(id),
})
const access = WorkingSetSelectionSchema.extend({
  state: z.enum(['unknown', 'hot', 'cold']),
  reviewDue: z.boolean(),
  pinned: z.boolean(),
  pinIntent: z.boolean(),
  pinRevision: z.number().int().nonnegative(),
  pinStatus: z.enum(['none', 'current', 'stale', 'terminal']),
  policyCoverage: z.enum(['complete', 'unknown']),
  metadataRevision: z.number().int().nonnegative(),
  contextSelectedCount: z.number().int().nonnegative(),
  lastSelectedAt: z.number().nullable(),
  protection: z
    .enum(['open_commitment', 'needs_review_commitment', 'unresolved_commitment_coverage'])
    .nullable(),
})
const working = z
  .object({
    policies: z.object({
      items: z.array(WorkingSetPinPolicySchema),
      coverage: z.enum(['complete', 'limited']),
    }),
    enabled: z.boolean(),
    coverage: z.enum(['complete', 'limited', 'disabled']),
    omittedCold: z.number().int().nonnegative(),
    omittedEntries: z.number().int().nonnegative().optional(),
    items: z.array(
      z.discriminatedUnion('kind', [
        z.object({ kind: z.literal('assertion'), record, accessibility: access }),
        z.object({ kind: z.literal('commitment'), view: plan, accessibility: access }),
      ]),
    ),
  })
  .openapi('MemoryWorkingSetView')
const episodes = z
  .object({
    enabled: z.boolean(),
    interpretation: z.literal('independent_qualified_records_not_synthesized_episodes'),
    sourceBodyCoverage: z.literal('unavailable'),
    coverage: z.object({
      status: z.enum(['complete', 'limited']),
      returned: z.number().int().nonnegative(),
      omittedEntries: z.number().int().nonnegative(),
    }),
    items: z.array(
      z.discriminatedUnion('kind', [
        z.object({
          kind: z.literal('reported_experience'),
          record,
          time: z.object({ occurredAt: z.string().nullable(), recordedAt: z.string() }),
        }),
        z.object({
          kind: z.literal('shared_plan_declaration'),
          view: plan,
          completionBasis: z.literal('participant_self_report_not_observed_event'),
        }),
      ]),
    ),
  })
  .openapi('MemoryEpisodesView')
const errors = {
  400: json(ErrorSchema, 'Invalid request'),
  401: json(ErrorSchema, 'Authentication required'),
  404: json(ErrorSchema, 'Scene or source unavailable'),
  409: json(ErrorSchema, 'Pin revision or qualification changed'),
}
/** Mount only after the administration authentication and recovery-readiness guards. */
export function installWorkspaceRoutes(
  app: OpenAPIHono,
  { memory, store }: { memory: MemoryService; store: Store },
) {
  const valid = (sceneId: string) => ['group', 'private'].includes(store.scene(sceneId)?.kind ?? '')
  const ctx = (sceneId: string) => ({ sceneId, simulation: false })
  const planView = (value: CommitmentView) => ({
    ...value,
    sourceMessageIds: [
      ...new Set(
        [...value.evidence, ...value.requiredProposalGroups.flat()].flatMap((evidence) =>
          evidence.proofs.flatMap((proof) => proof.sources.map((source) => source.messageId)),
        ),
      ),
    ],
  })
  const input = z.object({ sceneId: id, personId: id.optional() }).strict()
  const read = (sceneId: string, personId?: string, includeCold = false) => {
    const result = memory.workspace.workingSet(ctx(sceneId), { personId, includeCold })
    return working.parse({
      ...result,
      policies: memory.workspace.pinPolicies(ctx(sceneId)),
      items: result.items.map((item) =>
        item.kind === 'assertion'
          ? { kind: item.kind, record: item.snapshot.record, accessibility: item.accessibility }
          : { ...item, view: planView(item.view) },
      ),
    })
  }
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-working-set',
      request: {
        query: input.extend({ includeCold: z.enum(['true', 'false']).optional() }).strict(),
      },
      responses: { 200: json(working), ...errors },
    }),
    (c) => {
      const { sceneId, personId, includeCold } = c.req.valid('query')
      if (!valid(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      return c.json(read(sceneId, personId, includeCold === 'true'), 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-episodes',
      request: { query: input },
      responses: { 200: json(episodes), ...errors },
    }),
    (c) => {
      const { sceneId, personId } = c.req.valid('query')
      if (!valid(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      return c.json(
        episodes.parse({
          ...(() => {
            const value = memory.workspace.episodes(ctx(sceneId), { personId })
            return {
              ...value,
              items: value.items.map((item) =>
                item.kind === 'shared_plan_declaration'
                  ? { ...item, view: planView(item.view) }
                  : item,
              ),
            }
          })(),
          enabled: store.settings.memoryEpisodesEnabled,
        }),
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-working-set/pin',
      request: {
        body: {
          content: {
            'application/json': {
              schema: z
                .object({
                  sceneId: id,
                  selection: WorkingSetSelectionSchema,
                  expectedRevision: z.number().int().nonnegative().safe(),
                  pinned: z.boolean(),
                })
                .strict(),
            },
          },
        },
      },
      responses: { 200: json(WorkingSetPinPolicySchema), ...errors },
    }),
    (c) => {
      const { sceneId, selection, ...change } = c.req.valid('json')
      if (!valid(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      try {
        return c.json(memory.workspace.access.setPinned(ctx(sceneId), selection, change), 200)
      } catch (error) {
        if (
          error instanceof Error &&
          [
            'working_set_policy_conflict',
            'working_set_source_stale',
            'working_set_policy_terminal',
            'working_set_disabled',
          ].includes(error.message)
        )
          return c.json(
            { error: 'Pin state changed or is unavailable; refresh before retrying' },
            409,
          )
        if (error instanceof Error && error.message === 'working_set_policy_not_found')
          return c.json({ error: 'Pin unavailable' }, 404)
        throw error
      }
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-working-set/rebuild',
      request: { body: { content: { 'application/json': { schema: input } } } },
      responses: { 200: json(working), ...errors },
    }),
    (c) => {
      const { sceneId, personId } = c.req.valid('json')
      if (!valid(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      return c.json(read(sceneId, personId, true), 200)
    },
  )
}
