import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { Store } from '../../store.ts'
import type { SemanticService } from '../semantic/candidates.ts'
import {
  SemanticAnchorQualificationSchema,
  SemanticRefSchema,
  type SemanticView,
} from '../semantic/contracts.ts'
import { ErrorSchema } from '../../schema.ts'
const id = z.string().min(1).max(512)
const source = z.object({ sceneId: id, messageId: id, senderId: id })
const proof = z.object({
  sources: z.array(source),
  parents: z.array(id),
  parentReferences: z.array(SemanticRefSchema).optional(),
  subjectWitness: z
    .object({
      kind: z.string(),
      personId: id.optional(),
      sourceMessageId: id.optional(),
      mentionPartIndex: z.number().optional(),
    })
    .optional(),
  sourceObservations: z
    .array(source.extend({ sentAt: z.string().optional(), receivedAt: z.string().optional() }))
    .optional(),
})
const record = z.object({
  id,
  content: z.string(),
  kind: z.string(),
  epistemicKind: z.string().optional(),
  status: z.string(),
  origin: z.string(),
  scope: z.object({ kind: z.string(), sceneId: id.optional(), personId: id.optional() }),
  learnedAt: z.string(),
  observedAt: z.string().optional(),
  occurredAt: z.string().optional(),
  validFrom: z.string().optional(),
  validUntil: z.string().optional(),
  sources: z.array(source),
  internalUse: z.string().optional(),
})
const item = z
  .object({
    id,
    version: z.number(),
    personId: id.nullable(),
    kind: z.enum(['summary_candidate', 'duplicate_candidate', 'episode_candidate']),
    text: z.string(),
    selectedParentIds: z.array(id),
    status: z.enum(['candidate', 'admitted', 'rejected']),
    admission: z.string(),
    hypothesis: z.boolean(),
    anchorQualifications: z.array(SemanticAnchorQualificationSchema).max(40),
    createdAt: z.string(),
    applicableUntil: z.string().nullable(),
    episodeAnchor: z.object({ id, version: z.number() }).optional(),
    parents: z.array(z.object({ ref: SemanticRefSchema, proofs: z.array(proof) })),
    declarations: z.array(z.object({ ref: SemanticRefSchema, record })),
    rootCount: z.number(),
    applicability: z.enum(['current', 'review_needed']),
    interpretation: z.string(),
  })
  .openapi('SemanticAdministrationItem')
const json = <T extends z.ZodType>(schema: T) => ({
  description: 'Response',
  content: { 'application/json': { schema } },
})
const project = (view: SemanticView) =>
  item.parse({
    ...view.candidate,
    declarations: view.declarations,
    rootCount: view.roots.length,
    applicability: view.applicability,
    interpretation: view.interpretation,
  })
/** Install after authenticated administrator and recovery readiness middleware. */
export function installSemanticRoutes(
  app: OpenAPIHono,
  { semantic, store }: { semantic: SemanticService; store: Store },
) {
  const valid = (sceneId: string) => ['group', 'private'].includes(store.scene(sceneId)?.kind ?? '')
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-semantics',
      request: {
        query: z
          .object({ sceneId: id, limit: z.coerce.number().int().min(1).max(40).optional() })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            enabled: z.boolean(),
            coverage: z.enum(['complete', 'limited']),
            items: z.array(item),
            unavailable: z.array(
              z.object({
                id,
                version: z.number(),
                status: z.enum(['candidate', 'admitted', 'rejected']),
                reason: z.literal('qualification_unavailable'),
              }),
            ),
          }),
        ),
        401: json(ErrorSchema),
        404: json(ErrorSchema),
      },
    }),
    (c) => {
      const { sceneId, limit } = c.req.valid('query')
      if (!valid(sceneId)) return c.json({ error: 'semantic_scene_unavailable' }, 404)
      const page = semantic.adminList({ sceneId, simulation: false }, limit)
      return c.json(
        {
          enabled: store.settings.memorySemanticEnabled,
          coverage: page.coverage,
          items: page.items.map(project),
          unavailable: page.unavailable,
        },
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-semantics/{id}/review',
      request: {
        params: z.object({ id }),
        body: {
          required: true,
          content: {
            'application/json': {
              schema: z
                .object({
                  sceneId: id,
                  expectedVersion: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
                  status: z.enum(['admitted', 'rejected']),
                  confirmed: z.literal(true),
                })
                .strict(),
            },
          },
        },
      },
      responses: {
        200: json(
          z.object({
            id,
            version: z.number(),
            status: z.enum(['candidate', 'admitted', 'rejected']),
          }),
        ),
        400: json(ErrorSchema),
        401: json(ErrorSchema),
        404: json(ErrorSchema),
        409: json(ErrorSchema),
      },
    }),
    (c) => {
      const input = c.req.valid('json')
      if (!valid(input.sceneId)) return c.json({ error: 'semantic_scene_unavailable' }, 404)
      try {
        const next = store.db.transaction(() =>
          semantic.review(
            { sceneId: input.sceneId, simulation: false },
            c.req.valid('param').id,
            input.expectedVersion,
            input.status,
          ),
        )()
        return c.json({ id: next.id, version: next.version, status: next.status }, 200)
      } catch {
        return c.json({ error: 'semantic_review_conflict_or_unavailable' }, 409)
      }
    },
  )
}
