import { z } from 'zod'
import { KnowledgeRefSchema } from './contracts.ts'
export const KnowledgeQuerySchema = z
  .object({
    anchor: z.discriminatedUnion('kind', [
      z.object({ kind: z.literal('person'), personId: z.string().min(1).max(512) }).strict(),
      z
        .object({
          kind: z.literal('entity'),
          entityId: z.string().min(1).max(512),
          backingRef: KnowledgeRefSchema,
        })
        .strict(),
    ]),
    predicates: z
      .array(
        z.enum([
          'participates_in',
          'prefers_topic',
          'uses_tool',
          'member_of',
          'part_of',
          'depends_on',
          'has_property',
        ]),
      )
      .min(1)
      .max(7),
    depth: z.union([z.literal(1), z.literal(2)]),
  })
  .strict()

export const KnowledgeToolQuerySchema = KnowledgeQuerySchema.extend({
  anchor: z.union([
    KnowledgeQuerySchema.shape.anchor,
    z.object({ kind: z.literal('memory'), memoryId: z.string().min(1).max(512) }).strict(),
  ]),
}).strict()
