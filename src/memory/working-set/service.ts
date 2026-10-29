import { createHash, randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'
import {
  WorkingSetSelectionSchema,
  WorkingSetPinPolicySchema,
  WorkingSetSnapshotSchema,
  type WorkingSetPinPolicy,
  type WorkingSetContext,
  type WorkingSetItem,
  type WorkingSetLease,
  type WorkingSetPorts,
  type WorkingSetReference,
  type WorkingSetSelection,
  type WorkingSetSnapshot,
  type WorkingSetViewItem,
} from './contracts.ts'
import {
  classifyWorkingSet,
  referenceKey,
  sameSelection,
  selectionKey,
  workingSetPolicy,
} from './policy.ts'
import { WorkingSetStorage } from './storage.ts'

/** Optional metadata projection: no model, content mutation, or independent read permission. */
export class WorkingSetService {
  readonly storage: WorkingSetStorage
  private policy
  constructor(
    private readonly db: Database.Database,
    private readonly ports: WorkingSetPorts,
  ) {
    this.storage = new WorkingSetStorage(db)
    this.storage.assertSupported()
    this.policy = workingSetPolicy(ports.policy)
  }
  configurePolicy(
    update: Pick<
      Partial<import('./contracts.ts').WorkingSetPolicy>,
      'coldAfterMs' | 'reviewAfterMs'
    >,
  ): void {
    if (Object.keys(update).some((key) => key !== 'coldAfterMs' && key !== 'reviewAfterMs'))
      throw new Error('working_set_policy_invalid')
    this.policy = workingSetPolicy({ ...this.policy, ...update })
  }
  private now(): number {
    const value = (this.ports.now ?? Date.now)()
    if (!Number.isSafeInteger(value) || value < 0) throw new Error('working_set_clock_invalid')
    return value
  }
  private enabled(ctx: WorkingSetContext): boolean {
    this.ports.assertReady()
    return !ctx.simulation && this.ports.enabled()
  }
  private write(): void {
    const result: unknown = this.ports.beforeWrite()
    if (result && typeof result === 'object' && 'then' in result)
      throw new Error('working_set_async_hook_denied')
    this.storage.ensure()
  }
  private writePin(input: WorkingSetPinPolicy): void {
    const policy = WorkingSetPinPolicySchema.parse(input)
    this.write()
    const result: unknown = this.ports.beforePinWrite(structuredClone(policy))
    if (result && typeof result === 'object' && 'then' in result)
      throw new Error('working_set_async_hook_denied')
    this.storage.putPin(policy)
  }
  private bounded(input: readonly WorkingSetSelection[]) {
    const unique = new Map<string, WorkingSetSelection>()
    for (const raw of input.slice(0, this.policy.maxBatch)) {
      const value = WorkingSetSelectionSchema.parse(raw)
      const prior = unique.get(referenceKey(value.ref))
      if (prior && prior.dependencyKey !== value.dependencyKey)
        throw new Error('working_set_selection_conflict')
      unique.set(referenceKey(value.ref), value)
    }
    return { values: [...unique.values()], limited: input.length > this.policy.maxBatch }
  }
  private resolve(
    ctx: WorkingSetContext,
    input: readonly WorkingSetSelection[],
  ): Map<string, WorkingSetSnapshot> {
    if (!input.length) return new Map()
    const wanted = new Map(input.map((value) => [referenceKey(value.ref), value]))
    const snapshots = this.ports.resolveCurrent(
      ctx,
      input.map((value) => value.ref),
    )
    if (!Array.isArray(snapshots) || snapshots.length > this.policy.maxBatch)
      throw new Error('working_set_authority_invalid')
    const result = new Map<string, WorkingSetSnapshot>()
    for (const raw of snapshots) {
      const value = WorkingSetSnapshotSchema.parse(raw),
        selected = wanted.get(referenceKey(value.ref))
      if (!selected || value.sceneId !== ctx.sceneId || !sameSelection(selected, value)) continue
      const key = selectionKey(value)
      if (result.has(key)) throw new Error('working_set_authority_duplicate')
      result.set(key, value)
    }
    return result
  }
  private previous(snapshot: WorkingSetSnapshot): WorkingSetItem | undefined {
    const item = this.storage.item(snapshot.sceneId, snapshot.ref)
    return item && sameSelection(item.snapshot, snapshot) ? item : undefined
  }
  private item(
    snapshot: WorkingSetSnapshot,
    previous: WorkingSetItem | undefined,
    now: number,
  ): WorkingSetItem {
    return {
      schemaVersion: 1,
      snapshot,
      metadataRevision: previous?.metadataRevision ?? 1,
      contextSelectedCount: previous?.contextSelectedCount ?? 0,
      lastSelectedAt: previous?.lastSelectedAt ?? null,
      projectedAt: now,
    }
  }
  view(
    ctx: WorkingSetContext,
    input: readonly WorkingSetSelection[],
    options: { includeCold?: boolean } = {},
  ) {
    if (!this.enabled(ctx))
      return {
        enabled: false,
        items: [] as WorkingSetViewItem[],
        coverage: 'disabled' as const,
        omittedCold: 0,
        unavailable: 0,
      }
    const batch = this.bounded(input),
      current = this.resolve(ctx, batch.values),
      now = this.now()
    let omittedCold = 0
    const items: WorkingSetViewItem[] = []
    for (const selected of batch.values) {
      const snapshot = current.get(selectionKey(selected))
      if (!snapshot) continue
      const item = this.previous(snapshot),
        policy = this.storage.pin(ctx.sceneId, snapshot.ref)
      const exactPolicy = policy && sameSelection(policy.selection, snapshot)
      const pinStatus = !policy
        ? ('none' as const)
        : policy.terminal
          ? ('terminal' as const)
          : exactPolicy
            ? ('current' as const)
            : ('stale' as const)
      const unresolvedPolicy = !!policy?.pinned && !policy.terminal && !exactPolicy
      const pinned = !!policy?.pinned && !policy.terminal && !!exactPolicy
      const classification = classifyWorkingSet(
        snapshot,
        item,
        now,
        this.policy,
        pinned,
        unresolvedPolicy,
      )
      if (options.includeCold === false && classification.state === 'cold') {
        omittedCold++
        continue
      }
      items.push({
        ref: snapshot.ref,
        dependencyKey: snapshot.dependencyKey,
        ...classification,
        pinned,
        pinIntent: policy?.pinned ?? false,
        pinRevision: policy?.policyRevision ?? 0,
        pinStatus,
        policyCoverage: unresolvedPolicy ? 'unknown' : 'complete',
        metadataRevision: item?.metadataRevision ?? 0,
        contextSelectedCount: item?.contextSelectedCount ?? 0,
        lastSelectedAt: item?.lastSelectedAt ?? null,
        protection: snapshot.protection,
      })
    }
    this.ports.assertReady()
    return {
      enabled: true,
      items,
      coverage:
        batch.limited ||
        batch.values.length !== current.size ||
        omittedCold > 0 ||
        items.some(
          (item) =>
            item.protection === 'unresolved_commitment_coverage' ||
            item.policyCoverage === 'unknown',
        )
          ? ('limited' as const)
          : ('complete' as const),
      omittedCold,
      unavailable: batch.values.length - current.size,
    }
  }
  recordSelected(
    ctx: WorkingSetContext,
    input: { turnId: string; selectedAt: number; selections: readonly WorkingSetSelection[] },
  ) {
    return this.db
      .transaction(() => {
        if (!this.enabled(ctx))
          return { recorded: 0, duplicates: 0, unavailable: 0, coverage: 'disabled' as const }
        const now = this.now()
        if (
          !input.turnId ||
          input.turnId.length > 512 ||
          !Number.isSafeInteger(input.selectedAt) ||
          input.selectedAt < 0 ||
          input.selectedAt > now ||
          input.selectedAt < now - this.policy.selectionMaxAgeMs
        )
          throw new Error('working_set_selection_invalid')
        const batch = this.bounded(input.selections)
        // Direct plan exposure is not an assertion exposure. Plan metrics are not supported in this slice.
        if (batch.values.some((value) => value.ref.kind !== 'assertion'))
          throw new Error('working_set_plan_metrics_unsupported')
        const current = this.resolve(ctx, batch.values)
        let recorded = 0,
          duplicates = 0,
          limited = batch.limited
        for (const selected of batch.values) {
          const snapshot = current.get(selectionKey(selected))
          if (!snapshot) continue
          const key = createHash('sha256')
            .update(JSON.stringify([ctx.sceneId, input.turnId, referenceKey(selected.ref)]))
            .digest('hex')
          if (this.storage.receipt(key)) {
            duplicates++
            continue
          }
          this.write()
          this.storage.pruneReceipts(now - this.policy.receiptRetentionMs)
          if (this.storage.receiptCount() >= this.policy.maxReceipts) {
            limited = true
            continue
          }
          const previous = this.previous(snapshot),
            item = this.item(snapshot, previous, now)
          this.storage.putReceipt(key, {
            schemaVersion: 1,
            sceneId: ctx.sceneId,
            turnId: input.turnId,
            selection: selected,
            selectedAt: input.selectedAt,
          })
          this.storage.putItem({
            ...item,
            metadataRevision: previous ? previous.metadataRevision + 1 : 1,
            contextSelectedCount: Math.min(Number.MAX_SAFE_INTEGER, item.contextSelectedCount + 1),
            lastSelectedAt: Math.max(item.lastSelectedAt ?? 0, input.selectedAt),
          })
          recorded++
        }
        this.ports.assertReady()
        return {
          recorded,
          duplicates,
          unavailable: batch.values.length - current.size,
          coverage:
            limited || batch.values.length !== current.size
              ? ('limited' as const)
              : ('complete' as const),
        }
      })
      .immediate()
  }
  /** Administrator policy metadata only; available while the feature or source is OFF. */
  listPinPolicies(ctx: WorkingSetContext, options: { limit?: number } = {}) {
    this.ports.assertReady()
    const limit = options.limit ?? 100
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 200)
      throw new Error('working_set_limit_invalid')
    if (ctx.simulation) return { items: [] as WorkingSetPinPolicy[], coverage: 'complete' as const }
    const rows = this.storage.listPins(ctx.sceneId, limit + 1)
    this.ports.assertReady()
    return {
      items: rows.slice(0, limit),
      coverage: rows.length > limit ? ('limited' as const) : ('complete' as const),
    }
  }
  setPinned(
    ctx: WorkingSetContext,
    selection: WorkingSetSelection,
    input: { expectedRevision: number; pinned: boolean },
  ): WorkingSetPinPolicy {
    return this.db
      .transaction(() => {
        this.ports.assertReady()
        if (ctx.simulation) throw new Error('working_set_disabled')
        if (
          typeof input.pinned !== 'boolean' ||
          !Number.isSafeInteger(input.expectedRevision) ||
          input.expectedRevision < 0
        )
          throw new Error('working_set_pin_invalid')
        const selected = WorkingSetSelectionSchema.parse(selection),
          previous = this.storage.pin(ctx.sceneId, selected.ref)
        if ((previous?.policyRevision ?? 0) !== input.expectedRevision)
          throw new Error('working_set_policy_conflict')
        if (previous?.terminal) throw new Error('working_set_policy_terminal')
        if (input.pinned) {
          if (!this.ports.enabled()) throw new Error('working_set_disabled')
          if (!this.resolve(ctx, [selected]).has(selectionKey(selected)))
            throw new Error('working_set_source_stale')
        } else if (!previous) throw new Error('working_set_policy_not_found')
        const anchor = input.pinned ? selected : previous!.selection
        if (
          previous &&
          previous.pinned === input.pinned &&
          sameSelection(previous.selection, anchor)
        )
          return previous
        const policy: WorkingSetPinPolicy = {
          schemaVersion: 1,
          sceneId: ctx.sceneId,
          selection: anchor,
          policyRevision: (previous?.policyRevision ?? 0) + 1,
          pinned: input.pinned,
          terminal: false,
        }
        this.writePin(policy)
        this.ports.assertReady()
        return policy
      })
      .immediate()
  }
  /** Only the owner's authoritative terminal-delete lifecycle may call this. */
  terminalInvalidate(
    sceneId: string,
    refs: readonly Pick<WorkingSetReference, 'kind' | 'id'>[],
  ): void {
    this.db
      .transaction(() => {
        this.ports.assertReady()
        if (!this.storage.exists()) return
        for (const ref of refs) {
          const previous = this.storage.pin(sceneId, ref)
          if (previous && !previous.terminal) {
            const policy: WorkingSetPinPolicy = {
              ...previous,
              policyRevision: previous.policyRevision + 1,
              pinned: false,
              terminal: true,
            }
            this.writePin(policy)
          }
        }
        this.storage.invalidate(sceneId, refs)
      })
      .immediate()
  }
  /** Invoke in the authoritative commit transaction; no source replay or model work. */
  enqueue(ctx: WorkingSetContext, input: readonly WorkingSetSelection[]) {
    return this.db
      .transaction(() => {
        if (!this.enabled(ctx)) return { queued: 0, coverage: 'disabled' as const }
        const batch = this.bounded(input),
          current = this.resolve(ctx, batch.values)
        let queued = 0
        for (const snapshot of current.values()) {
          // A committed projection already at this exact input watermark is clean,
          // including after restart. Clock-only cooling is computed by policy at read.
          if (this.previous(snapshot)) continue
          const existing = this.storage.job(ctx.sceneId, snapshot.ref)
          if (existing && sameSelection(existing.snapshot, snapshot)) continue
          this.write()
          this.storage.putJob({
            schemaVersion: 1,
            snapshot,
            generation: randomUUID(),
            enqueuedAt: this.now(),
            lease: null,
          })
          queued++
        }
        return { queued, coverage: batch.limited ? ('limited' as const) : ('complete' as const) }
      })
      .immediate()
  }
  claim(ctx: WorkingSetContext, limit = this.policy.maxBatch): WorkingSetLease[] {
    return this.db
      .transaction(() => {
        if (!this.enabled(ctx)) return []
        if (!Number.isSafeInteger(limit) || limit < 1) throw new Error('working_set_limit_invalid')
        const now = this.now(),
          jobs = this.storage.jobs(ctx.sceneId, now, Math.min(limit, this.policy.maxBatch))
        if (!jobs.length) return []
        this.write()
        return jobs.map((job) => {
          const lease = { token: randomUUID(), expiresAt: now + this.policy.leaseMs }
          this.storage.putJob({ ...job, lease })
          return {
            sceneId: ctx.sceneId,
            ref: job.snapshot.ref,
            dependencyKey: job.snapshot.dependencyKey,
            generation: job.generation,
            ...lease,
          }
        })
      })
      .immediate()
  }
  complete(ctx: WorkingSetContext, leases: readonly WorkingSetLease[]) {
    return this.db
      .transaction(() => {
        if (!this.enabled(ctx))
          return { completed: 0, stale: leases.length, coverage: 'disabled' as const }
        const bounded = leases.slice(0, this.policy.maxBatch),
          now = this.now()
        const valid = bounded.filter((lease) => {
          if (lease.sceneId !== ctx.sceneId || lease.expiresAt <= now) return false
          const job = this.storage.job(ctx.sceneId, lease.ref)
          return (
            job?.generation === lease.generation &&
            job.lease?.token === lease.token &&
            job.lease.expiresAt === lease.expiresAt &&
            sameSelection(job.snapshot, lease)
          )
        })
        const current = valid.length
          ? this.resolve(ctx, valid)
          : new Map<string, WorkingSetSnapshot>()
        let completed = 0
        for (const lease of valid) {
          // Recheck the lease after the authority callback, before any metadata write.
          const job = this.storage.job(ctx.sceneId, lease.ref)
          if (job?.generation !== lease.generation || job.lease?.token !== lease.token) continue
          const snapshot = current.get(selectionKey(lease))
          this.write()
          if (snapshot) {
            this.storage.putItem(this.item(snapshot, this.previous(snapshot), now))
            completed++
          }
          this.storage.removeJob(ctx.sceneId, lease.ref)
        }
        this.ports.assertReady()
        return {
          completed,
          stale: bounded.length - completed,
          coverage: leases.length > bounded.length ? ('limited' as const) : ('complete' as const),
        }
      })
      .immediate()
  }
  drain(ctx: WorkingSetContext, limit = this.policy.maxBatch) {
    const leases = this.claim(ctx, limit),
      result = this.complete(ctx, leases)
    return { ...result, pending: this.storage.pending(ctx.sceneId) }
  }
  /** Disable/cancel hook: drop only deterministic pending work, independent of enabled. */
  cancelMaintenance(sceneId?: string): void {
    this.db
      .transaction(() => {
        this.ports.assertReady()
        if (!this.storage.exists()) return
        this.write()
        if (sceneId)
          this.db.prepare('DELETE FROM memory_working_set_jobs WHERE scene_id=?').run(sceneId)
        else this.db.prepare('DELETE FROM memory_working_set_jobs').run()
      })
      .immediate()
  }
  /** Privacy invalidation is independent of enabled. Never upgrades an old job to current. */
  invalidate(sceneId: string, refs: readonly Pick<WorkingSetReference, 'kind' | 'id'>[]): void {
    this.db
      .transaction(() => {
        this.ports.assertReady()
        if (!this.storage.exists() || !refs.length) return
        this.write()
        this.storage.invalidate(sceneId, refs)
      })
      .immediate()
  }
}
