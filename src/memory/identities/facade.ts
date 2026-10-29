import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import type { Store } from '../../store.ts'
import type { MemoryContext, MemoryRecord } from '../domain/contracts.ts'
import { domainSourceCurrent } from '../read/domain-source-events.ts'
import { recordIdentityMutation } from '../recovery/identities.ts'
import {
  AliasSchema,
  type IdentityAlias,
  type IdentityCommand,
  type IdentityEndpoint,
  type IdentityScope,
} from './contracts.ts'
import { IdentityRegistry } from './service.ts'

type IdentityStore = Pick<
  Store,
  'db' | 'scene' | 'recalled' | 'sourceEvents' | 'assertRecoveryReady'
>
interface IdentityMemory {
  list(ctx: MemoryContext): MemoryRecord[]
}
/** This boundary does not authorize new people. Its scope is rebuilt from currently authorized
 * original-person records on every call; linked identity never rewrites a source or grants access. */
export class IdentityFacade {
  readonly registry: IdentityRegistry
  constructor(
    private readonly store: IdentityStore,
    private readonly memory: IdentityMemory,
  ) {
    this.registry = new IdentityRegistry(store.db, {
      assertReady: () => store.assertRecoveryReady(),
      beforeWrite: (mutation) => recordIdentityMutation(store.db, mutation),
      authorizeObservation: (alias) =>
        this.admittingObservation !== undefined &&
        isDeepStrictEqual(alias, this.admittingObservation),
      sourceAvailable: (source, endpoint) => {
        // A newly enrolled head cannot retroactively authorize an unbound old observation.
        if (!source.eventRef && store.sourceEvents.registered(source.sceneId, source.messageId))
          return false
        const scene = store.scene(source.sceneId)
        return (
          !!scene &&
          scene.adapterId === endpoint.adapterId &&
          scene.accountId === endpoint.accountId &&
          source.senderId === endpoint.personId &&
          domainSourceCurrent(store, source)
        )
      },
    })
  }
  private admittingObservation?: IdentityAlias
  sceneScope(ctx: MemoryContext): IdentityScope {
    this.store.assertRecoveryReady()
    const scene = this.store.scene(ctx.sceneId)
    if (!scene) throw new Error('identity_scene_missing')
    const allowedPersonIds = ctx.simulation
      ? []
      : [
          ...new Set(
            this.memory
              .list(ctx)
              .flatMap((record) =>
                !record.internalUse &&
                record.scope.kind === 'scene' &&
                record.scope.sceneId === ctx.sceneId &&
                record.scope.personId
                  ? [record.scope.personId]
                  : [],
              ),
          ),
        ]
    return {
      sceneId: ctx.sceneId,
      adapterId: scene.adapterId,
      accountId: scene.accountId,
      allowedPersonIds,
      simulation: ctx.simulation,
    }
  }
  query(ctx: MemoryContext, personId: string) {
    const scope = this.sceneScope(ctx)
    const snapshot = this.registry.resolveMembers(scope, personId)
    return {
      snapshot,
      aliases: snapshot.members.map((endpoint) => ({
        endpoint,
        values: this.registry.aliases(scope, endpoint.personId),
      })),
    }
  }
  validate(ctx: MemoryContext, delivered: ReturnType<IdentityFacade['query']>): boolean {
    return isDeepStrictEqual(delivered, this.query(ctx, delivered.snapshot.anchor.personId))
  }
  adminList(adminConfirmed: true) {
    return this.registry.adminList(adminConfirmed)
  }
  witness() {
    return this.registry.witness()
  }
  adminLink(command: IdentityCommand, left: IdentityEndpoint, right: IdentityEndpoint) {
    return this.registry.link(command, left, right)
  }
  adminUnlink(command: IdentityCommand, left: IdentityEndpoint, right: IdentityEndpoint) {
    return this.registry.unlink(command, left, right)
  }
  adminSplit(command: IdentityCommand, partition: readonly IdentityEndpoint[]) {
    return this.registry.split(command, partition)
  }
  putManualAlias(
    command: IdentityCommand,
    input: Omit<IdentityAlias, 'source'>,
    administratorId: string,
  ) {
    this.store.assertRecoveryReady()
    const scene = this.store.scene(input.scopeSceneId)
    if (
      !scene ||
      scene.adapterId !== input.endpoint.adapterId ||
      scene.accountId !== input.endpoint.accountId
    )
      throw new Error('identity_alias_scope_mismatch')
    return this.registry.putAlias(command, {
      ...input,
      source: { kind: 'manual', administratorId },
    })
  }
  removeAlias(command: IdentityCommand, id: string) {
    return this.registry.removeAlias(command, id)
  }
  /** Server-only adapter observation. Never expose this method as a model argument route.
   * The label must come from the trusted adapter observation, not inferred identity text. */
  observeAlias(ctx: MemoryContext, command: IdentityCommand, input: IdentityAlias) {
    this.store.assertRecoveryReady()
    if (ctx.simulation || command.simulation) throw new Error('identity_simulation_write')
    const alias = AliasSchema.parse(input)
    if (alias.source.kind !== 'observation' || alias.scopeSceneId !== ctx.sceneId)
      throw new Error('identity_observation_untrusted')
    const source = alias.source
    const evidence = ctx.sourceEvidence?.find(
      (item) =>
        item.sceneId === source.sceneId &&
        item.messageId === source.messageId &&
        item.senderId === source.senderId &&
        item.available &&
        !item.isBot &&
        isDeepStrictEqual(item.eventRef, source.eventRef),
    )
    if (!evidence || source.senderId !== alias.endpoint.personId)
      throw new Error('identity_observation_untrusted')
    if (this.admittingObservation) throw new Error('identity_observation_reentrant')
    this.admittingObservation = alias
    try {
      return this.registry.putAlias(command, alias)
    } finally {
      this.admittingObservation = undefined
    }
  }
  /** Called after the root source intent, inside the same existing transaction. No replay learning. */
  sourceRecalled(sceneId: string, messageId: string) {
    this.store.assertRecoveryReady()
    if (!this.store.db.inTransaction) throw new Error('identity_transaction_required')
    const current = this.registry.adminList(true)
    if (
      !current.aliases.some(
        (alias) =>
          alias.source.kind === 'observation' &&
          alias.source.sceneId === sceneId &&
          alias.source.messageId === messageId,
      )
    )
      return
    const operationId =
      'recall-' +
      createHash('sha256')
        .update(JSON.stringify([sceneId, messageId, current.revision]))
        .digest('hex')
    return this.registry.recallSource(
      { operationId, expectedRevision: current.revision, adminConfirmed: true },
      sceneId,
      messageId,
    )
  }
}
export function createIdentityFacade(store: IdentityStore, memory: IdentityMemory): IdentityFacade {
  return new IdentityFacade(store, memory)
}
