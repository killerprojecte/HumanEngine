import { z } from '@hono/zod-openapi'
export const TypingDelayConfigSchema = z
  .object({ speedFactor: z.number().min(0.1).max(10).default(1) })
  .strict()
export const typingDelayConfigSchema = z.toJSONSchema(TypingDelayConfigSchema, { io: 'input' })
export type TypingDelayConfig = z.input<typeof TypingDelayConfigSchema>
