import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { Store } from '../../store.ts'
import type { IdentityRegistry } from '../identities/service.ts'
import { AliasSchema, EndpointSchema } from '../identities/contracts.ts'
import { ErrorSchema } from '../../schema.ts'
const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const id = z.string().min(1).max(160)
const guard = {
  operationId: id,
  expectedRevision: z.number().int().min(0).max(Number.MAX_SAFE_INTEGER),
  confirmed: z.literal(true),
}
const command = z.discriminatedUnion('action', [
  z
    .object({ ...guard, action: z.literal('link'), left: EndpointSchema, right: EndpointSchema })
    .strict(),
  z
    .object({ ...guard, action: z.literal('unlink'), left: EndpointSchema, right: EndpointSchema })
    .strict(),
  z
    .object({
      ...guard,
      action: z.literal('split'),
      partition: z.array(EndpointSchema).min(1).max(32),
    })
    .strict(),
  z
    .object({
      ...guard,
      action: z.literal('alias_put'),
      aliasId: id,
      sceneId: id,
      endpoint: EndpointSchema,
      label: z.string().min(1).max(200),
      validFrom: z.string().datetime({ offset: true }).nullable(),
      validUntil: z.string().datetime({ offset: true }).nullable(),
    })
    .strict(),
  z.object({ ...guard, action: z.literal('alias_remove'), aliasId: id, sceneId: id }).strict(),
])
const view = z
  .object({
    revision: z.number(),
    edges: z.array(z.tuple([EndpointSchema, EndpointSchema])),
    aliases: z.array(AliasSchema),
  })
  .openapi('MemoryIdentityAdministration')
/** Register only after authenticated administrator and recovery-readiness middleware. */
export function installIdentityRoutes(
  app: OpenAPIHono,
  { identities, store }: { identities: IdentityRegistry; store: Store },
) {
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-identities',
      responses: { 200: json(view), 401: json(ErrorSchema) },
    }),
    (c) => c.json(view.parse(identities.adminList(true)), 200),
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-identities',
      request: { body: { required: true, content: { 'application/json': { schema: command } } } },
      responses: {
        200: json(z.object({ revision: z.number(), duplicate: z.boolean() })),
        400: json(ErrorSchema),
        401: json(ErrorSchema),
        404: json(ErrorSchema),
        409: json(ErrorSchema),
      },
    }),
    (c) => {
      const input = c.req.valid('json')
      if (
        'sceneId' in input &&
        !['group', 'private'].includes(store.scene(input.sceneId)?.kind ?? '')
      )
        return c.json({ error: 'identity_scene_unavailable' }, 404)
      if (
        input.action === 'alias_put' &&
        (store.scene(input.sceneId)?.adapterId !== input.endpoint.adapterId ||
          store.scene(input.sceneId)?.accountId !== input.endpoint.accountId)
      )
        return c.json({ error: 'identity_namespace_mismatch' }, 400)
      if (
        input.action === 'alias_remove' &&
        identities
          .adminList(true)
          .aliases.some((a) => a.id === input.aliasId && a.scopeSceneId !== input.sceneId)
      )
        return c.json({ error: 'identity_alias_unavailable' }, 404)
      const trusted = {
        operationId: input.operationId,
        expectedRevision: input.expectedRevision,
        adminConfirmed: true as const,
      }
      try {
        const result = store.db.transaction(() => {
          switch (input.action) {
            case 'link':
              return identities.link(trusted, input.left, input.right)
            case 'unlink':
              return identities.unlink(trusted, input.left, input.right)
            case 'split':
              return identities.split(trusted, input.partition)
            case 'alias_remove':
              return identities.removeAlias(trusted, input.aliasId)
            case 'alias_put':
              return identities.putAlias(trusted, {
                id: input.aliasId,
                endpoint: input.endpoint,
                label: input.label,
                scopeSceneId: input.sceneId,
                validFrom: input.validFrom,
                validUntil: input.validUntil,
                source: { kind: 'manual', administratorId: 'authenticated-admin' },
              })
          }
        })()
        return c.json(result, 200)
      } catch {
        return c.json({ error: 'identity_change_rejected_refresh_required' }, 409)
      }
    },
  )
}
