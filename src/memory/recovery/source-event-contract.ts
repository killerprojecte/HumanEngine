import { z } from 'zod'
const id = z.string().min(1).max(512)
const hash = z.string().regex(/^[a-f0-9]{64}$/)
/** Qualification only: no message text, media locator, credentials, or replayable edit payload. */
export const SourceEditHeadEventSchema = z
  .object({
    kind: z.literal('source_edit_head'),
    schemaVersion: z.literal(1),
    sceneId: id,
    messageId: id,
    eventId: id,
    revision: z.number().int().positive().safe(),
    deletionEpoch: z.number().int().nonnegative().safe(),
    identityHash: hash,
    headHash: hash,
  })
  .strict()
export type SourceEditHeadEvent = z.infer<typeof SourceEditHeadEventSchema>
export function parseSourceEditHeadEvent(value: unknown): SourceEditHeadEvent {
  const result = SourceEditHeadEventSchema.safeParse(value)
  if (!result.success) throw new Error('recovery_source_event_invalid')
  return result.data
}
