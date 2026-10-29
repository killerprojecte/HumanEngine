import type { ModelMemoryContext } from './model-exposure.ts'

export interface StateExposurePort<T extends object, Input> {
  query(ctx: ModelMemoryContext, input: Input): readonly T[]
  valid(ctx: ModelMemoryContext, values: readonly T[]): boolean
  sourceIds(values: readonly T[]): readonly string[]
  onDelivered?(ctx: ModelMemoryContext, values: readonly T[]): void
}
interface Group<T> {
  values: readonly T[]
  delivered: boolean
}
const freeze = <T>(value: T): T => {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze)
    Object.freeze(value)
  }
  return value
}

/** Exact prepared state receipts become authority only after whole-group budget delivery. */
export class StateExposure<T extends object, Input> {
  private readonly pending = new WeakMap<ModelMemoryContext, WeakMap<object, Group<T>>>()
  private readonly delivered = new WeakMap<ModelMemoryContext, Group<T>[]>()
  constructor(private readonly port: StateExposurePort<T, Input>) {}
  private valid(ctx: ModelMemoryContext, values: readonly T[]): boolean {
    return !ctx.signal.aborted && ctx.mode === 'real' && this.port.valid(ctx, values)
  }
  query(ctx: ModelMemoryContext, input: Input): readonly T[] {
    if (ctx.signal.aborted || ctx.mode !== 'real') return Object.freeze([])
    const values = freeze(structuredClone([...this.port.query(ctx, input)]))
    if (!values.length || !this.valid(ctx, values)) return Object.freeze([])
    const group: Group<T> = { values, delivered: false }
    const pending = this.pending.get(ctx) ?? new WeakMap<object, Group<T>>()
    for (const value of values) pending.set(value, group)
    this.pending.set(ctx, pending)
    return values
  }
  deliver(ctx: ModelMemoryContext, values: readonly unknown[]): readonly T[] {
    if (!values.length || ctx.signal.aborted || ctx.mode !== 'real') return Object.freeze([])
    const first = values[0]
    const group = first && typeof first === 'object' ? this.pending.get(ctx)?.get(first) : undefined
    if (
      !group ||
      group.values.length !== values.length ||
      group.values.some((value, index) => value !== values[index])
    )
      return Object.freeze([])
    if (!this.valid(ctx, group.values)) return Object.freeze([])
    if (group.delivered) return group.values
    const ids = [...this.port.sourceIds(group.values)]
    if (ids.some((id) => typeof id !== 'string' || !id))
      throw new Error('state_exposure_source_invalid')
    try {
      this.port.onDelivered?.(ctx, group.values)
      if (!this.valid(ctx, group.values)) {
        ctx.validations.push(() => false)
        return Object.freeze([])
      }
    } catch (error) {
      // A callback may have bound another authority before failing. Never allow this turn to proceed.
      ctx.validations.push(() => false)
      throw error
    }
    group.delivered = true
    this.delivered.set(ctx, [...(this.delivered.get(ctx) ?? []), group])
    ids.forEach((id) => ctx.dependencies.add(id))
    ctx.validations.push(() => this.valid(ctx, group.values))
    return group.values
  }
  readDelivered(ctx: ModelMemoryContext): readonly T[] {
    const groups = this.delivered.get(ctx) ?? []
    if (groups.some((group) => !this.valid(ctx, group.values))) return Object.freeze([])
    return Object.freeze(groups.flatMap((group) => group.values))
  }
  captureGuard(ctx: ModelMemoryContext): (() => boolean) | undefined {
    const groups = this.delivered.get(ctx)
    if (!groups?.length) return undefined
    const detached: ModelMemoryContext = {
      scene: { id: ctx.scene.id },
      mode: ctx.mode,
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    const snapshots = freeze(structuredClone(groups.map((group) => group.values)))
    // Do not condition the existence of the guard on current validity: false authority stays false.
    return () => snapshots.every((values) => this.valid(detached, values))
  }
}
