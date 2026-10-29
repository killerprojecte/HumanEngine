import { revoke } from '../recovery/ledger.ts'
import { createHash } from 'node:crypto'
import type Database from 'better-sqlite3'
import type { MemoryRecord } from '../domain/contracts.ts'
import type { SupportProof } from '../domain/proofs.ts'

export type HistoryDatabase = Pick<
  Database.Database,
  'exec' | 'prepare' | 'transaction' | 'inTransaction'
>
export type AssertionSupportSnapshot = SupportProof
export interface AssertionHistoryState {
  version: number
  deletionEpoch: number
  deleted: boolean
}
export interface AssertionRevision {
  assertionId: string
  version: number
  recordedAt: string | null
  validFrom: string | null
  validTo: string | null
  record: MemoryRecord
  supports: AssertionSupportSnapshot[]
  event: {
    kind: 'admin_assertion' | 'learned_assertion' | 'lifecycle'
    actorId: string | null
    legacyImport: boolean
  }
}
export interface AppendAssertionRevision {
  record: MemoryRecord
  supports: AssertionSupportSnapshot[]
  expectedVersion: number
  expectedDeletionEpoch: number
  idempotencyKey: string
  recordedAt: string | null
  validFrom: string | null
  validTo: string | null
  eventKind: AssertionRevision['event']['kind']
  actorId?: string
  legacyImport?: boolean
}
export interface AssertionHistoryQuery {
  throughVersion: number
  asOfRecorded?: string
  validAt?: string
  limit: number
}
export interface AssertionHistoryPage {
  revisions: AssertionRevision[]
  hasMore: boolean
  unknownRecordedTime: boolean
  retentionIncomplete: boolean
}
interface RevisionRow {
  assertion_id: string
  version: number
  scope_key: string
  recorded_at: string | null
  idempotency_key: string | null
  payload_hash: string | null
  value: string | null
}
const scopeKey = (record: MemoryRecord) =>
  record.scope.kind === 'scene' ? `scene:${record.scope.sceneId}` : 'shared:public'
const hash = (value: string) => createHash('sha256').update(value).digest('hex')
const validTime = (value: string | null) =>
  value === null ||
  (typeof value === 'string' &&
    Number.isFinite(Date.parse(value)) &&
    new Date(value).toISOString() === value)

/** Transaction-local history companion, not an authorization or truth service.
 * The facade validates scope, current sources and AND/OR proofs before committing.
 * Read callers must revalidate every revision before exposing it to a user/model.
 */
export class AssertionHistory {
  constructor(private readonly db: HistoryDatabase) {
    db.transaction(() => {
      const needsParentMigration = !db
        .prepare(
          "SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_assertion_revision_parents'",
        )
        .get()
      db.exec(`
      CREATE TABLE IF NOT EXISTS memory_assertion_state (
        assertion_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
        deletion_epoch INTEGER NOT NULL, deleted INTEGER NOT NULL
      );
      CREATE TABLE IF NOT EXISTS memory_assertion_revisions (
        assertion_id TEXT NOT NULL REFERENCES memory_assertion_state(assertion_id),
        version INTEGER NOT NULL, scope_key TEXT NOT NULL, recorded_at TEXT,
        idempotency_key TEXT, payload_hash TEXT, value TEXT,
        PRIMARY KEY(assertion_id,version), UNIQUE(assertion_id,idempotency_key)
      );
      CREATE TABLE IF NOT EXISTS memory_assertion_revision_sources (
        assertion_id TEXT NOT NULL, version INTEGER NOT NULL,
        scene_id TEXT NOT NULL, message_id TEXT NOT NULL,
        PRIMARY KEY(assertion_id,version,scene_id,message_id),
        FOREIGN KEY(assertion_id,version) REFERENCES memory_assertion_revisions(assertion_id,version) ON DELETE CASCADE
      );
      CREATE TABLE IF NOT EXISTS memory_assertion_revision_parents (
        assertion_id TEXT NOT NULL, version INTEGER NOT NULL, parent_id TEXT NOT NULL,
        PRIMARY KEY(assertion_id,version,parent_id),
        FOREIGN KEY(assertion_id,version) REFERENCES memory_assertion_revisions(assertion_id,version) ON DELETE CASCADE
      );
      CREATE INDEX IF NOT EXISTS memory_assertion_revision_parent_lookup
        ON memory_assertion_revision_parents(parent_id,assertion_id);
      CREATE INDEX IF NOT EXISTS memory_assertion_revision_source_lookup
        ON memory_assertion_revision_sources(scene_id,message_id);
      CREATE INDEX IF NOT EXISTS memory_assertion_revision_scope
        ON memory_assertion_revisions(scope_key,assertion_id);
    `)
      if (needsParentMigration)
        db.exec(`
        INSERT OR IGNORE INTO memory_assertion_revision_parents(assertion_id,version,parent_id)
        SELECT revision.assertion_id,revision.version,parent.value
        FROM memory_assertion_revisions AS revision,
          json_each(revision.value,'$.supports') AS support,
          json_each(support.value,'$.parents') AS parent
        WHERE revision.value IS NOT NULL AND parent.type='text';
      `)
    })()
  }

  state(id: string): AssertionHistoryState {
    const row = this.db
      .prepare(
        'SELECT version,deletion_epoch,deleted FROM memory_assertion_state WHERE assertion_id=?',
      )
      .get(id) as { version: number; deletion_epoch: number; deleted: number } | undefined
    return row
      ? { version: row.version, deletionEpoch: row.deletion_epoch, deleted: !!row.deleted }
      : { version: 0, deletionEpoch: 0, deleted: false }
  }

  append(input: AppendAssertionRevision): AssertionRevision {
    this.requireTransaction()
    if (
      !input.record.id ||
      !input.idempotencyKey ||
      input.idempotencyKey.length > 256 ||
      !Number.isSafeInteger(input.expectedVersion) ||
      input.expectedVersion < 0 ||
      !Number.isSafeInteger(input.expectedDeletionEpoch) ||
      input.expectedDeletionEpoch < 0 ||
      !validTime(input.recordedAt) ||
      !validTime(input.validFrom) ||
      !validTime(input.validTo) ||
      (input.validFrom !== null && input.validTo !== null && input.validFrom > input.validTo)
    )
      throw new Error('invalid_assertion_revision')
    const state = this.state(input.record.id)
    if (state.deleted || state.deletionEpoch !== input.expectedDeletionEpoch)
      throw new Error('assertion_deleted_or_stale_epoch')
    const payload = {
      record: input.record,
      supports: input.supports,
      recordedAt: input.recordedAt,
      validFrom: input.validFrom,
      validTo: input.validTo,
      event: {
        kind: input.eventKind,
        actorId: input.actorId ?? null,
        legacyImport: input.legacyImport ?? false,
      },
    }
    const payloadHash = hash(JSON.stringify(payload))
    const existing = this.db
      .prepare(
        'SELECT * FROM memory_assertion_revisions WHERE assertion_id=? AND idempotency_key=?',
      )
      .get(input.record.id, input.idempotencyKey) as RevisionRow | undefined
    if (existing) {
      if (existing.payload_hash !== payloadHash || !existing.value)
        throw new Error('assertion_idempotency_conflict')
      return JSON.parse(existing.value) as AssertionRevision
    }
    if (state.version !== input.expectedVersion) throw new Error('assertion_version_conflict')
    const previousScope = this.db
      .prepare(
        'SELECT scope_key FROM memory_assertion_revisions WHERE assertion_id=? AND value IS NOT NULL LIMIT 1',
      )
      .get(input.record.id) as { scope_key: string } | undefined
    if (previousScope && previousScope.scope_key !== scopeKey(input.record))
      throw new Error('assertion_scope_conflict')
    const revision: AssertionRevision = {
      assertionId: input.record.id,
      version: state.version + 1,
      ...payload,
    }
    this.db
      .prepare(
        `INSERT INTO memory_assertion_state VALUES(?,?,?,0)
      ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version`,
      )
      .run(input.record.id, revision.version, state.deletionEpoch)
    this.db
      .prepare('INSERT INTO memory_assertion_revisions VALUES(?,?,?,?,?,?,?)')
      .run(
        input.record.id,
        revision.version,
        scopeKey(input.record),
        input.recordedAt,
        input.idempotencyKey,
        payloadHash,
        JSON.stringify(revision),
      )
    const insertSource = this.db.prepare(
      'INSERT OR IGNORE INTO memory_assertion_revision_sources VALUES(?,?,?,?)',
    )
    for (const source of [
      ...input.record.sources,
      ...input.supports.flatMap((group) => group.sources),
    ])
      insertSource.run(input.record.id, revision.version, source.sceneId, source.messageId)
    const insertParent = this.db.prepare(
      'INSERT OR IGNORE INTO memory_assertion_revision_parents VALUES(?,?,?)',
    )
    for (const parent of input.supports.flatMap((group) => group.parents))
      insertParent.run(input.record.id, revision.version, parent)
    return JSON.parse(JSON.stringify(revision)) as AssertionRevision
  }

  /** Lazy migration inside the same transaction as the legacy record's write/read migration.
   * Existing learned provenance and unknown validity stay unknown; no chat replay.
   */
  importLegacy(record: MemoryRecord, supports: AssertionSupportSnapshot[]): AssertionHistoryState {
    this.requireTransaction()
    const state = this.state(record.id)
    if (state.deleted) throw new Error('assertion_deleted_or_stale_epoch')
    if (state.version > 0) return state
    this.append({
      record,
      supports,
      expectedVersion: 0,
      expectedDeletionEpoch: 0,
      idempotencyKey: 'legacy-import-v1',
      recordedAt: validTime(record.learnedAt) ? record.learnedAt : null,
      validFrom: validTime(record.validFrom ?? null) ? (record.validFrom ?? null) : null,
      validTo: validTime(record.validUntil ?? null) ? (record.validUntil ?? null) : null,
      eventKind: record.origin === 'manual' ? 'admin_assertion' : 'learned_assertion',
      legacyImport: true,
    })
    return this.state(record.id)
  }

  /** Scope-filtered storage read only. This is not a replacement for facade authorization. */
  /** Storage lookup only: callers still authorize current scope/epoch and revision proof. */
  revision(id: string, version: number, authorizedScopeKey: string): AssertionRevision | undefined {
    if (!Number.isSafeInteger(version) || version < 1 || this.state(id).deleted) return undefined
    const row = this.db
      .prepare(
        `SELECT value FROM memory_assertion_revisions
       WHERE assertion_id=? AND version=? AND scope_key=? AND value IS NOT NULL`,
      )
      .get(id, version, authorizedScopeKey) as { value: string } | undefined
    return row ? this.decodeRevision(id, version, authorizedScopeKey, row.value) : undefined
  }

  private decodeRevision(
    id: string,
    version: number,
    authorizedScopeKey: string,
    json: string,
  ): AssertionRevision | undefined {
    try {
      const value = JSON.parse(json) as AssertionRevision
      if (
        !value ||
        value.assertionId !== id ||
        value.version !== version ||
        !value.record ||
        value.record.id !== id ||
        !value.record.scope ||
        (value.record.scope.kind !== 'scene' &&
          value.record.scope.kind !== 'sharedPublicKnowledge') ||
        scopeKey(value.record) !== authorizedScopeKey
      )
        return undefined
      return value
    } catch {
      return undefined
    }
  }

  /** Optional limit returns the latest N retained revisions, in ascending order. */
  retainedCount(id: string, authorizedScopeKey: string): number {
    if (this.state(id).deleted) return 0
    return (
      this.db
        .prepare(
          'SELECT COUNT(*) AS count FROM memory_assertion_revisions WHERE assertion_id=? AND scope_key=? AND value IS NOT NULL',
        )
        .get(id, authorizedScopeKey) as { count: number }
    ).count
  }

  revisions(id: string, authorizedScopeKey: string, limit?: number): AssertionRevision[] {
    if (limit !== undefined && (!Number.isSafeInteger(limit) || limit < 1 || limit > 10000))
      throw new Error('assertion_history_limit_invalid')
    if (this.state(id).deleted) return []
    const rows = (
      this.db
        .prepare(
          `SELECT version,value FROM memory_assertion_revisions
      WHERE assertion_id=? AND scope_key=? AND value IS NOT NULL ORDER BY version ${limit === undefined ? 'ASC' : 'DESC'} LIMIT ?`,
        )
        .all(id, authorizedScopeKey, limit ?? -1) as { version: number; value: string }[]
    ).flatMap((row) => {
      const revision = this.decodeRevision(id, row.version, authorizedScopeKey, row.value)
      return revision ? [revision] : []
    })
    return limit === undefined ? rows : rows.reverse()
  }

  /** Time predicates execute before LIMIT: old cutoffs never search only the newest prefix.
   * Unknown validity is retained and must be qualified by the read projection.
   */
  queryRetained(
    id: string,
    authorizedScopeKey: string,
    query: AssertionHistoryQuery,
  ): AssertionHistoryPage {
    if (
      !Number.isSafeInteger(query.throughVersion) ||
      query.throughVersion < 1 ||
      !Number.isSafeInteger(query.limit) ||
      query.limit < 1 ||
      query.limit > 20 ||
      (query.asOfRecorded !== undefined && !validTime(query.asOfRecorded)) ||
      (query.validAt !== undefined && !validTime(query.validAt))
    )
      throw new Error('assertion_history_query_invalid')
    const state = this.state(id)
    if (state.deleted)
      return {
        revisions: [],
        hasMore: false,
        unknownRecordedTime: false,
        retentionIncomplete: true,
      }
    const base = 'assertion_id=? AND scope_key=? AND version<=? AND value IS NOT NULL'
    const args = [id, authorizedScopeKey, query.throughVersion]
    const retained = this.db
      .prepare(`SELECT COUNT(*) AS count FROM memory_assertion_revisions WHERE ${base}`)
      .get(...args) as { count: number }
    const unknown = this.db
      .prepare(
        `SELECT 1 FROM memory_assertion_revisions WHERE ${base} AND recorded_at IS NULL LIMIT 1`,
      )
      .get(...args)
    const malformed = this.db
      .prepare(
        `SELECT 1 FROM memory_assertion_revisions WHERE ${base} AND NOT json_valid(value) LIMIT 1`,
      )
      .get(...args)
    const predicates = [base, 'json_valid(value)']
    const parameters: (string | number)[] = [...args]
    if (query.asOfRecorded !== undefined) {
      predicates.push('recorded_at IS NOT NULL AND recorded_at<=?')
      parameters.push(query.asOfRecorded)
    }
    if (query.validAt !== undefined) {
      predicates.push(
        "(json_extract(value,'$.validFrom') IS NULL OR json_extract(value,'$.validFrom')<=?) AND (json_extract(value,'$.validTo') IS NULL OR json_extract(value,'$.validTo')>?)",
      )
      parameters.push(query.validAt, query.validAt)
    }
    const rows = this.db
      .prepare(
        `SELECT version,recorded_at,value FROM memory_assertion_revisions WHERE ${predicates.join(' AND ')} ORDER BY version DESC LIMIT ?`,
      )
      .all(...parameters, query.limit + 1) as {
      version: number
      recorded_at: string | null
      value: string
    }[]
    let invalid = !!malformed
    const revisions = rows
      .slice(0, query.limit)
      .flatMap((row) => {
        const decoded = this.decodeRevision(id, row.version, authorizedScopeKey, row.value)
        if (
          !decoded ||
          decoded.recordedAt !== row.recorded_at ||
          !validTime(decoded.recordedAt) ||
          !validTime(decoded.validFrom) ||
          !validTime(decoded.validTo) ||
          !decoded.event ||
          typeof decoded.event.legacyImport !== 'boolean'
        ) {
          invalid = true
          return []
        }
        return [decoded]
      })
      .reverse()
    return {
      revisions,
      hasMore: rows.length > query.limit,
      unknownRecordedTime: !!unknown,
      retentionIncomplete:
        invalid || retained.count < Math.min(state.version, query.throughVersion),
    }
  }

  /** Historical children must be collected before purging their parent index rows. */
  dependents(id: string): string[] {
    return (
      this.db
        .prepare(
          `SELECT DISTINCT parent.assertion_id
      FROM memory_assertion_revision_parents AS parent
      JOIN memory_assertion_revisions AS revision
        ON revision.assertion_id=parent.assertion_id AND revision.version=parent.version
      WHERE parent.parent_id=? AND revision.value IS NOT NULL ORDER BY parent.assertion_id`,
        )
        .all(id) as { assertion_id: string }[]
    ).map((row) => row.assertion_id)
  }

  /** Erase revoked evidence bodies even if an alternative proof keeps the current record alive. */
  redactSource(sceneId: string, messageId: string): string[] {
    this.requireTransaction()
    const affected = this.db
      .prepare(
        `SELECT DISTINCT assertion_id FROM memory_assertion_revision_sources
      WHERE scene_id=? AND message_id=?`,
      )
      .all(sceneId, messageId) as { assertion_id: string }[]
    for (const row of affected) this.purge(row.assertion_id)
    return affected.map((row) => row.assertion_id)
  }

  /** Nonterminal privacy invalidation. Keep monotonic version but discard all historical bodies. */
  purge(id: string): AssertionHistoryState {
    this.requireTransaction()
    const state = this.state(id)
    revoke(this.db as Database.Database, {
      kind: 'assertion',
      id,
      throughVersion: state.version,
      terminal: false,
    })
    if (state.deleted) return state
    this.db
      .prepare(
        `INSERT INTO memory_assertion_state VALUES(?,?,?,0)
      ON CONFLICT(assertion_id) DO UPDATE SET deletion_epoch=excluded.deletion_epoch`,
      )
      .run(id, state.version, state.deletionEpoch + 1)
    this.db.prepare('DELETE FROM memory_assertion_revision_sources WHERE assertion_id=?').run(id)
    this.db.prepare('DELETE FROM memory_assertion_revision_parents WHERE assertion_id=?').run(id)
    this.db
      .prepare(
        `UPDATE memory_assertion_revisions SET value=NULL,idempotency_key=NULL,payload_hash=NULL
      WHERE assertion_id=?`,
      )
      .run(id)
    return this.state(id)
  }

  /** Terminal deletion keeps only an ID/version/epoch tombstone; content and event bodies are removed. */
  erase(id: string): AssertionHistoryState {
    this.requireTransaction()
    const state = this.state(id)
    revoke(this.db as Database.Database, {
      kind: 'assertion',
      id,
      throughVersion: state.version,
      terminal: true,
    })
    if (state.deleted) return state
    this.db
      .prepare(
        `INSERT INTO memory_assertion_state VALUES(?,?,?,1)
      ON CONFLICT(assertion_id) DO UPDATE SET deletion_epoch=excluded.deletion_epoch,deleted=1`,
      )
      .run(id, state.version, state.deletionEpoch + 1)
    this.db.prepare('DELETE FROM memory_assertion_revision_sources WHERE assertion_id=?').run(id)
    this.db.prepare('DELETE FROM memory_assertion_revision_parents WHERE assertion_id=?').run(id)
    this.db.prepare('DELETE FROM memory_assertion_revisions WHERE assertion_id=?').run(id)
    return this.state(id)
  }

  private requireTransaction() {
    if (!this.db.inTransaction) throw new Error('assertion_transaction_required')
  }
}
