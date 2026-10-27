import { z } from '@hono/zod-openapi'
const environmentName = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[A-Z][A-Z0-9_]*$/)
export const SnowLumaConfigSchema = z
  .object({
    transport: z
      .enum(['websocket', 'http'])
      .default('websocket')
      .describe('Omitted means legacy WebSocket.'),
    url: z.string().min(1).max(2048),
    accountId: z.string().min(1).max(256),
    token: z
      .string()
      .max(16384)
      .refine((value) => value === '' || value.trim().length > 0)
      .optional()
      .meta({ secret: true }),
    clearToken: z.boolean().optional(),
    eventToken: z
      .string()
      .max(16384)
      .refine((value) => value === '' || value.trim().length > 0)
      .optional()
      .meta({ secret: true }),
    clearEventToken: z.boolean().optional(),
    credentialEnv: environmentName.optional(),
    eventHost: z.enum(['127.0.0.1', '::1', '0.0.0.0', '::']).default('127.0.0.1'),
    eventPort: z
      .number()
      .int()
      .min(1)
      .max(65535)
      .optional()
      .describe('Required for HTTP transport.'),
    eventPath: z
      .string()
      .min(1)
      .max(256)
      .regex(/^\/[a-zA-Z0-9/_-]+$/)
      .default('/onebot/events'),
    eventCredentialEnv: environmentName
      .optional()
      .describe(
        'Optional legacy environment reference; prefer the database-backed event signature secret.',
      ),
  })
  .strict()
  .superRefine((value, context) => {
    if ((value.clearToken && value.token) || (value.clearEventToken && value.eventToken))
      context.addIssue({ code: 'custom', message: 'conflicting_credential_change' })
    let url: URL
    try {
      url = new URL(value.url)
    } catch {
      context.addIssue({ code: 'custom', message: 'invalid_transport_url', path: ['url'] })
      return
    }
    const protocols = value.transport === 'http' ? ['http:', 'https:'] : ['ws:', 'wss:']
    if (!protocols.includes(url.protocol) || url.username || url.password || url.search || url.hash)
      context.addIssue({ code: 'custom', message: 'invalid_transport_url', path: ['url'] })
    if (value.eventPath?.includes('//'))
      context.addIssue({ code: 'custom', message: 'invalid_event_path', path: ['eventPath'] })
    if (value.transport === 'http' && !value.eventPort)
      context.addIssue({ code: 'custom', message: 'http_event_configuration_required' })
  })
export const snowLumaConfigSchema = z.toJSONSchema(SnowLumaConfigSchema, { io: 'input' })
