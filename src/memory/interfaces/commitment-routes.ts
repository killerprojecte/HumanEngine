import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { MemoryService } from '../../memory.ts'
import type { Store } from '../../store.ts'
import { ErrorSchema } from '../../schema.ts'
import { CommitmentTermsSchema, type CommitmentView } from '../domain/commitments.ts'
import { displayName } from '../../plugins/builtin/snowluma/profiles.ts'
const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const id = z.string().min(1).max(2048)
const ref = z.object({
  id,
  version: z.number().int().positive(),
  deletionEpoch: z.number().int().nonnegative(),
})
const plan = z
  .object({
    ref,
    mutationVersion: z.number().int().positive(),
    terms: CommitmentTermsSchema.optional(),
    recordedAt: z.string().optional(),
    confirmation: z.enum(['proposed', 'confirmed', 'incomplete']),
    lifecycle: z.enum(['pending', 'done', 'cancelled', 'needs_review']),
    qualification: z.literal('declared_not_verified'),
    sourceBodyCoverage: z.literal('unavailable'),
    changed: z.boolean(),
    participants: z.array(
      z.object({
        personId: id,
        displayName: z.string().optional(),
        confirmed: z.boolean(),
        missing: z.boolean(),
        completed: z.boolean(),
        cancelled: z.boolean(),
      }),
    ),
  })
  .openapi('MemoryCommitmentView')
const errors = {
  400: json(ErrorSchema, 'Invalid request'),
  401: json(ErrorSchema, 'Authentication required'),
  404: json(ErrorSchema, 'Unavailable or disabled'),
}
/** Read-only administration routes; all authority remains in MemoryService. */
export function installCommitmentRoutes(
  app: OpenAPIHono,
  { memory, store }: { memory: MemoryService; store: Store },
) {
  const real = (sceneId: string) => ['group', 'private'].includes(store.scene(sceneId)?.kind ?? '')
  const context = (sceneId: string) => ({
    sceneId,
    simulation: false,
    provenance: 'contextual' as const,
  })
  const project = (sceneId: string, row: CommitmentView) =>
    plan.parse({
      ...row,
      participants: [
        ...new Set([
          ...(row.terms?.participantIds ?? []),
          ...row.confirmedParticipantIds,
          ...row.missingParticipantIds,
          ...row.completedParticipantIds,
          ...row.cancelledParticipantIds,
        ]),
      ].map((personId) => ({
        personId,
        displayName: displayName(
          store.get<{ senderDisplayName?: string }>(`identity:${sceneId}:${personId}`, {})
            .senderDisplayName,
        ),
        confirmed: row.confirmedParticipantIds.includes(personId),
        missing: row.missingParticipantIds.includes(personId),
        completed: row.completedParticipantIds.includes(personId),
        cancelled: row.cancelledParticipantIds.includes(personId),
      })),
    })
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-commitments',
      request: {
        query: z
          .object({
            sceneId: id,
            personId: id.optional(),
            lifecycle: z.enum(['open', 'terminal', 'all']).default('open'),
            limit: z.coerce.number().int().min(1).max(12).optional(),
          })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            sceneId: id,
            enabled: z.boolean(),
            items: z.array(plan),
            coverage: z.enum(['complete', 'limited']),
          }),
        ),
        ...errors,
      },
    }),
    (c) => {
      const { sceneId, ...options } = c.req.valid('query')
      if (!real(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const result = memory.commitments.list(context(sceneId), options)
      return c.json(
        {
          sceneId,
          enabled: store.settings.memoryCommitmentsEnabled,
          ...result,
          items: result.items.map((row) => project(sceneId, row)),
        },
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-commitments/{id}',
      request: {
        params: z.object({ id }).strict(),
        query: z
          .object({
            sceneId: id,
            version: z.coerce.number().int().positive(),
            deletionEpoch: z.coerce.number().int().nonnegative(),
          })
          .strict(),
      },
      responses: { 200: json(plan), ...errors },
    }),
    (c) => {
      const { sceneId, ...version } = c.req.valid('query')
      if (!real(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const value = memory.commitments.current(context(sceneId), {
        id: c.req.valid('param').id,
        ...version,
      })
      return value
        ? c.json(project(sceneId, value), 200)
        : c.json({ error: 'Commitment unavailable' }, 404)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-commitments/{id}/history',
      request: {
        params: z.object({ id }).strict(),
        query: z
          .object({
            sceneId: id,
            version: z.coerce.number().int().positive(),
            deletionEpoch: z.coerce.number().int().nonnegative(),
          })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            revisions: z.array(plan).max(20),
            coverage: z.literal('limited'),
            interpretation: z.literal('previously_recorded_not_current_truth'),
            sourceBodyCoverage: z.literal('unavailable'),
          }),
        ),
        ...errors,
      },
    }),
    (c) => {
      const { sceneId, ...version } = c.req.valid('query')
      if (!real(sceneId) || !store.settings.memoryHistoricalReadEnabled)
        return c.json({ error: 'History unavailable' }, 404)
      const reference = { id: c.req.valid('param').id, ...version }
      const current = memory.commitments.current(context(sceneId), reference)
      if (!current) return c.json({ error: 'Commitment unavailable' }, 404)
      const value = memory.commitments.history(
        {
          ...context(sceneId),
          exposedRefs: [{ ...reference, mutationVersion: current.mutationVersion }],
        },
        reference,
      )
      if (!value) return c.json({ error: 'History unavailable' }, 404)
      return c.json(
        {
          revisions: value.revisions.slice(-20).map((row) => project(sceneId, row)),
          coverage: 'limited' as const,
          interpretation: 'previously_recorded_not_current_truth' as const,
          sourceBodyCoverage: 'unavailable' as const,
        },
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-commitments/{id}/timeline',
      request: {
        params: z.object({ id }).strict(),
        query: z
          .object({
            sceneId: id,
            version: z.coerce.number().int().positive(),
            deletionEpoch: z.coerce.number().int().nonnegative(),
            mutationVersion: z.coerce.number().int().positive(),
            asOfRecorded: z.iso.datetime().optional(),
            limit: z.coerce.number().int().min(1).max(20).optional(),
          })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            recordedStates: z
              .array(
                z.object({
                  recordedAt: z.string(),
                  mutationVersion: z.number().int().positive(),
                  reason: z.string(),
                  activeVersion: z.number().int().positive().nullable(),
                  latestVersion: z.number().int().positive(),
                  status: z.enum(['available', 'redacted']),
                  states: z.array(plan).max(2),
                }),
              )
              .max(20),
            coverage: z.literal('limited'),
            reasons: z.array(z.string()),
            interpretation: z.literal('previously_recorded_declaration_state_not_current_truth'),
            sourceBodyCoverage: z.literal('unavailable'),
            stateTime: z.literal('server_recorded_not_occurred'),
            window: z.object({
              maxTransitions: z.literal(64),
              firstRecordedAt: z.string().nullable(),
              firstMutationVersion: z.number().int().positive().nullable(),
              prunedThroughMutationVersion: z.number().int().nonnegative(),
              throughMutationVersion: z.number().int().positive(),
            }),
          }),
        ),
        ...errors,
      },
    }),
    (c) => {
      const { sceneId, asOfRecorded, limit, ...reference } = c.req.valid('query')
      if (!real(sceneId) || !store.settings.memoryHistoricalReadEnabled)
        return c.json({ error: 'History unavailable' }, 404)
      const anchor = { id: c.req.valid('param').id, ...reference }
      const value = memory.commitments.temporalHistory(
        { ...context(sceneId), exposedRefs: [anchor] },
        anchor,
        { asOfRecorded, limit },
      )
      if (!value) return c.json({ error: 'History unavailable' }, 404)
      return c.json(
        {
          ...value,
          recordedStates: value.recordedStates.map((row) => ({
            ...row,
            states: row.states.map((state) => project(sceneId, state)),
          })),
        },
        200,
      )
    },
  )
  const instant = z
    .string()
    .refine(
      (value) => Number.isFinite(Date.parse(value)) && new Date(value).toISOString() === value,
      'Use a canonical UTC ISO instant',
    )
  const history = z
    .object({
      interpretation: z.literal('previously_recorded_not_current_truth'),
      sourceBodyCoverage: z.literal('unavailable'),
      asOfRecorded: z.string().optional(),
      validAt: z.string().optional(),
      coverage: z.object({
        status: z.enum(['limited', 'retained_scope_only']),
        returned: z.number().int().nonnegative(),
        hasMore: z.boolean(),
      }),
      revisions: z.array(
        z.object({
          version: z.number().int().positive(),
          recordedAt: z.string().nullable(),
          validFrom: z.string().nullable(),
          validTo: z.string().nullable(),
          content: z.string(),
          status: z.enum(['active', 'disputed', 'superseded', 'invalidated']),
          kind: z.string(),
          epistemicKind: z.string().optional(),
          validityCoverage: z.enum(['unknown', 'bounded']),
        }),
      ),
    })
    .openapi('MemoryAssertionHistoricalView')
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-assertion-history',
      request: {
        query: z
          .object({
            sceneId: id,
            memoryId: id,
            asOfRecorded: instant.optional(),
            validAt: instant.optional(),
            limit: z.coerce.number().int().min(1).max(20).optional(),
          })
          .strict(),
      },
      responses: { 200: json(history), ...errors },
    }),
    (c) => {
      const { sceneId, ...input } = c.req.valid('query')
      if (!real(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      try {
        const value = memory.historicalQuery({ sceneId, simulation: false }, input)
        return c.json(
          history.parse({
            ...value,
            revisions: value.revisions.map((revision) => ({
              version: revision.version,
              recordedAt: revision.recordedAt,
              validFrom: revision.validFrom,
              validTo: revision.validTo,
              content: revision.record.content,
              status: revision.record.status,
              kind: revision.record.kind,
              epistemicKind: revision.record.epistemicKind,
              validityCoverage: revision.validityCoverage,
            })),
          }),
          200,
        )
      } catch (error) {
        if (
          error instanceof Error &&
          ['memory_history_disabled', 'memory_history_anchor_unavailable'].includes(error.message)
        )
          return c.json({ error: 'History unavailable' }, 404)
        throw error
      }
    },
  )
}
