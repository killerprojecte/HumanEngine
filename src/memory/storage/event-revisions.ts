import { createHash, randomUUID } from 'node:crypto'
import type Database from 'better-sqlite3'
import { z } from 'zod'
import {
  SourceEventIdentitySchema,
  SourceEventRefSchema,
  TrustedSourceChangeSchema,
  sourceIdentityKey,
  sourceEventHeadHash,
  sameSourceRef,
  type SourceEventIdentity,
  type SourceEventRef,
  type SourceEventMetadata,
  type SourceEventMutation,
  type SourceEventResult,
  type SourceEventEligibility,
  type TrustedSourceChange,
} from '../domain/events.ts'

type DB = Pick<Database.Database, 'exec' | 'prepare' | 'transaction' | 'inTransaction'>
export const SourceEventMetadataSchema = z
  .object({
    identity: SourceEventIdentitySchema,
    ref: SourceEventRefSchema,
    bodyHash: z.string().regex(/^[a-f0-9]{64}$/),
    observedAt: z.iso.datetime({ offset: true }),
    occurredAt: z.iso.datetime({ offset: true }).nullable(),
    bodyCoverage: z.literal('unavailable'),
  })
  .strict()
export const SourceEventHeadSchema = z
  .object({
    metadata: SourceEventMetadataSchema,
    sequence: z.number().int().nonnegative().safe().nullable(),
    orderMode: z.enum(['platform_sequence', 'observed_cas']).nullable(),
    deleted: z.boolean(),
  })
  .strict()
  .refine(
    (head) =>
      head.metadata.ref.revision === 1
        ? head.orderMode === null && head.sequence === null
        : head.orderMode === 'platform_sequence'
          ? head.sequence !== null
          : head.orderMode === 'observed_cas' && head.sequence === null,
    'invalid_source_order_mode',
  )
type Head = z.infer<typeof SourceEventHeadSchema>
export interface SourceEventStoreOptions {
  assertReady?: () => void
  beforeWrite?: (mutation: Readonly<SourceEventMutation>) => void
  id?: () => string
  now?: () => string
  maxRevisions?: number
  maxEditKeys?: number
  maxHeadsPerScene?: number
  maxHeadsTotal?: number
}
/** Metadata only: never retains source text, media locators, or learning jobs. */
export class SourceEventStore {
  private readonly committed = new Set<(mutation: Readonly<SourceEventMutation>) => void>()
  /** Synchronous post-write notifications inside the original caller transaction. */
  subscribeCommitted(listener: (mutation: Readonly<SourceEventMutation>) => void): () => void {
    this.committed.add(listener)
    return () => {
      this.committed.delete(listener)
    }
  }
  private notifyCommitted(mutation: SourceEventMutation): void {
    for (const listener of this.committed) {
      const result: unknown = listener(structuredClone(mutation))
      if (result && typeof (result as PromiseLike<unknown>).then === 'function')
        throw new Error('source_event_async_subscriber')
    }
  }
  private readonly revisions: number
  private readonly edits: number
  private readonly headsPerScene: number
  private readonly headsTotal: number
  constructor(
    private readonly db: DB,
    private readonly options: SourceEventStoreOptions = {},
  ) {
    this.revisions = this.bound(options.maxRevisions ?? 32, 1, 256)
    this.edits = this.bound(options.maxEditKeys ?? 128, 1, 1024)
    this.headsPerScene = this.bound(options.maxHeadsPerScene ?? 5000, 1, 1000000)
    this.headsTotal = this.bound(options.maxHeadsTotal ?? 20000, 1, 1000000)
  }
  private bound(n: number, min: number, max: number) {
    if (!Number.isSafeInteger(n) || n < min || n > max) throw new Error('invalid_event_limit')
    return n
  }
  private exists(): boolean {
    this.options.assertReady?.()
    const names = this.db
      .prepare(
        "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('source_event_heads','source_event_revisions','source_event_edit_keys','source_event_tombstones')",
      )
      .all() as { name: string }[]
    if (names.length !== 0 && names.length !== 4) throw new Error('source_event_corrupt')
    return names.length === 4
  }
  headMetadata(identity: SourceEventIdentity): SourceEventMetadata | undefined {
    SourceEventIdentitySchema.parse(identity)
    if (!this.exists() || this.revoked(identity)) return undefined
    return this.head(identity)?.metadata
  }
  registered(sceneId: string, messageId: string): SourceEventMetadata | undefined {
    z.string().min(1).max(512).parse(sceneId)
    z.string().min(1).max(512).parse(messageId)
    if (!this.exists()) return undefined
    const rows = this.db
      .prepare('SELECT value FROM source_event_heads WHERE scene_id=? AND message_id=?')
      .all(sceneId, messageId) as { value: string }[]
    if (rows.length > 1) throw new Error('source_event_corrupt')
    if (!rows.length) return undefined
    const value = SourceEventHeadSchema.parse(this.json(rows[0]!.value))
    if (
      value.metadata.identity.sceneId !== sceneId ||
      value.metadata.identity.messageId !== messageId
    )
      throw new Error('source_event_corrupt')
    return this.headMetadata(value.metadata.identity)
  }
  private initialize() {
    this.options.assertReady?.()
    this.db
      .exec(`CREATE TABLE IF NOT EXISTS source_event_heads(identity_key TEXT PRIMARY KEY,event_id TEXT UNIQUE NOT NULL,scene_id TEXT NOT NULL,message_id TEXT NOT NULL,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS source_event_revisions(event_id TEXT NOT NULL,revision INTEGER NOT NULL,value TEXT NOT NULL,PRIMARY KEY(event_id,revision));
CREATE TABLE IF NOT EXISTS source_event_edit_keys(event_id TEXT NOT NULL,edit_key TEXT NOT NULL,revision INTEGER NOT NULL,change_hash TEXT NOT NULL,PRIMARY KEY(event_id,edit_key));
CREATE TABLE IF NOT EXISTS source_event_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));`)
  }
  private transaction<T>(fn: () => T): T {
    return this.db.inTransaction ? fn() : this.db.transaction(fn).immediate()
  }
  private head(identity: SourceEventIdentity): Head | undefined {
    const row = this.db
      .prepare(
        'SELECT identity_key,event_id,scene_id,message_id,value FROM source_event_heads WHERE identity_key=?',
      )
      .get(sourceIdentityKey(identity)) as
      | {
          identity_key: string
          event_id: string
          scene_id: string
          message_id: string
          value: string
        }
      | undefined
    if (!row) return undefined
    const parsed = SourceEventHeadSchema.safeParse(this.json(row.value))
    if (
      !parsed.success ||
      sourceIdentityKey(parsed.data.metadata.identity) !== row.identity_key ||
      parsed.data.metadata.ref.eventId !== row.event_id ||
      parsed.data.metadata.identity.sceneId !== row.scene_id ||
      parsed.data.metadata.identity.messageId !== row.message_id
    )
      throw new Error('source_event_corrupt')
    return parsed.data
  }
  private json(value: string): unknown {
    try {
      return JSON.parse(value)
    } catch {
      throw new Error('source_event_corrupt')
    }
  }
  private revoked(identity: SourceEventIdentity) {
    return !!this.db
      .prepare('SELECT 1 FROM source_event_tombstones WHERE scene_id=? AND message_id=?')
      .get(identity.sceneId, identity.messageId)
  }
  private hook(mutation: SourceEventMutation) {
    const result: unknown = this.options.beforeWrite?.(structuredClone(mutation))
    if (result && typeof (result as PromiseLike<unknown>).then === 'function')
      throw new Error('source_event_async_authority')
  }
  observe(input: TrustedSourceChange): SourceEventResult {
    const change = TrustedSourceChangeSchema.parse(input)
    return this.transaction(() => {
      this.initialize()
      this.options.assertReady?.()
      const result = (
        status: SourceEventResult['status'],
        ref?: SourceEventRef,
        previousRef?: SourceEventRef,
      ): SourceEventResult => ({
        status,
        ...(ref ? { ref } : {}),
        ...(previousRef ? { previousRef } : {}),
        bodyCoverage: 'unavailable',
      })
      if (this.revoked(change.identity)) return result('recalled')
      // A platform message cannot silently change its account or speaker identity.
      const collision = this.db
        .prepare('SELECT identity_key FROM source_event_heads WHERE scene_id=? AND message_id=?')
        .all(change.identity.sceneId, change.identity.messageId) as { identity_key: string }[]
      if (collision.some((row) => row.identity_key !== sourceIdentityKey(change.identity)))
        return result('conflict')
      const old = this.head(change.identity)
      if (old?.deleted) return result('recalled')
      if (!old && change.kind === 'ingress') {
        const total = this.db.prepare('SELECT count(*) n FROM source_event_heads').get() as {
          n: number
        }
        const scene = this.db
          .prepare('SELECT count(*) n FROM source_event_heads WHERE scene_id=?')
          .get(change.identity.sceneId) as { n: number }
        if (total.n >= this.headsTotal || scene.n >= this.headsPerScene) return result('capacity')
      }
      if (change.kind === 'ingress' && old)
        return result(
          old.metadata.bodyHash === change.bodyHash ? 'duplicate' : 'conflict',
          old.metadata.ref,
        )
      const changeHash = createHash('sha256').update(JSON.stringify(change)).digest('hex')
      if (change.kind === 'edit') {
        if (!old) return result('unorderable')
        const key = this.db
          .prepare(
            'SELECT revision,change_hash FROM source_event_edit_keys WHERE event_id=? AND edit_key=?',
          )
          .get(old.metadata.ref.eventId, change.editId) as
          { revision: number; change_hash: string } | undefined
        if (key) {
          if (
            !Number.isSafeInteger(key.revision) ||
            key.revision < 1 ||
            key.revision > old.metadata.ref.revision ||
            !/^[a-f0-9]{64}$/.test(key.change_hash)
          )
            throw new Error('source_event_corrupt')
          return result(key.change_hash === changeHash ? 'duplicate' : 'conflict', {
            ...old.metadata.ref,
            revision: key.revision,
          })
        }
        if (old.orderMode !== null && old.orderMode !== change.order.kind)
          return result('unorderable', old.metadata.ref)
        if (change.order.kind === 'observed_cas') {
          if (!sameSourceRef(change.order.expectedBase, old.metadata.ref))
            return result('conflict', old.metadata.ref)
        } else if (old.sequence !== null && change.order.sequence <= old.sequence)
          return result('conflict', old.metadata.ref)
      }
      const ref: SourceEventRef = {
        eventId: old?.metadata.ref.eventId ?? (this.options.id ?? randomUUID)(),
        revision: (old?.metadata.ref.revision ?? 0) + 1,
        deletionEpoch: old?.metadata.ref.deletionEpoch ?? 0,
      }
      const metadata = SourceEventMetadataSchema.parse({
        identity: change.identity,
        ref,
        bodyHash: change.bodyHash,
        observedAt: (this.options.now ?? (() => new Date().toISOString()))(),
        occurredAt: change.occurredAt,
        bodyCoverage: 'unavailable',
      })
      const kind = old ? 'edited' : 'new'
      const head: Head = {
        metadata,
        sequence:
          change.kind === 'edit' && change.order.kind === 'platform_sequence'
            ? change.order.sequence
            : (old?.sequence ?? null),
        orderMode: change.kind === 'edit' ? change.order.kind : null,
        deleted: false,
      }
      const mutation: SourceEventMutation = {
        kind,
        identity: change.identity,
        ref,
        previousRef: old?.metadata.ref,
        metadata,
        headHash: sourceEventHeadHash(metadata, head.sequence, head.orderMode),
      }
      this.hook(mutation)
      this.db
        .prepare(
          'INSERT INTO source_event_heads(identity_key,event_id,scene_id,message_id,value) VALUES(?,?,?,?,?) ON CONFLICT(identity_key) DO UPDATE SET value=excluded.value',
        )
        .run(
          sourceIdentityKey(change.identity),
          ref.eventId,
          change.identity.sceneId,
          change.identity.messageId,
          JSON.stringify(head),
        )
      this.db
        .prepare('INSERT INTO source_event_revisions VALUES(?,?,?)')
        .run(ref.eventId, ref.revision, JSON.stringify(metadata))
      if (change.kind === 'edit')
        this.db
          .prepare('INSERT INTO source_event_edit_keys VALUES(?,?,?,?)')
          .run(ref.eventId, change.editId, ref.revision, changeHash)
      this.db
        .prepare(
          'DELETE FROM source_event_revisions WHERE event_id=? AND revision NOT IN (SELECT revision FROM source_event_revisions WHERE event_id=? ORDER BY revision DESC LIMIT ?)',
        )
        .run(ref.eventId, ref.eventId, this.revisions)
      this.db
        .prepare(
          'DELETE FROM source_event_edit_keys WHERE event_id=? AND edit_key NOT IN (SELECT edit_key FROM source_event_edit_keys WHERE event_id=? ORDER BY revision DESC LIMIT ?)',
        )
        .run(ref.eventId, ref.eventId, this.edits)
      this.notifyCommitted(mutation)
      return result(kind, ref, old?.metadata.ref)
    })
  }
  current(ref: SourceEventRef, identity: SourceEventIdentity): SourceEventEligibility {
    return this.eligibility(ref, identity, false)
  }
  historical(ref: SourceEventRef, identity: SourceEventIdentity): SourceEventEligibility {
    return this.eligibility(ref, identity, true)
  }
  private eligibility(
    raw: SourceEventRef,
    identity: SourceEventIdentity,
    historical: boolean,
  ): SourceEventEligibility {
    const ref = SourceEventRefSchema.parse(raw)
    SourceEventIdentitySchema.parse(identity)
    const exists = this.exists()
    const no = (reason: SourceEventEligibility['reason']): SourceEventEligibility => ({
      eligible: false,
      reason,
      bodyCoverage: 'unavailable',
    })
    if (!exists) return no('missing')
    if (this.revoked(identity)) return no('recalled')
    const head = this.head(identity)
    if (!head) return no('missing')
    if (head.metadata.ref.eventId !== ref.eventId) return no('scope_mismatch')
    if (head.deleted || head.metadata.ref.deletionEpoch !== ref.deletionEpoch) return no('recalled')
    if (!historical && head.metadata.ref.revision !== ref.revision) return no('revision_changed')
    const row = this.db
      .prepare('SELECT value FROM source_event_revisions WHERE event_id=? AND revision=?')
      .get(ref.eventId, ref.revision) as { value: string } | undefined
    if (!row) return no('missing')
    const parsed = SourceEventMetadataSchema.safeParse(this.json(row.value))
    if (
      !parsed.success ||
      !sameSourceRef(parsed.data.ref, ref) ||
      sourceIdentityKey(parsed.data.identity) !== sourceIdentityKey(identity)
    )
      throw new Error('source_event_corrupt')
    if (
      sameSourceRef(head.metadata.ref, ref) &&
      JSON.stringify(parsed.data) !== JSON.stringify(head.metadata)
    )
      throw new Error('source_event_corrupt')
    return {
      eligible: true,
      reason: historical ? 'historical_metadata_only' : 'current',
      metadata: parsed.data,
      bodyCoverage: 'unavailable',
    }
  }
  hasEdits(identity: SourceEventIdentity): boolean {
    if (!this.exists()) return false
    return (this.head(identity)?.metadata.ref.revision ?? 0) > 1
  }
  capture(
    identity: SourceEventIdentity,
    bodyHash: string,
    expectedRef: SourceEventRef,
  ): SourceEventRef | undefined {
    if (!this.exists()) return undefined
    const head = this.head(identity)
    if (
      !head ||
      head.metadata.bodyHash !== bodyHash ||
      !sameSourceRef(head.metadata.ref, SourceEventRefSchema.parse(expectedRef))
    )
      return undefined
    return this.current(head.metadata.ref, identity).eligible
      ? structuredClone(head.metadata.ref)
      : undefined
  }
  // Recall tombstones are monotonic privacy authority, not evictable cache entries.
  // Head capacity does not cap or weaken this separate safety-retention obligation.
  revokeAll(sceneId: string, messageId: string): void {
    z.string().min(1).max(512).parse(sceneId)
    z.string().min(1).max(512).parse(messageId)
    this.transaction(() => {
      this.initialize()
      const rows = this.db
        .prepare('SELECT value FROM source_event_heads WHERE scene_id=? AND message_id=?')
        .all(sceneId, messageId) as { value: string }[]
      for (const row of rows) {
        const head = SourceEventHeadSchema.parse(this.json(row.value))
        if (head.deleted) continue
        const previousRef = head.metadata.ref
        const ref = SourceEventRefSchema.parse({
          ...previousRef,
          deletionEpoch: previousRef.deletionEpoch + 1,
        })
        this.hook({ kind: 'recalled', identity: head.metadata.identity, ref, previousRef })
        this.db.prepare('DELETE FROM source_event_revisions WHERE event_id=?').run(ref.eventId)
        this.db.prepare('DELETE FROM source_event_edit_keys WHERE event_id=?').run(ref.eventId)
        // Preserve identity/ref tombstone only: erase content hash and source clocks.
        this.db.prepare('DELETE FROM source_event_heads WHERE event_id=?').run(ref.eventId)
      }
      this.db
        .prepare('INSERT OR IGNORE INTO source_event_tombstones VALUES(?,?)')
        .run(sceneId, messageId)
    })
  }
}
