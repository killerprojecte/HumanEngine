import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import {
  ExternalCostInputSchema,
  ExternalCostCorrectionSchema,
  ExternalCostReceiptSchema,
} from './contracts.ts'
import type { ExternalCostService } from './service.ts'
const json = <T extends z.ZodType>(schema: T, description: string) => ({
  description,
  content: { 'application/json': { schema } },
})
const error = z.object({ error: z.string() })
const conflicts = new Set([
  'external_cost_conflict',
  'external_cost_revision_conflict',
  'external_cost_capacity',
])
/** Authenticated administrator declarations, not provider invoices or verified payments. */
export function registerExternalCostRoutes(app: OpenAPIHono, costs: ExternalCostService) {
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/budget/external-costs',
      request: {
        query: z
          .object({
            page: z.coerce.number().int().min(1).max(10000).default(1),
            pageSize: z.coerce.number().int().min(1).max(50).default(20),
          })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            items: z.array(ExternalCostReceiptSchema),
            total: z.number().int().nonnegative(),
            page: z.number().int().positive(),
            pageSize: z.number().int().positive(),
          }),
          'Imported administrator-declared receipts; bounded page',
        ),
        401: json(error, 'Authentication required'),
      },
    }),
    (c) => c.json(costs.list(c.req.valid('query')), 200),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/budget/external-costs',
      request: {
        body: {
          required: true,
          content: { 'application/json': { schema: ExternalCostInputSchema } },
        },
      },
      responses: {
        200: json(ExternalCostReceiptSchema, 'Idempotent imported receipt'),
        400: json(error, 'Invalid receipt'),
        409: json(error, 'Exact receipt conflict or capacity'),
        401: json(error, 'Authentication required'),
      },
    }),
    (c) => {
      try {
        return c.json(costs.import(c.req.valid('json')), 200)
      } catch (e) {
        if (e instanceof Error && conflicts.has(e.message)) return c.json({ error: e.message }, 409)
        throw e
      }
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/budget/external-costs/corrections',
      request: {
        body: {
          required: true,
          content: { 'application/json': { schema: ExternalCostCorrectionSchema } },
        },
      },
      responses: {
        200: json(
          ExternalCostReceiptSchema,
          'Exact-version correction; historical revisions retained',
        ),
        400: json(error, 'Invalid correction'),
        409: json(error, 'Receipt or revision conflict'),
        401: json(error, 'Authentication required'),
      },
    }),
    (c) => {
      try {
        return c.json(costs.correct(c.req.valid('json')), 200)
      } catch (e) {
        if (e instanceof Error && conflicts.has(e.message)) return c.json({ error: e.message }, 409)
        throw e
      }
    },
  )
}
