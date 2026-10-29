import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { MemoryService } from '../../memory.ts'
import type { Store } from '../../store.ts'
import type { RelationRecord } from '../domain/relations.ts'
import { ErrorSchema } from '../../schema.ts'
import { displayName } from '../../plugins/builtin/snowluma/profiles.ts'
const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const id = z.string().min(1).max(2048)
const status = z.enum(['candidate', 'accepted', 'rejected'])
const view = z
  .object({
    id,
    version: z.number().int().positive(),
    assertionContent: z.string(),
    personId: id,
    personName: z.string().optional(),
    predicate: z.literal('participates_in'),
    target: z.object({ id, kind: z.enum(['plan', 'event']), title: z.string() }),
    polarity: z.enum(['positive', 'negative']),
    modality: z.enum(['asserted', 'possible']),
    temporalScope: z.enum(['current', 'past', 'future', 'unspecified']),
    conditions: z.array(z.string()),
    occurredAt: z.string().optional(),
    validFrom: z.string().optional(),
    validUntil: z.string().optional(),
    epistemicKind: z
      .enum(['explicit', 'reported', 'observed', 'inferred', 'hypothesis'])
      .optional(),
    status,
    admission: z.enum(['automatic', 'candidate', 'reviewed']),
    autoReason: z.literal('self_explicit_no_visible_conflict').optional(),
    createdAt: z.string(),
    reviewedAt: z.string().optional(),
    sourceGroups: z.array(
      z.object({
        witnessKind: z.enum(['self', 'mentioned', 'legacy_sender', 'unresolved']).optional(),
        sources: z.array(
          z.object({ messageId: id, senderId: id, senderName: z.string().optional() }),
        ),
        parentCount: z.number().int().nonnegative(),
      }),
    ),
  })
  .openapi('MemoryRelationView')
const errors = {
  400: json(ErrorSchema, 'Invalid request'),
  401: json(ErrorSchema, 'Authentication required'),
  404: json(ErrorSchema, 'Relation or scene unavailable'),
}
/** Mount after the administration application's authentication middleware. */
export function installRelationRoutes(
  app: OpenAPIHono,
  { memory, store }: { memory: MemoryService; store: Store },
) {
  const context = (sceneId: string) => ({
    sceneId,
    simulation: false,
    provenance: 'contextual' as const,
  })
  const available = (sceneId: string) =>
    ['group', 'private'].includes(store.scene(sceneId)?.kind ?? '')
  const name = (sceneId: string, personId: string) =>
    displayName(
      store.get<{ senderDisplayName?: string }>(`identity:${sceneId}:${personId}`, {})
        .senderDisplayName,
    )
  const project = (row: RelationRecord, assertionContent: string) =>
    view.parse({
      ...row,
      assertionContent,
      personName: name(row.sceneId, row.personId),
      sourceGroups: row.proofs.map((proof) => ({
        witnessKind: proof.subjectWitness?.kind,
        sources: proof.sources.map((source) => ({
          messageId: source.messageId,
          senderId: source.senderId,
          senderName:
            source.sceneId === row.sceneId ? name(row.sceneId, source.senderId) : undefined,
        })),
        parentCount: proof.parents.length,
      })),
    })
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-relations',
      request: {
        query: z
          .object({
            sceneId: id,
            personId: id.optional(),
            status: status.optional(),
            limit: z.coerce.number().int().min(1).max(40).optional(),
          })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            sceneId: id,
            enabled: z.boolean(),
            items: z.array(view),
            coverage: z.enum(['complete', 'limited']),
          }),
        ),
        ...errors,
      },
    }),
    (c) => {
      const { sceneId, ...options } = c.req.valid('query')
      if (!available(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const result = memory.relations.list(context(sceneId), options)
      const assertions = new Map(
        memory.list({ sceneId, simulation: false }).map((record) => [record.id, record.content]),
      )
      return c.json(
        {
          sceneId,
          enabled: store.settings.memoryGraphEnabled,
          ...result,
          items: result.items
            .filter((row) => assertions.has(row.backing.id))
            .map((row) => project(row, assertions.get(row.backing.id)!)),
        },
        200,
      )
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-relations/{id}/review',
      request: {
        params: z.object({ id }).strict(),
        body: {
          content: {
            'application/json': {
              schema: z
                .object({
                  sceneId: id,
                  expectedVersion: z.number().int().positive(),
                  status: z.enum(['accepted', 'rejected']),
                })
                .strict(),
            },
          },
        },
      },
      responses: { 200: json(view), ...errors, 409: json(ErrorSchema, 'Revision conflict') },
    }),
    (c) => {
      const body = c.req.valid('json')
      if (!available(body.sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      try {
        const row = store.db.transaction(() =>
          memory.relations.review(
            context(body.sceneId),
            c.req.valid('param').id,
            body.expectedVersion,
            body.status,
            'admin',
          ),
        )()
        const assertion = memory
          .list({ sceneId: body.sceneId, simulation: false })
          .find((record) => record.id === row.backing.id)
        if (!assertion) return c.json({ error: 'Relation unavailable' }, 404)
        return c.json(project(row, assertion.content), 200)
      } catch (error) {
        if (error instanceof Error && error.message === 'relation_version_conflict')
          return c.json({ error: 'Relation changed; refresh before reviewing' }, 409)
        if (error instanceof Error && error.message === 'relation_unavailable')
          return c.json({ error: 'Relation unavailable' }, 404)
        throw error
      }
    },
  )
}
