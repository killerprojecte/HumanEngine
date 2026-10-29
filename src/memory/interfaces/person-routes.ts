import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { MemoryService } from '../../memory.ts'
import type { Store } from '../../store.ts'
import { ErrorSchema } from '../../schema.ts'
import { displayName } from '../../plugins/builtin/snowluma/profiles.ts'

const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const identifier = z.string().min(1).max(2048)
const query = z.object({ sceneId: identifier }).strict()
const person = z
  .object({
    personId: identifier,
    displayName: z.string().optional(),
    recordCount: z.number().int().nonnegative(),
  })
  .openapi('MemoryPerson')
const source = z.object({ sceneId: z.string(), messageId: z.string(), senderId: z.string() })
const subjectEvidence = z.object({
  observedAt: z.string().optional(),
  coverage: z.enum(['complete', 'limited']),
  witnesses: z
    .array(
      z.object({
        kind: z.enum(['self', 'mentioned', 'legacy_sender', 'unresolved']),
        personId: z.string().optional(),
        subjectDisplayName: z.string().optional(),
        sourceMessageId: z.string().optional(),
        mentionPartIndex: z.number().int().nonnegative().optional(),
        sources: z.array(source.extend({ senderDisplayName: z.string().optional() })),
        observations: z.array(
          source.extend({ sentAt: z.string().optional(), receivedAt: z.string().optional() }),
        ),
      }),
    )
    .max(40),
})
const status = z.enum(['active', 'disputed', 'superseded', 'invalidated'])
const origin = z.enum(['manual', 'learned'])
const record = z
  .object({
    id: z.string(),
    scope: z.object({ kind: z.literal('scene'), sceneId: z.string(), personId: z.string() }),
    kind: z.enum(['fact', 'claim', 'term', 'experience', 'summary']),
    content: z.string(),
    sources: z.array(source),
    importance: z.enum(['low', 'medium', 'high']),
    confidence: z.number(),
    learnedAt: z.string(),
    observedAt: z.string().optional(),
    subjectEvidence: subjectEvidence.optional(),
    validFrom: z.string().optional(),
    epistemicKind: z
      .enum(['explicit', 'reported', 'observed', 'inferred', 'hypothesis'])
      .optional(),
    occurredAt: z.string().optional(),
    validUntil: z.string().optional(),
    status,
    origin,
    locked: z.boolean(),
    supersedesId: z.string().optional(),
    lineageStatus: z.enum(['verified', 'unverified', 'quarantined']).optional(),
  })
  .openapi('PersonMemoryRecord')
const profile = z
  .object({
    sceneId: identifier,
    personId: identifier,
    displayName: z.string().optional(),
    records: z.array(record),
    subjectEvidenceCoverage: z.enum(['complete', 'limited']),
    timeline: z.array(
      z.object({
        memoryId: z.string(),
        recordedAt: z.string(),
        observedAt: z.string().optional(),
        validFrom: z.string().optional(),
        occurredAt: z.string().optional(),
        validUntil: z.string().optional(),
        status,
        origin,
        sourceCount: z.number().int().nonnegative(),
      }),
    ),
  })
  .openapi('PersonMemoryProfile')
const errors = {
  400: json(ErrorSchema, 'Invalid request'),
  401: json(ErrorSchema, 'Authentication required'),
  404: json(ErrorSchema, 'Scene unavailable'),
}

/** Install only on the authenticated administration application, after /api/* middleware. */
export function installPersonMemoryRoutes(
  app: OpenAPIHono,
  { memory, store }: { memory: MemoryService; store: Store },
) {
  const name = (sceneId: string, personId: string) =>
    displayName(
      store.get<{ senderDisplayName?: string }>(`identity:${sceneId}:${personId}`, {})
        .senderDisplayName,
    )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-people',
      request: { query },
      responses: {
        200: json(z.object({ sceneId: identifier, people: z.array(person) })),
        ...errors,
      },
    }),
    (c) => {
      const { sceneId } = c.req.valid('query')
      const scene = store.scene(sceneId)
      if (!scene || !['group', 'private'].includes(scene.kind))
        return c.json({ error: 'Scene unavailable' }, 404)
      const people = memory.people({ sceneId, simulation: false }).map((item) => ({
        ...item,
        ...(name(sceneId, item.personId) ? { displayName: name(sceneId, item.personId) } : {}),
      }))
      return c.json({ sceneId, people }, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-people/profile',
      request: { query: query.extend({ personId: identifier }).strict() },
      responses: { 200: json(profile), ...errors },
    }),
    (c) => {
      const { sceneId, personId } = c.req.valid('query')
      const scene = store.scene(sceneId)
      if (!scene || !['group', 'private'].includes(scene.kind))
        return c.json({ error: 'Scene unavailable' }, 404)
      const value = memory.personProfile({ sceneId, simulation: false }, personId)
      const evidenceBatch = memory.subjectEvidenceBatch(
        { sceneId, simulation: false },
        value.records.map((record) => record.id),
      )
      // Parsing keeps the HTTP view limited to this contract, even if domain records grow.
      return c.json(
        profile.parse({
          ...value,
          subjectEvidenceCoverage: evidenceBatch.coverage,
          records: value.records.map((record) => {
            const evidence = evidenceBatch.items.get(record.id)
            return {
              ...record,
              subjectEvidence: evidence
                ? {
                    ...evidence,
                    witnesses: evidence.witnesses.map((witness) => ({
                      ...witness,
                      subjectDisplayName: witness.personId
                        ? name(sceneId, witness.personId)
                        : undefined,
                      sources: witness.sources.map((source) => ({
                        ...source,
                        senderDisplayName:
                          source.sceneId === sceneId ? name(sceneId, source.senderId) : undefined,
                      })),
                    })),
                  }
                : undefined,
            }
          }),
          displayName: value.records.length ? name(sceneId, personId) : undefined,
        }),
        200,
      )
    },
  )
}
