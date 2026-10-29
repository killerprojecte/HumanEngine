import type Database from 'better-sqlite3'
import {
  SemanticCandidateSchema,
  SemanticAttemptSchema,
  type SemanticAttempt,
  semanticHash,
  type SemanticCandidate,
  type SemanticMutation,
} from './contracts.ts'
export class SemanticStorage {
  constructor(
    private readonly db: Database.Database,
    private readonly ready: () => void,
    private readonly beforeWrite?: (mutation: SemanticMutation) => void,
  ) {}
  requireTransaction() {
    this.ready()
    if (!this.db.inTransaction) throw new Error('semantic_transaction_required')
  }
  private exists() {
    this.ready()
    return !!this.db
      .prepare(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_semantic_candidates'",
      )
      .get()
  }
  private ensure() {
    this.db.exec(
      `CREATE TABLE IF NOT EXISTS memory_semantic_candidates(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,input_key TEXT NOT NULL UNIQUE,version INTEGER NOT NULL,value TEXT); CREATE INDEX IF NOT EXISTS memory_semantic_scene ON memory_semantic_candidates(scene_id)`,
    )
  }
  private decode(row: {
    id: string
    scene_id: string
    input_key: string
    version: number
    value: unknown
  }): SemanticCandidate | undefined {
    if (
      typeof row.id !== 'string' ||
      !row.id ||
      row.id.length > 512 ||
      typeof row.scene_id !== 'string' ||
      !row.scene_id ||
      row.scene_id.length > 512 ||
      typeof row.input_key !== 'string' ||
      !/^[a-f0-9]{64}$/.test(row.input_key)
    )
      throw new Error('semantic_storage_invalid')
    if (!Number.isSafeInteger(row.version) || row.version < 1)
      throw new Error('semantic_storage_invalid')
    if (row.value === null) return undefined
    if (typeof row.value !== 'string') throw new Error('semantic_storage_invalid')
    const value = SemanticCandidateSchema.parse(JSON.parse(row.value))
    if (
      value.id !== row.id ||
      value.sceneId !== row.scene_id ||
      value.inputKey !== row.input_key ||
      value.version !== row.version
    )
      throw new Error('semantic_storage_identity_invalid')
    return value
  }
  rows(sceneId?: string): SemanticCandidate[] {
    if (!this.exists()) return []
    const rows = (
      sceneId === undefined
        ? this.db.prepare('SELECT * FROM memory_semantic_candidates ORDER BY id').all()
        : this.db
            .prepare('SELECT * FROM memory_semantic_candidates WHERE scene_id=? ORDER BY id')
            .all(sceneId)
    ) as Parameters<SemanticStorage['decode']>[0][]
    if (rows.length > (sceneId === undefined ? 4096 : 40))
      throw new Error('semantic_storage_capacity_invalid')
    return rows.flatMap((row) => {
      const value = this.decode(row)
      return value ? [value] : []
    })
  }
  get(id: string): SemanticCandidate | undefined {
    if (!this.exists()) return
    const row = this.db.prepare('SELECT * FROM memory_semantic_candidates WHERE id=?').get(id) as
      Parameters<SemanticStorage['decode']>[0] | undefined
    return row ? this.decode(row) : undefined
  }
  seen(inputKey: string): boolean {
    if (!this.exists()) return false
    return !!this.db
      .prepare('SELECT 1 FROM memory_semantic_candidates WHERE input_key=?')
      .get(inputKey)
  }
  availableSlots(sceneId: string): number {
    if (!this.exists()) return 40
    const scene = this.db
      .prepare('SELECT COUNT(*) n FROM memory_semantic_candidates WHERE scene_id=?')
      .get(sceneId) as { n: number }
    const total = this.db.prepare('SELECT COUNT(*) n FROM memory_semantic_candidates').get() as {
      n: number
    }
    return Math.max(0, Math.min(40 - scene.n, 4096 - total.n))
  }
  write(
    next: SemanticCandidate | undefined,
    previous: SemanticCandidate | undefined,
    operation: SemanticMutation['operation'],
  ) {
    this.requireTransaction()
    if (!next && !previous) throw new Error('semantic_write_empty')
    if (next) next = SemanticCandidateSchema.parse(next)
    const current = this.get((next ?? previous)!.id)
    if (semanticHash(current ?? null) !== semanticHash(previous ?? null))
      throw new Error('semantic_version_conflict')
    if (
      next &&
      previous &&
      (next.id !== previous.id ||
        next.sceneId !== previous.sceneId ||
        next.inputKey !== previous.inputKey ||
        next.version !== previous.version + 1)
    )
      throw new Error('semantic_version_conflict')
    if (
      next &&
      !previous &&
      this.exists() &&
      this.db.prepare('SELECT 1 FROM memory_semantic_candidates WHERE id=?').get(next.id)
    )
      throw new Error('semantic_identity_reused')
    if (
      next &&
      !previous &&
      (next.version !== 1 || this.seen(next.inputKey) || this.availableSlots(next.sceneId) < 1)
    )
      throw new Error('semantic_capacity_or_duplicate')
    const mutation: SemanticMutation = {
      operation,
      ...(next && (operation === 'admit' || operation === 'review')
        ? { nextState: { status: next.status, admission: next.admission } }
        : {}),
      id: (next ?? previous)!.id,
      sceneId: (next ?? previous)!.sceneId,
      previousVersion: previous?.version ?? null,
      nextVersion: next?.version ?? null,
      beforeHash: previous ? semanticHash(previous) : null,
      afterHash: next ? semanticHash(next) : null,
    }
    const returned: unknown = this.beforeWrite?.(mutation)
    if (returned && typeof returned === 'object' && 'then' in returned)
      throw new Error('semantic_async_hook_denied')
    this.ensure()
    if (next)
      this.db
        .prepare(
          'INSERT INTO memory_semantic_candidates(id,scene_id,input_key,version,value) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,value=excluded.value',
        )
        .run(next.id, next.sceneId, next.inputKey, next.version, JSON.stringify(next))
    else
      this.db
        .prepare('UPDATE memory_semantic_candidates SET version=version+1,value=NULL WHERE id=?')
        .run(previous!.id)
  }
  private attemptsExist() {
    this.ready()
    return !!this.db
      .prepare("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_semantic_attempts'")
      .get()
  }
  attempts(): SemanticAttempt[] {
    if (!this.attemptsExist()) return []
    const rows = this.db
      .prepare('SELECT scene_id,input_key,value FROM memory_semantic_attempts ORDER BY input_key')
      .all() as { scene_id: string; input_key: string; value: unknown }[]
    if (rows.length > 4096) throw new Error('semantic_attempt_capacity_invalid')
    return rows.map((row) => {
      if (typeof row.value !== 'string') throw new Error('semantic_attempt_invalid')
      const value = SemanticAttemptSchema.parse(JSON.parse(row.value))
      if (value.sceneId !== row.scene_id || value.inputKey !== row.input_key)
        throw new Error('semantic_attempt_identity_invalid')
      return value
    })
  }
  writeAttempt(next: SemanticAttempt, previous: SemanticAttempt | undefined) {
    this.requireTransaction()
    next = SemanticAttemptSchema.parse(next)
    const rows = this.attempts(),
      current = rows.find((r) => r.inputKey === next.inputKey)
    if (semanticHash(current ?? null) !== semanticHash(previous ?? null))
      throw new Error('semantic_attempt_conflict')
    if (!previous && (rows.length >= 4096 || next.version !== 1))
      throw new Error('semantic_attempt_capacity')
    if (
      previous &&
      (previous.sceneId !== next.sceneId ||
        previous.inputKey !== next.inputKey ||
        next.version !== previous.version + 1)
    )
      throw new Error('semantic_attempt_conflict')
    const result: unknown = this.beforeWrite?.({
      operation: 'attempt',
      id: next.inputKey,
      sceneId: next.sceneId,
      previousVersion: previous?.version ?? null,
      nextVersion: next.version,
      beforeHash: previous ? semanticHash(previous) : null,
      afterHash: semanticHash(next),
    })
    if (result && typeof result === 'object' && 'then' in result)
      throw new Error('semantic_async_hook_denied')
    this.db.exec(
      'CREATE TABLE IF NOT EXISTS memory_semantic_attempts(scene_id TEXT NOT NULL,input_key TEXT PRIMARY KEY,value TEXT NOT NULL)',
    )
    this.db
      .prepare(
        'INSERT INTO memory_semantic_attempts(scene_id,input_key,value) VALUES(?,?,?) ON CONFLICT(input_key) DO UPDATE SET value=excluded.value',
      )
      .run(next.sceneId, next.inputKey, JSON.stringify(next))
  }
}
