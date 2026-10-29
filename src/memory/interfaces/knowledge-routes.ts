import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { Store } from '../../store.ts'
import type { KnowledgeService } from '../knowledge/service.ts'
import { KnowledgeRefSchema, type KnowledgeContext } from '../knowledge/contracts.ts'
import { ErrorSchema } from '../../schema.ts'
const id = z.string().min(1).max(512)
const predicates = z.enum([
  'participates_in',
  'prefers_topic',
  'uses_tool',
  'member_of',
  'part_of',
  'depends_on',
  'has_property',
])
const allPredicates = predicates.options
const source = z.object({ sceneId: id, messageId: id, senderId: id })
const proof = z.object({
  sources: z.array(source),
  parents: z.array(id),
  parentReferences: z.array(KnowledgeRefSchema).optional(),
})
const entity = z.object({
  id,
  sceneId: id,
  kind: z.string(),
  title: z.string(),
  backing: KnowledgeRefSchema,
})
const endpoint = z.union([
  z.object({ kind: z.literal('person'), personId: id }),
  z.object({ kind: z.literal('entity'), entity }),
  z.object({
    kind: z.literal('literal'),
    valueType: z.enum(['text', 'number', 'boolean']),
    value: z.union([z.string(), z.number(), z.boolean()]),
    unit: z.string().nullable().optional(),
  }),
])
const edge = z.object({
  id,
  version: z.number(),
  predicate: predicates,
  property: z.string().optional(),
  subject: endpoint,
  object: endpoint,
  polarity: z.enum(['positive', 'negative']),
  modality: z.enum(['asserted', 'possible']),
  temporalScope: z.enum(['current', 'past', 'future', 'unspecified']),
  conditions: z.array(z.string()),
  validFrom: z.string().optional(),
  validUntil: z.string().optional(),
  createdAt: z.string(),
  backing: KnowledgeRefSchema,
  backingStatus: z.string(),
  status: z.enum(['candidate', 'accepted']),
  admission: z.string(),
  applicability: z.string(),
  proofs: z.array(proof),
  dependencies: z.array(z.object({ entity, proofs: z.array(proof) })),
  requiresPersonalization: z.boolean(),
  readDependencies: z.array(
    z.object({
      backing: KnowledgeRefSchema,
      proofs: z.array(proof),
      edgeId: id.optional(),
      edgeVersion: z.number().int().positive().optional(),
    }),
  ),
  dependencyStatuses: z.array(z.object({ entityId: id, status: z.string() })),
  spans: z.array(
    z.object({
      source,
      partIndex: z.number(),
      start: z.number(),
      end: z.number(),
      stance: z.string(),
    }),
  ),
})
const result = z
  .object({
    enabled: z.boolean(),
    edges: z.array(edge),
    supportClosure: z.object({
      edges: z.array(edge),
      records: z.array(
        z.object({
          ref: KnowledgeRefSchema,
          record: z.object({
            id,
            content: z.string(),
            kind: z.string(),
            epistemicKind: z.string().optional(),
            status: z.string(),
            origin: z.string(),
            learnedAt: z.string(),
            observedAt: z.string().optional(),
            occurredAt: z.string().optional(),
            validFrom: z.string().optional(),
            validUntil: z.string().optional(),
            internalUse: z.enum(['commitment_declaration', 'knowledge_declaration']).optional(),
            scope: z.object({ kind: z.string(), sceneId: id.optional(), personId: id.optional() }),
            sources: z.array(source),
          }),
        }),
      ),
    }),
    paths: z.array(z.array(id)),
    coverage: z.enum(['complete', 'limited']),
    reason: z.string().optional(),
    interpretation: z.literal('attributed_claims_not_transitive_truth'),
    sourceBodyCoverage: z.literal('unavailable'),
  })
  .openapi('KnowledgeAdministrationView')
const query = z
  .object({
    sceneId: id,
    personId: id.optional(),
    seedBackingRef: KnowledgeRefSchema.optional(),
    anchor: z.union([
      z.object({ kind: z.literal('person'), personId: id }).strict(),
      z.object({ kind: z.literal('memory'), backingRef: KnowledgeRefSchema }).strict(),
      z
        .object({ kind: z.literal('entity'), entityId: id, backingRef: KnowledgeRefSchema })
        .strict(),
    ]),
    predicates: z.array(predicates).min(1).max(7),
    depth: z.union([z.literal(1), z.literal(2)]),
  })
  .strict()
const json = <T extends z.ZodType>(schema: T) => ({
  description: 'Response',
  content: { 'application/json': { schema } },
})
/** Authenticated administrator read-only query. No candidate review or grant operation. */
export function installKnowledgeRoutes(
  app: OpenAPIHono,
  { knowledge, store }: { knowledge: KnowledgeService; store: Store },
) {
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-knowledge/query',
      request: { body: { required: true, content: { 'application/json': { schema: query } } } },
      responses: {
        200: json(result),
        400: json(ErrorSchema),
        401: json(ErrorSchema),
        404: json(ErrorSchema),
        409: json(ErrorSchema),
      },
    }),
    (c) => {
      const input = c.req.valid('json')
      if (!['group', 'private'].includes(store.scene(input.sceneId)?.kind ?? ''))
        return c.json({ error: 'knowledge_scene_unavailable' }, 404)
      const ctx: KnowledgeContext = {
        sceneId: input.sceneId,
        simulation: false,
        provenance: 'contextual',
      }
      try {
        if (input.anchor.kind === 'person' && input.anchor.personId !== input.personId)
          return c.json({ error: 'knowledge_anchor_mismatch' }, 400)
        if (input.anchor.kind === 'entity') {
          const anchor = input.anchor
          if (!input.seedBackingRef && !input.personId)
            return c.json({ error: 'knowledge_seed_required' }, 400)
          const seed = input.seedBackingRef
            ? knowledge.discover(ctx, input.seedBackingRef, {
                predicates: [...allPredicates],
                depth: 2,
              })
            : knowledge.query(ctx, {
                anchor: { kind: 'person', personId: input.personId! },
                predicates: [...allPredicates],
                depth: 2,
              })
          const found =
            seed.coverage === 'complete' &&
            seed.edges
              .flatMap((e) => [e.subject, e.object])
              .some(
                (e) =>
                  e.kind === 'entity' &&
                  e.entity.id === anchor.entityId &&
                  e.entity.backing.id === anchor.backingRef.id &&
                  e.entity.backing.version === anchor.backingRef.version &&
                  e.entity.backing.deletionEpoch === anchor.backingRef.deletionEpoch,
              )
          if (!found) return c.json({ error: 'knowledge_anchor_unavailable' }, 404)
          ctx.exposedEntities = [{ entityId: anchor.entityId, backingRef: anchor.backingRef }]
        }
        const value =
          input.anchor.kind === 'memory'
            ? knowledge.discover(ctx, input.anchor.backingRef, {
                predicates: input.predicates,
                depth: input.depth,
              })
            : knowledge.query(ctx, {
                anchor: input.anchor,
                predicates: input.predicates,
                depth: input.depth,
              })
        return c.json(
          result.parse({
            ...value,
            enabled: store.settings.memoryKnowledgeEnabled,
            sourceBodyCoverage: 'unavailable',
          }),
          200,
        )
      } catch {
        return c.json({ error: 'knowledge_query_unavailable' }, 409)
      }
    },
  )
}
