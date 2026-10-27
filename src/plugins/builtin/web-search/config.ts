import { z } from '@hono/zod-openapi'
import { providerIds } from './types.ts'
const key = z
  .string()
  .max(16384)
  .refine((v) => v === '' || v.trim().length > 0)
  .optional()
  .meta({ secret: true })
export const WebSearchConfigSchema = z
  .object({
    provider: z.enum(providerIds).default('bing'),
    fallbackProvider: z.enum(['none', ...providerIds]).default('none'),
    allowExperimentalHtml: z
      .boolean()
      .default(false)
      .describe(
        'Opt in to best-effort public HTML search. Markup, availability and terms vary; this is not a licensed search API or DDGS.',
      ),
    maxResults: z.number().int().min(1).max(10).default(5),
    timeoutMs: z.number().int().min(500).max(30000).default(10000),
    maxResponseBytes: z.number().int().min(4096).max(1048576).default(262144),
    maxConcurrency: z.number().int().min(1).max(4).default(2),
    tavilyKey: key,
    clearTavilyKey: z.boolean().optional(),
    youKey: key,
    clearYouKey: z.boolean().optional(),
    deepseekKey: key,
    clearDeepseekKey: z.boolean().optional(),
  })
  .strict()
  .superRefine((value, ctx) => {
    for (const [secret, clear] of secretFields)
      if (value[secret] && value[clear])
        ctx.addIssue({ code: 'custom', message: 'conflicting_credential_change', path: [secret] })
  })
export const secretFields = [
  ['tavilyKey', 'clearTavilyKey'],
  ['youKey', 'clearYouKey'],
  ['deepseekKey', 'clearDeepseekKey'],
] as const
export type WebSearchConfig = z.infer<typeof WebSearchConfigSchema>
// The generic plugin form intentionally supports a small JSON Schema vocabulary.
export const webSearchConfigSchema = z.toJSONSchema(WebSearchConfigSchema, { io: 'input' })
