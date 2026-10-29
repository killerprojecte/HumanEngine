import { ProofTraversalLimit } from '../read/provenance.ts'
import { parentBindingState } from '../domain/proofs.ts'
import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { MemoryService } from '../../memory.ts'
import type { Store } from '../../store.ts'
import { ErrorSchema } from '../../schema.ts'

const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const identifier = z.string().min(1).max(2048)
const revision = z
  .object({
    sourceBodyCoverage: z.literal('unavailable'),
    version: z.number().int().positive(),
    recordedAt: z.string().nullable(),
    validFrom: z.string().nullable(),
    validTo: z.string().nullable(),
    content: z.string(),
    status: z.enum(['active', 'disputed', 'superseded', 'invalidated']),
    kind: z.enum(['fact', 'claim', 'term', 'experience', 'summary']),
    epistemicKind: z
      .enum(['explicit', 'reported', 'observed', 'inferred', 'hypothesis'])
      .optional(),
    occurredAt: z.string().optional(),
    origin: z.enum(['manual', 'learned']),
    parentBinding: z.enum(['bound', 'legacy_unknown', 'none']),
  })
  .openapi('MemoryRevisionView')

/** Install on the administration app after its authenticated /api/* middleware. */
export function installMemoryHistoryRoutes(
  app: OpenAPIHono,
  { memory, store }: { memory: MemoryService; store: Store },
) {
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-history',
      request: { query: z.object({ sceneId: identifier, id: identifier }).strict() },
      responses: {
        200: json(
          z.object({ revisions: z.array(revision), coverage: z.enum(['complete', 'limited']) }),
        ),
        400: json(ErrorSchema, 'Invalid request'),
        401: json(ErrorSchema, 'Authentication required'),
        404: json(ErrorSchema, 'Memory unavailable'),
        503: json(ErrorSchema, 'Evidence traversal limit reached'),
      },
    }),
    (c) => {
      const { sceneId, id } = c.req.valid('query')
      const scene = store.scene(sceneId)
      if (!scene || !['group', 'private'].includes(scene.kind))
        return c.json({ error: 'Memory unavailable' }, 404)
      let history: ReturnType<MemoryService['historyFor']>
      try {
        history = memory.historyFor({ sceneId, simulation: false }, id)
      } catch (error) {
        if (error instanceof ProofTraversalLimit)
          return c.json({ error: 'Memory history exceeded its safe evidence traversal limit' }, 503)
        throw error
      }
      if (!history) return c.json({ error: 'Memory unavailable' }, 404)
      // Do not expose raw proof groups, actor identities, epochs or import events.
      const revisions = history.revisions.map((item) => ({
        sourceBodyCoverage: 'unavailable' as const,
        version: item.version,
        recordedAt: item.recordedAt,
        validFrom: item.validFrom,
        validTo: item.validTo,
        content: item.record.content,
        status: item.record.status,
        kind: item.record.kind,
        ...(item.record.epistemicKind ? { epistemicKind: item.record.epistemicKind } : {}),
        ...(item.record.occurredAt ? { occurredAt: item.record.occurredAt } : {}),
        origin: item.record.origin,
        parentBinding: item.supports.some((proof) => proof.parents.length > 0)
          ? item.supports.some((proof) => parentBindingState(proof) === 'legacy_unknown')
            ? ('legacy_unknown' as const)
            : ('bound' as const)
          : ('none' as const),
      }))
      return c.json({ revisions, coverage: history.coverage }, 200)
    },
  )
}
