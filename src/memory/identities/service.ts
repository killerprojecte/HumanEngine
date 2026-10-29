import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import type Database from 'better-sqlite3'
import {
  AliasSchema,
  EndpointSchema,
  RegistryStateSchema,
  type IdentityAlias,
  type IdentityCommand,
  type IdentityEndpoint,
  type IdentityMutation,
  type IdentityPorts,
  type IdentityScope,
  type IdentitySnapshot,
  type RegistryState,
} from './contracts.ts'
import { IdentityStorage } from './storage.ts'

export const endpointKey = (value: IdentityEndpoint): string =>
  JSON.stringify([value.adapterId, value.accountId, value.personId])
export const identityQualificationHash = (value: unknown) =>
  createHash('sha256').update(JSON.stringify(value)).digest('hex')
const compare = (a: IdentityEndpoint, b: IdentityEndpoint) =>
  endpointKey(a) < endpointKey(b) ? -1 : endpointKey(a) > endpointKey(b) ? 1 : 0
function component(state: RegistryState, endpoint: IdentityEndpoint): IdentityEndpoint[] {
  const found = new Map([[endpointKey(endpoint), endpoint]])
  let changed = true
  while (changed) {
    changed = false
    for (const [a, b] of state.edges)
      if (found.has(endpointKey(a)) || found.has(endpointKey(b))) {
        for (const item of [a, b])
          if (!found.has(endpointKey(item))) {
            found.set(endpointKey(item), item)
            changed = true
            if (found.size > 32) throw new Error('identity_component_capacity')
          }
      }
  }
  return [...found.values()].sort(compare)
}
export class IdentityRegistry {
  private readonly storage: IdentityStorage
  constructor(
    private readonly db: Database.Database,
    private readonly ports: IdentityPorts,
  ) {
    this.storage = new IdentityStorage(db, () => ports.assertReady())
  }
  private mutate(
    command: IdentityCommand,
    kind: IdentityMutation['kind'],
    payload: unknown,
    apply: (state: RegistryState) => void,
  ) {
    this.ports.assertReady()
    if (command.simulation) throw new Error('identity_simulation_write')
    if (command.adminConfirmed !== true) throw new Error('identity_confirmation_required')
    if (!this.db.inTransaction) throw new Error('identity_transaction_required')
    if (
      !command.operationId ||
      command.operationId.length > 160 ||
      !Number.isSafeInteger(command.expectedRevision) ||
      command.expectedRevision < 0
    )
      throw new Error('identity_command_invalid')
    const state = this.storage.read()
    const digest = identityQualificationHash({
      kind,
      payload,
      expectedRevision: command.expectedRevision,
    })
    const prior = state.operations.find((op) => op.id === command.operationId)
    if (prior) {
      if (prior.hash !== digest) throw new Error('identity_operation_conflict')
      return { revision: prior.revision, duplicate: true }
    }
    if (state.revision !== command.expectedRevision) throw new Error('identity_revision_conflict')
    if (state.operations.length >= 16384 || state.revision >= Number.MAX_SAFE_INTEGER)
      throw new Error('identity_capacity')
    const beforeHash = identityQualificationHash(state)
    const targets: IdentityMutation['targets'] = { endpoints: [], aliasIds: [] }
    if (kind === 'link' || kind === 'unlink' || kind === 'split')
      targets.endpoints = structuredClone(payload as IdentityEndpoint[])
    if (kind === 'alias_put') {
      const alias = payload as IdentityAlias
      targets.endpoints = [structuredClone(alias.endpoint)]
      targets.aliasIds = [alias.id]
      if (alias.source.kind === 'observation')
        targets.source = { sceneId: alias.source.sceneId, messageId: alias.source.messageId }
    }
    if (kind === 'alias_remove') targets.aliasIds = [(payload as { aliasId: string }).aliasId]
    if (kind === 'source_recall') {
      targets.source = payload as { sceneId: string; messageId: string }
      targets.aliasIds = state.aliases
        .filter(
          (a) =>
            a.source.kind === 'observation' &&
            a.source.sceneId === targets.source!.sceneId &&
            a.source.messageId === targets.source!.messageId,
        )
        .map((a) => a.id)
    }
    apply(state)
    const previous = state.revision++
    state.operations.push({ id: command.operationId, hash: digest, revision: state.revision })
    if (state.operations.length + state.edges.length + state.aliases.length > 16384)
      throw new Error('identity_revocation_reserve_capacity')
    RegistryStateSchema.parse(state)
    const mutation = {
      kind,
      operationId: command.operationId,
      expectedRevision: previous,
      nextRevision: state.revision,
      beforeHash,
      targets,
      qualificationHash: identityQualificationHash(state),
    }
    const result: unknown = this.ports.beforeWrite?.(mutation)
    if (result && typeof (result as { then?: unknown }).then === 'function')
      throw new Error('identity_async_hook')
    this.storage.write(state)
    return { revision: state.revision, duplicate: false }
  }
  link(command: IdentityCommand, left: IdentityEndpoint, right: IdentityEndpoint) {
    left = EndpointSchema.parse(left)
    right = EndpointSchema.parse(right)
    const pair = [left, right].sort(compare) as [IdentityEndpoint, IdentityEndpoint]
    return this.mutate(command, 'link', pair, (state) => {
      if (endpointKey(left) === endpointKey(right)) throw new Error('identity_self_link')
      if (!state.edges.some((edge) => isDeepStrictEqual(edge, pair))) {
        if (state.edges.length >= 4096) throw new Error('identity_edge_capacity')
        state.edges.push(pair)
      }
      component(state, left)
    })
  }
  unlink(command: IdentityCommand, left: IdentityEndpoint, right: IdentityEndpoint) {
    const pair = [EndpointSchema.parse(left), EndpointSchema.parse(right)].sort(compare)
    return this.mutate(command, 'unlink', pair, (state) => {
      state.edges = state.edges.filter((edge) => !isDeepStrictEqual(edge, pair))
    })
  }
  /** Explicit cut, unlike unlink: alternate cycle paths cannot silently preserve a requested split. */
  split(command: IdentityCommand, partition: readonly IdentityEndpoint[]) {
    if (!partition.length || partition.length > 32) throw new Error('identity_partition_invalid')
    const selected = partition.map((value) => EndpointSchema.parse(value)).sort(compare)
    const keys = new Set(selected.map(endpointKey))
    if (keys.size !== selected.length) throw new Error('identity_partition_invalid')
    return this.mutate(command, 'split', selected, (state) => {
      const whole = component(state, selected[0]!)
      if (
        selected.some((e) => !whole.some((item) => endpointKey(item) === endpointKey(e))) ||
        selected.length === whole.length
      )
        throw new Error('identity_partition_invalid')
      state.edges = state.edges.filter(
        ([a, b]) => keys.has(endpointKey(a)) === keys.has(endpointKey(b)),
      )
    })
  }
  putAlias(command: IdentityCommand, input: IdentityAlias) {
    const alias = AliasSchema.parse(input)
    if (
      alias.source.kind === 'observation' &&
      (!this.ports.authorizeObservation?.(alias) ||
        this.ports.sourceAvailable?.(alias.source, alias.endpoint) !== true)
    )
      throw new Error('identity_observation_untrusted')
    return this.mutate(command, 'alias_put', alias, (state) => {
      const prior = state.aliases.find((a) => a.id === alias.id)
      if (prior && !isDeepStrictEqual(prior, alias)) throw new Error('identity_alias_immutable')
      if (!prior) {
        if (state.aliases.length >= 4096) throw new Error('identity_alias_capacity')
        state.aliases.push(alias)
      }
    })
  }
  removeAlias(command: IdentityCommand, aliasId: string) {
    return this.mutate(command, 'alias_remove', { aliasId }, (state) => {
      state.aliases = state.aliases.filter((a) => a.id !== aliasId)
    })
  }
  recallSource(command: IdentityCommand, sceneId: string, messageId: string) {
    return this.mutate(command, 'source_recall', { sceneId, messageId }, (state) => {
      state.aliases = state.aliases.filter(
        (a) =>
          a.source.kind !== 'observation' ||
          a.source.sceneId !== sceneId ||
          a.source.messageId !== messageId,
      )
    })
  }
  resolveMembers(scope: IdentityScope, personId: string): IdentitySnapshot {
    const state = this.storage.read()
    const anchor = EndpointSchema.parse({
      adapterId: scope.adapterId,
      accountId: scope.accountId,
      personId,
    })
    const allowed = new Set(scope.allowedPersonIds)
    const members =
      scope.simulation || !allowed.has(personId)
        ? []
        : component(state, anchor).filter(
            (e) =>
              e.adapterId === scope.adapterId &&
              e.accountId === scope.accountId &&
              allowed.has(e.personId),
          )
    return {
      revision: state.revision,
      sceneId: scope.sceneId,
      anchor,
      members: structuredClone(members),
    }
  }
  validateSnapshot(scope: IdentityScope, snapshot: IdentitySnapshot): boolean {
    this.ports.assertReady()
    if (
      snapshot.sceneId !== scope.sceneId ||
      snapshot.anchor.adapterId !== scope.adapterId ||
      snapshot.anchor.accountId !== scope.accountId
    )
      return false
    return isDeepStrictEqual(snapshot, this.resolveMembers(scope, snapshot.anchor.personId))
  }
  aliases(scope: IdentityScope, personId: string): IdentityAlias[] {
    const state = this.storage.read()
    if (scope.simulation || !scope.allowedPersonIds.includes(personId)) return []
    const now = this.ports.now?.() ?? Date.now()
    if (!Number.isSafeInteger(now) || now < 0) throw new Error('identity_clock_invalid')
    return state.aliases
      .filter(
        (a) =>
          a.scopeSceneId === scope.sceneId &&
          a.endpoint.adapterId === scope.adapterId &&
          a.endpoint.accountId === scope.accountId &&
          a.endpoint.personId === personId &&
          (!a.validFrom || Date.parse(a.validFrom) <= now) &&
          (!a.validUntil || now < Date.parse(a.validUntil)) &&
          (a.source.kind === 'manual' ||
            (a.source.sceneId === scope.sceneId &&
              this.ports.sourceAvailable?.(a.source, a.endpoint) === true)),
      )
      .map((a) => structuredClone(a))
  }
  /** Exact whole-state hash for trusted recovery verification; never copies alias bodies into a journal. */
  witness(): { revision: number; qualificationHash: string } {
    const state = this.storage.read()
    return { revision: state.revision, qualificationHash: identityQualificationHash(state) }
  }
  adminList(adminConfirmed: true) {
    if (adminConfirmed !== true) throw new Error('identity_confirmation_required')
    const state = this.storage.read()
    return { revision: state.revision, edges: state.edges, aliases: state.aliases }
  }
}
