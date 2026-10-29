/** Model-facing permission, not a blanket storage/admin/learning gate. */
export interface ModelReadScene {
  id: string
  memoryReadEnabled?: boolean
  memoryReadRevision?: number
  personalizationEnabled?: boolean
}
export interface ModelReadTicket {
  readonly sceneId: string
  readonly revision: number
  readonly personalizationEnabled: boolean
}
export interface ModelReadContext {
  scene: { id: string }
  mode: 'real' | 'simulation'
  signal: AbortSignal
  dependencies: Set<string>
  validations: Array<() => boolean>
}
export class ModelReadDenied extends Error {
  constructor(readonly code: 'memory_read_disabled' | 'memory_read_revoked') {
    super(code)
    this.name = 'ModelReadDenied'
  }
}
/** A ticket is owned by this policy instance. Copying fields cannot renew old permission. */
export class ModelReadPolicy {
  private readonly issued = new WeakSet<ModelReadTicket>()
  private readonly validators = new WeakMap<Array<() => boolean>, Set<ModelReadTicket>>()
  private readonly turns = new WeakMap<Set<string>, ModelReadTicket>()
  constructor(private readonly port: { scene(id: string): ModelReadScene | undefined }) {}
  private state(sceneId: string) {
    const scene = this.port.scene(sceneId)
    for (const flag of [scene?.memoryReadEnabled, scene?.personalizationEnabled])
      if (flag !== undefined && typeof flag !== 'boolean')
        throw new ModelReadDenied('memory_read_revoked')
    const revision = scene?.memoryReadRevision ?? 0
    if (!Number.isSafeInteger(revision) || revision < 0)
      throw new ModelReadDenied('memory_read_revoked')
    return {
      enabled: !!scene && scene.memoryReadEnabled !== false,
      revision,
      personalizationEnabled: scene?.personalizationEnabled !== false,
    }
  }
  allowed(sceneId: string): boolean {
    return this.state(sceneId).enabled
  }
  personalizationAllowed(sceneId: string): boolean {
    const state = this.state(sceneId)
    return state.enabled && state.personalizationEnabled
  }
  capture(sceneId: string): ModelReadTicket | undefined {
    const state = this.state(sceneId)
    if (!state.enabled) return undefined
    const ticket = Object.freeze({
      sceneId,
      revision: state.revision,
      personalizationEnabled: state.personalizationEnabled,
    })
    this.issued.add(ticket)
    return ticket
  }
  require(sceneId: string): ModelReadTicket {
    const ticket = this.capture(sceneId)
    if (!ticket) throw new ModelReadDenied('memory_read_disabled')
    return ticket
  }
  valid(ticket: ModelReadTicket): boolean {
    if (!this.issued.has(ticket)) return false
    try {
      const state = this.state(ticket.sceneId)
      return (
        state.enabled &&
        state.revision === ticket.revision &&
        state.personalizationEnabled === ticket.personalizationEnabled
      )
    } catch {
      return false
    }
  }
  assert(ticket: ModelReadTicket): void {
    if (!this.valid(ticket)) throw new ModelReadDenied('memory_read_revoked')
  }
  /** Call BEFORE asynchronous reads/prefetch. Any later denial poisons the original turn. */
  begin(ctx: ModelReadContext): ModelReadTicket {
    if (ctx.signal.aborted) throw new ModelReadDenied('memory_read_revoked')
    if (ctx.mode !== 'real') throw new ModelReadDenied('memory_read_disabled')
    const existing = this.turns.get(ctx.dependencies)
    if (existing && existing.sceneId !== ctx.scene.id)
      throw new ModelReadDenied('memory_read_revoked')
    const ticket = existing ?? this.require(ctx.scene.id)
    this.assert(ticket)
    if (!existing) this.turns.set(ctx.dependencies, ticket)
    const attached = this.validators.get(ctx.validations) ?? new Set<ModelReadTicket>()
    if (!attached.has(ticket)) {
      ctx.validations.push(
        () => !ctx.signal.aborted && ticket.sceneId === ctx.scene.id && this.valid(ticket),
      )
      attached.add(ticket)
      this.validators.set(ctx.validations, attached)
    }
    return ticket
  }
  captureGuard(
    ctx: Pick<ModelReadContext, 'dependencies'>,
  ): { identity: string; valid: () => boolean } | undefined {
    const ticket = this.turns.get(ctx.dependencies)
    if (!ticket) return undefined
    return {
      identity: JSON.stringify([ticket.sceneId, ticket.revision, ticket.personalizationEnabled]),
      valid: () => this.valid(ticket),
    }
  }
  used(ctx: Pick<ModelReadContext, 'dependencies'>): boolean {
    return this.turns.has(ctx.dependencies)
  }
  validContext(ctx: ModelReadContext): boolean {
    const ticket = this.turns.get(ctx.dependencies)
    return (
      !ctx.signal.aborted && (!ticket || (ticket.sceneId === ctx.scene.id && this.valid(ticket)))
    )
  }
}
