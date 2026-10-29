import { isDeepStrictEqual } from 'node:util'
import type { MemoryContext, MemoryRecord } from '../domain/contracts.ts'
import type { ProofParentReference } from '../domain/proofs.ts'

export interface ModelMemoryContext {
  scene: { id: string }
  mode: 'real' | 'simulation'
  signal: AbortSignal
  dependencies: Set<string>
  validations: Array<() => boolean>
  deferMemoryExposure?: boolean
}
export interface ModelMemorySnapshot extends ProofParentReference {
  deleted: boolean
}
interface MemoryExposurePort {
  list(context: MemoryContext): MemoryRecord[]
  captureParentReference(
    context: MemoryContext,
    record: MemoryRecord,
  ): ProofParentReference | undefined
  captureExposedReferences?(
    context: MemoryContext,
    records: readonly MemoryRecord[],
  ): readonly ProofParentReference[]
  captureParentReferences?(
    context: MemoryContext,
    records: readonly MemoryRecord[],
  ): readonly ProofParentReference[]
  revisionState(id: string): { version: number; deletionEpoch: number; deleted: boolean }
  parentBinding(id: string): MemoryRecord['parentBinding']
}
interface PreparedMemory {
  authority: MemoryRecord
  body: MemoryRecord
  reference?: ModelMemorySnapshot
  binding?: MemoryRecord['parentBinding']
}
function freeze<T>(value: T): T {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) freeze(child)
    Object.freeze(value)
  }
  return value
}
const scope = (context: ModelMemoryContext): MemoryContext => ({
  sceneId: context.scene.id,
  simulation: context.mode === 'simulation',
})

/** Receipt preparation is not exposure. Only selected, unchanged objects committed
 * at the model-delivery boundary contribute cancellation and learning dependencies.
 */
export class ModelMemoryExposure {
  private readonly pending = new WeakMap<ModelMemoryContext, WeakMap<object, PreparedMemory>>()
  private readonly delivered = new WeakMap<ModelMemoryContext, Map<string, PreparedMemory>>()
  private readonly exposed = new WeakMap<ModelMemoryContext, Map<string, ModelMemorySnapshot>>()
  constructor(
    private readonly memory: MemoryExposurePort,
    private readonly onDelivered?: (
      context: ModelMemoryContext,
      refs: readonly ProofParentReference[],
    ) => void,
  ) {}

  prepare(context: ModelMemoryContext, records: readonly MemoryRecord[]): MemoryRecord[] {
    if (context.signal.aborted) return []
    const bodies = new Map<string, Omit<MemoryRecord, 'score'>>()
    for (const record of records) {
      const { score: _score, ...body } = record
      const prior = bodies.get(record.id)
      if (prior && !isDeepStrictEqual(prior, body)) return []
      bodies.set(record.id, body)
    }
    // A production batch capture validates each immutable body and captures its exact
    // revision in ONE synchronous authorized read. No map survives this stage.
    const captureBatch =
      this.memory.captureExposedReferences ??
      (records.length <= 40 ? this.memory.captureParentReferences : undefined)
    const batch =
      context.mode === 'real' && captureBatch
        ? captureBatch.call(this.memory, scope(context), records)
        : undefined
    const captured = batch ? new Map(batch.map((ref) => [ref.id, ref])) : undefined
    const current = captured
      ? undefined
      : new Map(this.memory.list(scope(context)).map((record) => [record.id, record]))
    const pending = this.pending.get(context) ?? new WeakMap<object, PreparedMemory>()
    this.pending.set(context, pending)
    const prepared: MemoryRecord[] = []
    for (const record of records) {
      const { score: _score, ...authority } = record
      if (current && !isDeepStrictEqual(current.get(record.id), authority)) continue
      const reference =
        context.mode === 'simulation'
          ? undefined
          : captured
            ? captured.get(record.id)
            : this.memory.captureParentReference(scope(context), authority)
      if (context.mode !== 'simulation' && !reference) continue
      const binding =
        context.mode === 'simulation' ? undefined : this.memory.parentBinding(record.id)
      const body = freeze(structuredClone(binding ? { ...record, parentBinding: binding } : record))
      pending.set(body, {
        authority: freeze(structuredClone(authority)),
        body,
        reference: reference ? freeze({ ...reference, deleted: false }) : undefined,
        binding,
      })
      prepared.push(body)
    }
    return prepared
  }

  commit(context: ModelMemoryContext, selected: readonly unknown[]): MemoryRecord[] {
    if (context.signal.aborted) return []
    const pending = this.pending.get(context)
    if (!pending) return []
    const exposed = this.exposed.get(context) ?? new Map<string, ModelMemorySnapshot>()
    const accepted: PreparedMemory[] = []
    const current = new Map(this.memory.list(scope(context)).map((record) => [record.id, record]))
    // Validate the entire selected receipt before mutating turn exposure. A stale
    // sibling must not partially commit a receipt which the caller cannot deliver.
    for (const object of selected) {
      if (!object || typeof object !== 'object') return []
      const prepared = pending.get(object)
      if (
        !prepared ||
        !isDeepStrictEqual(object, prepared.body) ||
        !this.valid(context, prepared, current)
      )
        return []
      if (context.mode !== 'simulation') {
        const snapshot = prepared.reference!
        const previous = exposed.get(snapshot.id)
        if (previous && !isDeepStrictEqual(previous, snapshot)) {
          context.validations.push(() => false)
          return []
        }
      }
      accepted.push(prepared)
    }
    if (context.mode === 'real') {
      try {
        this.onDelivered?.(
          context,
          accepted.map((prepared) => prepared.reference!),
        )
      } catch (error) {
        context.validations.push(() => false)
        throw error
      }
    }
    if (context.mode === 'real' && this.onDelivered) {
      // A callback is another synchronous mutation boundary. Re-authorize after it;
      // no callback-era source/version change may register stale exposure.
      const current = new Map(this.memory.list(scope(context)).map((record) => [record.id, record]))
      if (!accepted.every((prepared) => this.valid(context, prepared, current))) {
        context.validations.push(() => false)
        return []
      }
    }
    if (context.mode !== 'simulation')
      for (const prepared of accepted) {
        const snapshot = prepared.reference!
        if (!exposed.has(snapshot.id)) {
          exposed.set(snapshot.id, snapshot)
          this.exposed.set(context, exposed)
          for (const source of prepared.authority.sources)
            if (source.sceneId === context.scene.id) context.dependencies.add(source.messageId)
          let delivered = this.delivered.get(context)
          if (!delivered) {
            delivered = new Map<string, PreparedMemory>()
            this.delivered.set(context, delivered)
            // One fresh authorization map for all successfully delivered records;
            // never a cached permission or a submit-time revision upgrade.
            context.validations.push(() => {
              if (context.signal.aborted) return false
              const current = new Map(
                this.memory.list(scope(context)).map((record) => [record.id, record]),
              )
              return [...(this.delivered.get(context)?.values() ?? [])].every((item) =>
                this.valid(context, item, current),
              )
            })
          }
          delivered.set(snapshot.id, prepared)
        }
      }
    return accepted.map((prepared) => prepared.body)
  }

  readExposed(context: ModelMemoryContext): ModelMemorySnapshot[] {
    return [...(this.exposed.get(context)?.values() ?? [])].map((snapshot) => ({ ...snapshot }))
  }

  /** Detached, exact first-delivered body/ref guard for typed downstream evidence. */
  captureExactGuard(context: ModelMemoryContext, id: string): (() => boolean) | undefined {
    const delivered = this.delivered.get(context)?.get(id)
    if (!delivered?.reference || context.mode !== 'real') return undefined
    const prepared = freeze(structuredClone(delivered))
    const detached: ModelMemoryContext = {
      scene: { id: context.scene.id },
      mode: 'real',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    return () => {
      try {
        const current = new Map(
          this.memory.list(scope(detached)).map((record) => [record.id, record]),
        )
        return this.valid(detached, prepared, current)
      } catch {
        return false
      }
    }
  }

  private valid(
    context: ModelMemoryContext,
    prepared: PreparedMemory,
    authorized: ReadonlyMap<string, MemoryRecord>,
  ): boolean {
    if (context.signal.aborted) return false
    const current = authorized.get(prepared.authority.id)
    if (!isDeepStrictEqual(current, prepared.authority)) return false
    if (context.mode === 'simulation') return true
    const state = this.memory.revisionState(prepared.authority.id)
    return (
      !!prepared.reference &&
      !state.deleted &&
      state.version === prepared.reference.version &&
      state.deletionEpoch === prepared.reference.deletionEpoch &&
      this.memory.parentBinding(prepared.authority.id) === prepared.binding
    )
  }
}
