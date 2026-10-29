import { randomUUID } from 'node:crypto'
import { withBudgetContext } from '../../budget/native-attempt.ts'
import { withModelPriority } from '../../scheduler.ts'
import type { Router } from '../../models.ts'
import { LearningAdmission, type LearningAdmissionOptions } from '../learning/admission.ts'
import { ModelReadPolicy, type ModelReadTicket } from '../read/model-read-policy.ts'
import { type SemanticProposal, semanticHash } from './contracts.ts'
import {
  captureSemanticInput,
  type SemanticDirty,
  type SemanticInput,
  type SemanticResolvedInput,
} from './input.ts'
import { requestSemanticProposals } from './request.ts'

export interface SemanticCoordinatorPorts {
  readPolicy: ModelReadPolicy
  enabled(sceneId: string): boolean
  /** Monotonic generation/auto-admission/answer policy revision, including OFF→ON changes. */
  policyRevision(): number
  foregroundBusy(): boolean
  timezone(): string
  resolveDirty(
    dirty: readonly SemanticDirty[],
    ticket: ModelReadTicket,
  ): SemanticResolvedInput | undefined
  valid(snapshot: SemanticInput): boolean
  /** Must wire assertCurrent into every physical native attempt, even with accounting OFF. */
  router(assertCurrent: () => void): Pick<Router, 'complete'>
  /** Both metadata methods are owner-wrapped synchronous transactions, no durable runnable job. */
  beginAttempt(snapshot: SemanticInput): boolean
  finishAttempt(snapshot: SemanticInput, status: 'finished' | 'abandoned'): void
  /** Owner executes guard inside the existing assertion/checkpoint transaction immediately before writing. */
  commit(
    snapshot: SemanticInput,
    proposals: readonly SemanticProposal[],
    guard: () => void,
  ): { created: number; rejected: number }
  event?(event: {
    sceneId: string
    status: 'queued' | 'skipped' | 'started' | 'finished' | 'abandoned'
    reason?: string
  }): void
}
interface DirtyTicket {
  dirty: SemanticDirty
  ticket: ModelReadTicket
  policyRevision: number
}
interface Active {
  sceneId: string
  partition: string
  controller: AbortController
  entries: DirtyTicket[]
  snapshot?: SemanticInput
}

/** One ephemeral semantic owner; notifications, not restart scans or recurring model clocks, create work. */
export class SemanticCoordinator {
  private readonly admission: LearningAdmission
  private readonly dirty = new Map<string, Map<string, DirtyTicket>>()
  private active?: Active
  private timer?: ReturnType<typeof setTimeout>
  private disposed = false
  private scheduled = false
  private readonly waiters = new Set<() => void>()
  private readonly counts = { accepted: 0, attempts: 0, finished: 0, abandoned: 0, skipped: 0 }
  constructor(
    private readonly ports: SemanticCoordinatorPorts,
    options: LearningAdmissionOptions & { pollMs?: number } = {},
  ) {
    this.admission = new LearningAdmission({
      quietMs: 30_000,
      maxWaitMs: 120_000,
      minIntervalMs: 60_000,
      maxSourcesPerScene: 128,
      maxSourcesTotal: 1024,
      maxScenes: 128,
      ...options,
    })
    this.pollMs = options.pollMs ?? 1_000
    if (!Number.isSafeInteger(this.pollMs) || this.pollMs < 10)
      throw new Error('semantic_poll_invalid')
  }
  private readonly pollMs: number

  notify(dirty: SemanticDirty): 'accepted' | 'duplicate' | 'disabled' | 'capacity' {
    if (this.disposed || !this.ports.enabled(dirty.sceneId)) return 'disabled'
    let ticket: ModelReadTicket
    try {
      ticket = this.ports.readPolicy.require(dirty.sceneId)
    } catch {
      return 'disabled'
    }
    const partition = JSON.stringify([
      dirty.sceneId,
      dirty.episodeAnchor ? ['episode', dirty.episodeAnchor] : ['person', dirty.personId],
    ])
    const key = semanticHash([dirty.trigger, dirty.reason])
    const accepted = this.admission.admit(partition, key, 'background')
    if (accepted !== 'accepted') return accepted
    const pending = this.dirty.get(partition) ?? new Map<string, DirtyTicket>()
    pending.set(key, {
      dirty: structuredClone(dirty),
      ticket,
      policyRevision: this.ports.policyRevision(),
    })
    this.dirty.set(partition, pending)
    this.counts.accepted++
    this.emit(dirty.sceneId, 'queued')
    this.resume()
    return accepted
  }

  /** Called after flag/learning/permission changes; no historical scan or reenrollment. */
  configure(options?: Omit<LearningAdmissionOptions, 'now'>): void {
    if (options) this.admission.configure(options)
    for (const [, pending] of this.dirty) {
      const sceneId = pending.values().next().value!.dirty.sceneId
      if (
        !this.ports.enabled(sceneId) ||
        [...pending.values()].some((entry) => !this.ticketValid(entry))
      )
        this.cancelScene(sceneId)
    }
    if (
      this.active &&
      (!this.ports.enabled(this.active.sceneId) ||
        this.active.entries.some((entry) => !this.ticketValid(entry)) ||
        (this.active.snapshot && !this.ports.valid(this.active.snapshot)))
    )
      this.active.controller.abort()
    this.resume()
  }
  cancelScene(sceneId: string): void {
    for (const [partition, pending] of this.dirty) {
      if (pending.values().next().value?.dirty.sceneId === sceneId) {
        this.dirty.delete(partition)
        this.admission.cancelScene(partition)
      }
    }
    if (this.active?.sceneId === sceneId) this.admission.cancelScene(this.active.partition)
    if (this.active?.sceneId === sceneId) this.active.controller.abort()
    this.settleIdle()
  }
  dispose(): void {
    this.disposed = true
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
    this.admission.setEnabled(false)
    this.dirty.clear()
    this.active?.controller.abort()
    this.settleIdle()
  }
  stats() {
    return {
      ...this.counts,
      pending: [...this.dirty.values()].reduce((sum, entries) => sum + entries.size, 0),
      running: Number(!!this.active),
    }
  }
  whenIdle(): Promise<void> {
    if (!this.active && !this.dirty.size) return Promise.resolve()
    return new Promise((resolve) => this.waiters.add(resolve))
  }
  resume(): void {
    if (this.disposed || this.active || this.scheduled) return
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
    this.scheduled = true
    queueMicrotask(() => {
      this.scheduled = false
      this.pump()
    })
  }
  private pump(): void {
    if (this.disposed || this.active || !this.dirty.size) {
      this.settleIdle()
      return
    }
    if (this.ports.foregroundBusy()) {
      this.schedule(this.pollMs)
      return
    }
    for (const partition of this.admission.readyScenes()) {
      const batch = this.admission.take(partition, 128)
      if (!batch) continue
      const pending = this.dirty.get(partition)!
      const entries = batch.sourceKeys.flatMap((key) => {
        const entry = pending.get(key)
        pending.delete(key)
        return entry ? [entry] : []
      })
      if (!pending.size) this.dirty.delete(partition)
      const sceneId = entries[0]!.dirty.sceneId
      const active: Active = { sceneId, partition, controller: new AbortController(), entries }
      this.active = active
      void this.run(active, entries).finally(() => {
        this.admission.finish(partition, batch.token)
        this.active = undefined
        this.settleIdle()
        this.resume()
      })
      return
    }
    this.schedule(this.pollMs)
  }
  private async run(active: Active, entries: DirtyTicket[]): Promise<void> {
    let started = false
    const guard = () => {
      if (
        this.disposed ||
        active.controller.signal.aborted ||
        !entries.length ||
        entries.some((entry) => !this.ticketValid(entry)) ||
        (active.snapshot && !this.ports.valid(active.snapshot))
      )
        throw new Error('semantic_authority_changed')
    }
    try {
      guard()
      const resolved = this.ports.resolveDirty(
        entries.map((entry) => entry.dirty),
        entries[0]!.ticket,
      )
      guard()
      if (!resolved) {
        this.skipped(active.sceneId, 'no_complete_input')
        return
      }
      const snapshot = captureSemanticInput(active.sceneId, resolved)
      if (
        !snapshot ||
        (snapshot.assertions.some(
          ({ record }) => record.scope.kind === 'scene' && record.scope.personId !== undefined,
        ) &&
          !entries[0]!.ticket.personalizationEnabled)
      ) {
        this.skipped(active.sceneId, 'no_complete_input')
        return
      }
      const trigger = entries[0]!.dirty
      if (
        trigger.episodeAnchor
          ? snapshot.episodeAnchor?.id !== trigger.episodeAnchor.id ||
            snapshot.episodeAnchor.version !== trigger.episodeAnchor.version
          : snapshot.personId !== trigger.personId || snapshot.episodeOnly
      ) {
        this.skipped(active.sceneId, 'partition_mismatch')
        return
      }
      active.snapshot = snapshot
      guard()
      if (!this.ports.beginAttempt(snapshot)) {
        this.skipped(active.sceneId, 'unchanged_or_capacity')
        return
      }
      started = true
      this.counts.attempts++
      this.emit(active.sceneId, 'started')
      const proposals = await withModelPriority(2, () =>
        withBudgetContext({ turnId: randomUUID(), lane: 'maintenance' }, () =>
          requestSemanticProposals({
            snapshot,
            router: this.ports.router(guard),
            signal: active.controller.signal,
            assertCurrent: guard,
            timezone: this.ports.timezone(),
          }),
        ),
      )
      guard()
      if (proposals.length) this.ports.commit(snapshot, proposals, guard)
      guard()
      this.ports.finishAttempt(snapshot, 'finished')
      this.counts.finished++
      this.emit(active.sceneId, 'finished')
    } catch {
      if (started && active.snapshot) {
        try {
          this.ports.finishAttempt(active.snapshot, 'abandoned')
        } catch {
          /* A started receipt remains unknown/non-replayable. */
        }
      }
      this.counts.abandoned++
      this.emit(active.sceneId, 'abandoned', 'authority_or_request_failed')
    }
  }
  private ticketValid(entry: DirtyTicket): boolean {
    return (
      this.ports.enabled(entry.dirty.sceneId) &&
      this.ports.policyRevision() === entry.policyRevision &&
      this.ports.readPolicy.valid(entry.ticket)
    )
  }
  private schedule(delay: number): void {
    if (this.timer || this.disposed || !this.dirty.size) return
    this.timer = setTimeout(() => {
      this.timer = undefined
      this.resume()
    }, delay)
    this.timer.unref?.()
  }
  private skipped(sceneId: string, reason: string): void {
    this.counts.skipped++
    this.emit(sceneId, 'skipped', reason)
  }
  private emit(
    sceneId: string,
    status: Parameters<NonNullable<SemanticCoordinatorPorts['event']>>[0]['status'],
    reason?: string,
  ): void {
    try {
      this.ports.event?.({ sceneId, status, reason })
    } catch {
      /* Safe observation cannot restart work. */
    }
  }
  private settleIdle(): void {
    if (this.active || this.dirty.size) return
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
    for (const resolve of this.waiters) resolve()
    this.waiters.clear()
  }
}
