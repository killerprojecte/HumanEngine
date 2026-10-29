import { OpenAPIHono, createRoute, z } from '@hono/zod-openapi'
import type { ExperienceService } from '../experience/service.ts'
import type { Store } from '../../store.ts'
import { ErrorSchema } from '../../schema.ts'
import { displayName } from '../../plugins/builtin/snowluma/profiles.ts'
const json = <T extends z.ZodType>(schema: T, description = 'Success') => ({
  description,
  content: { 'application/json': { schema } },
})
const id = z.string().min(1).max(2048)
const status = z.enum(['candidate', 'validated', 'active', 'suspended', 'retired'])
const policyView = z
  .object({
    id,
    version: z.number().int().positive(),
    action: z.enum([
      'exact_person_before_semantic',
      'verify_plan_qualifiers',
      'conclusion_before_detail',
    ]),
    condition: z.discriminatedUnion('kind', [
      z.object({ kind: z.literal('person_question'), personId: id }),
      z.object({ kind: z.literal('plan_question') }),
      z.object({ kind: z.literal('response_style'), personId: id }),
    ]),
    personName: z.string().optional(),
    trialEligible: z.boolean(),
    trial: z
      .object({
        version: z.number().int().positive(),
        policyVersion: z.number().int().positive(),
        evidencePolicyVersion: z.number().int().positive(),
        phase: z.enum(['trial', 'promoted', 'suspended', 'expired']),
        requesterName: z.string().optional(),
        createdAt: z.string(),
        expiresAt: z.string(),
        positiveCount: z.number().int().min(0).max(2),
        instruction: z.object({ id, version: z.number().int().positive() }),
        reason: z
          .enum([
            'explicit_instruction_trial',
            'user_reported_helpfulness',
            'negative_feedback',
            'instruction_withdrawn',
            'expiry',
            'qualification_changed',
          ])
          .optional(),
      })
      .optional(),
    activationBasis: z.enum(['explicit_preference', 'administrator_review']).optional(),
    status,
    createdAt: z.string(),
    expiresAt: z.string(),
    expired: z.boolean(),
    sources: z.array(
      z.object({
        messageId: id,
        senderId: id,
        senderName: z.string().optional(),
        observedAt: z.string(),
        code: z.enum([
          'request_exact_person_lookup',
          'request_verify_plan_qualifiers',
          'request_conclusion_first',
          'counterexample',
          'withdraw_instruction',
          'withdraw_person_instruction',
          'withdraw_plan_instruction',
          'user_reported_helpful',
        ]),
      }),
    ),
    validation: z
      .object({
        tier: z.literal('mechanical_only'),
        checks: z.array(z.string()),
        failures: z.array(z.string()),
        passed: z.boolean(),
      })
      .optional(),
    uses: z.object({
      exposed: z.number().int().nonnegative(),
      applied: z.number().int().nonnegative(),
      declined: z.number().int().nonnegative(),
    }),
  })
  .openapi('MemoryExperienceView')
const errors = {
  400: json(ErrorSchema, 'Invalid input'),
  401: json(ErrorSchema, 'Authentication required'),
  404: json(ErrorSchema, 'Experience or scene unavailable'),
  409: json(ErrorSchema, 'Revision or lifecycle conflict'),
}
/** Register after administration authentication middleware. Inputs never accept validator results. */
export function installExperienceRoutes(
  app: OpenAPIHono,
  { experience, store }: { experience: ExperienceService; store: Store },
) {
  const realScene = (sceneId: string) =>
    ['group', 'private'].includes(store.scene(sceneId)?.kind ?? '')
  const context = (sceneId: string) => ({ sceneId, simulation: false })
  const name = (sceneId: string, personId: string) =>
    displayName(
      store.get<{ senderDisplayName?: string }>(`identity:${sceneId}:${personId}`, {})
        .senderDisplayName,
    )
  app.openapi(
    createRoute({
      method: 'get',
      path: '/api/memory-experience',
      request: {
        query: z
          .object({
            sceneId: id,
            status: status.optional(),
            limit: z.coerce.number().int().min(1).max(40).optional(),
          })
          .strict(),
      },
      responses: {
        200: json(
          z.object({
            enabled: z.boolean(),
            sceneId: id,
            items: z.array(policyView),
            coverage: z.enum(['complete', 'limited']),
          }),
        ),
        ...errors,
      },
    }),
    (c) => {
      const { sceneId, ...options } = c.req.valid('query')
      if (!realScene(sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const result = experience.adminList(context(sceneId), options)
      return c.json(
        {
          sceneId,
          enabled: store.settings.memoryExperienceEnabled,
          ...result,
          items: result.items.map((row) =>
            policyView.parse({
              ...row,
              trial: row.trial
                ? {
                    version: row.trial.version,
                    policyVersion: row.trial.policyVersion,
                    evidencePolicyVersion: row.trial.evidencePolicyVersion,
                    phase: row.trial.phase,
                    requesterName: name(sceneId, row.trial.requesterId),
                    createdAt: row.trial.createdAt,
                    expiresAt: row.trial.expiresAt,
                    positiveCount: row.trial.positives.length,
                    instruction: row.trial.instruction,
                    reason: row.trial.reason,
                  }
                : undefined,
              personName:
                'personId' in row.condition ? name(sceneId, row.condition.personId) : undefined,
              sources: row.sources.map((source) => ({
                messageId: source.source.messageId,
                senderId: source.source.senderId,
                observedAt: source.observedAt,
                code: source.code,
                senderName: name(sceneId, source.source.senderId),
              })),
            }),
          ),
        },
        200,
      )
    },
  )
  const guard = z.object({ sceneId: id, expectedVersion: z.number().int().positive() }).strict()
  const success = z.object({ ok: z.literal(true) })
  const handle = (operation: () => void) => {
    try {
      operation()
      return undefined
    } catch (error) {
      if (!(error instanceof Error)) throw error
      if (error.message === 'experience_unavailable')
        return { status: 404 as const, error: 'Experience unavailable' }
      if (
        [
          'experience_version_conflict',
          'experience_validation_ineligible',
          'experience_validation_failed',
          'experience_activation_ineligible',
          'experience_invalid_transition',
        ].includes(error.message)
      )
        return {
          status: 409 as const,
          error: 'Experience changed or is not eligible; refresh before reviewing',
        }
      throw error
    }
  }
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-experience/{id}/validate',
      request: {
        params: z.object({ id }).strict(),
        body: { content: { 'application/json': { schema: guard } } },
      },
      responses: { 200: json(success), ...errors },
    }),
    (c) => {
      const body = c.req.valid('json')
      if (!realScene(body.sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const failure = handle(() => {
        experience.validate(context(body.sceneId), c.req.valid('param').id, body.expectedVersion)
      })
      if (failure) return c.json({ error: failure.error }, failure.status)
      return c.json({ ok: true as const }, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'post',
      path: '/api/memory-experience/{id}/review',
      request: {
        params: z.object({ id }).strict(),
        body: {
          content: {
            'application/json': {
              schema: guard.extend({ status: z.enum(['active', 'suspended', 'retired']) }).strict(),
            },
          },
        },
      },
      responses: { 200: json(success), ...errors },
    }),
    (c) => {
      const body = c.req.valid('json')
      if (!realScene(body.sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const failure = handle(() => {
        experience.review(
          context(body.sceneId),
          c.req.valid('param').id,
          body.expectedVersion,
          body.status,
        )
      })
      if (failure) return c.json({ error: failure.error }, failure.status)
      return c.json({ ok: true as const }, 200)
    },
  )
  app.openapi(
    createRoute({
      method: 'delete',
      path: '/api/memory-experience/{id}',
      request: {
        params: z.object({ id }).strict(),
        body: { content: { 'application/json': { schema: guard } } },
      },
      responses: { 200: json(success), ...errors },
    }),
    (c) => {
      const body = c.req.valid('json')
      if (!realScene(body.sceneId)) return c.json({ error: 'Scene unavailable' }, 404)
      const failure = handle(() =>
        experience.delete(context(body.sceneId), c.req.valid('param').id, body.expectedVersion),
      )
      if (failure) return c.json({ error: failure.error }, failure.status)
      return c.json({ ok: true as const }, 200)
    },
  )
}
