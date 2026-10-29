import { isDeepStrictEqual } from 'node:util'
import type { IdentityAlias, IdentityEndpoint, IdentitySnapshot } from '../identities/contracts.ts'
import type { ModelMemoryContext } from './model-exposure.ts'
import { StateExposure } from './state-exposure.ts'

export interface IdentityView {
  stateKind: 'identity_view'
  page: {
    sceneId: string
    revision: number
    personId: string
    coverage: 'complete'
    interpretation: 'administrator_verified_links_not_read_authority_or_personal_facts'
    items: Array<{
      personId: string
      aliases: Array<{
        label: string
        validFrom: string | null
        validUntil: string | null
        sourceKind: 'manual' | 'observation'
        sourceMessageId?: string
      }>
    }>
  }
}
export interface IdentityExposurePort {
  query(
    ctx: { sceneId: string; simulation: boolean },
    personId: string,
  ): {
    snapshot: IdentitySnapshot
    aliases: Array<{ endpoint: IdentityEndpoint; values: IdentityAlias[] }>
  }
  allowed(ctx: ModelMemoryContext): boolean
}

/** Links never rewrite source identities or become ordinary assertion parent authority. */
export class IdentityExposure {
  private readonly exposure: StateExposure<IdentityView, string>
  constructor(private readonly port: IdentityExposurePort) {
    this.exposure = new StateExposure({
      query: (ctx, personId) => this.current(ctx, personId),
      valid: (ctx, values) => {
        try {
          return (
            values.length === 1 &&
            isDeepStrictEqual(values, this.current(ctx, values[0]!.page.personId))
          )
        } catch {
          return false
        }
      },
      sourceIds: (values) =>
        values.flatMap((value) =>
          value.page.items.flatMap((item) =>
            item.aliases.flatMap((alias) => (alias.sourceMessageId ? [alias.sourceMessageId] : [])),
          ),
        ),
    })
  }
  private current(ctx: ModelMemoryContext, personId: string): IdentityView[] {
    if (!this.port.allowed(ctx) || ctx.mode !== 'real') return []
    const result = this.port.query({ sceneId: ctx.scene.id, simulation: false }, personId)
    if (!result.snapshot.members.some((member) => member.personId === personId)) return []
    if (
      result.snapshot.members.length > 32 ||
      result.aliases.reduce((n, item) => n + item.values.length, 0) > 64
    )
      throw new Error('identity_query_limited')
    return [
      {
        stateKind: 'identity_view',
        page: {
          sceneId: ctx.scene.id,
          revision: result.snapshot.revision,
          personId,
          coverage: 'complete',
          interpretation: 'administrator_verified_links_not_read_authority_or_personal_facts',
          items: result.snapshot.members.map((member) => ({
            personId: member.personId,
            aliases: result.aliases
              .filter((group) => isDeepStrictEqual(group.endpoint, member))
              .flatMap((group) =>
                group.values.map((alias) => ({
                  label: alias.label,
                  validFrom: alias.validFrom,
                  validUntil: alias.validUntil,
                  sourceKind: alias.source.kind,
                  ...(alias.source.kind === 'observation'
                    ? { sourceMessageId: alias.source.messageId }
                    : {}),
                })),
              ),
          })),
        },
      },
    ]
  }
  query(ctx: ModelMemoryContext, personId: string) {
    return this.exposure.query(ctx, personId)
  }
  deliver(ctx: ModelMemoryContext, values: readonly unknown[]) {
    return this.exposure.deliver(ctx, values)
  }
  captureGuard(ctx: ModelMemoryContext) {
    return this.exposure.captureGuard(ctx)
  }
}
