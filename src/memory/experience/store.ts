import { assertDomainReady } from '../recovery/ledger.ts'
import type Database from 'better-sqlite3'
import type {
  ExperienceOutcome,
  ExperiencePolicyCandidate,
  MechanicalReceipt,
  TrustedPolicyUse,
} from '../domain/experience.ts'
export type ExperienceDatabase = Pick<
  Database.Database,
  'exec' | 'prepare' | 'transaction' | 'inTransaction'
>
export class ExperienceStore {
  constructor(readonly db: ExperienceDatabase) {
    assertDomainReady(db)
    db.transaction(() =>
      db.exec(`
      CREATE TABLE IF NOT EXISTS memory_experience_outcomes(id TEXT PRIMARY KEY,root TEXT UNIQUE NOT NULL,scene TEXT NOT NULL,source TEXT NOT NULL,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_policies(id TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_validations(id TEXT PRIMARY KEY,policy TEXT NOT NULL,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_uses(root TEXT PRIMARY KEY,scene TEXT NOT NULL,policy TEXT NOT NULL,target TEXT,value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_experience_tombstones(key TEXT PRIMARY KEY);
      CREATE INDEX IF NOT EXISTS memory_experience_outcome_source ON memory_experience_outcomes(scene,source);
      CREATE INDEX IF NOT EXISTS memory_experience_policy_scene ON memory_experience_policies(scene);
    `),
    )()
  }
  transaction<T>(work: () => T): T {
    return this.db.transaction(work)()
  }
  private writing() {
    assertDomainReady(this.db)
    if (!this.db.inTransaction) throw new Error('experience_transaction_required')
  }
  private read<T>(sql: string, ...args: string[]): T | undefined {
    assertDomainReady(this.db)
    const row = this.db.prepare(sql).get(...args) as { value: string } | undefined
    return row ? (JSON.parse(row.value) as T) : undefined
  }
  outcome(id: string) {
    return this.read<ExperienceOutcome>(
      'SELECT value FROM memory_experience_outcomes WHERE id=?',
      id,
    )
  }
  root(root: string) {
    return this.read<ExperienceOutcome>(
      'SELECT value FROM memory_experience_outcomes WHERE root=?',
      root,
    )
  }
  policy(id: string) {
    return this.read<ExperiencePolicyCandidate>(
      'SELECT value FROM memory_experience_policies WHERE id=?',
      id,
    )
  }
  receipt(id: string) {
    return this.read<MechanicalReceipt>(
      'SELECT value FROM memory_experience_validations WHERE id=?',
      id,
    )
  }
  tombstoned(key: string) {
    assertDomainReady(this.db)
    return !!this.db.prepare('SELECT 1 FROM memory_experience_tombstones WHERE key=?').get(key)
  }
  tombstone(key: string) {
    this.writing()
    this.db.prepare('INSERT OR IGNORE INTO memory_experience_tombstones VALUES(?)').run(key)
  }
  putOutcome(outcome: ExperienceOutcome) {
    this.writing()
    this.db
      .prepare('INSERT INTO memory_experience_outcomes VALUES(?,?,?,?,?)')
      .run(
        outcome.id,
        outcome.rootId,
        outcome.sceneId,
        outcome.source.messageId,
        JSON.stringify(outcome),
      )
  }
  putPolicy(policy: ExperiencePolicyCandidate, expectedVersion?: number) {
    this.writing()
    if (expectedVersion === undefined)
      this.db
        .prepare('INSERT INTO memory_experience_policies VALUES(?,?,?,?)')
        .run(policy.id, policy.sceneId, policy.version, JSON.stringify(policy))
    else if (
      this.db
        .prepare('UPDATE memory_experience_policies SET version=?,value=? WHERE id=? AND version=?')
        .run(policy.version, JSON.stringify(policy), policy.id, expectedVersion).changes !== 1
    )
      throw new Error('experience_version_conflict')
  }
  putReceipt(id: string, receipt: MechanicalReceipt) {
    this.writing()
    this.db
      .prepare('INSERT INTO memory_experience_validations VALUES(?,?,?)')
      .run(id, receipt.candidateId, JSON.stringify(receipt))
  }
  policies(scene: string, limit = 201): ExperiencePolicyCandidate[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare('SELECT value FROM memory_experience_policies WHERE scene=? ORDER BY id LIMIT ?')
        .all(scene, limit) as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  outcomes(scene: string, limit: number): ExperienceOutcome[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare('SELECT value FROM memory_experience_outcomes WHERE scene=? ORDER BY id LIMIT ?')
        .all(scene, limit) as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  policiesForOutcome(scene: string, outcome: string): ExperiencePolicyCandidate[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare(
          "SELECT p.value FROM memory_experience_policies p WHERE p.scene=? AND (EXISTS(SELECT 1 FROM json_each(p.value,'$.supportIds') j WHERE j.value=?) OR EXISTS(SELECT 1 FROM json_each(p.value,'$.counterexampleIds') j WHERE j.value=?))",
        )
        .all(scene, outcome, outcome) as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  sourceOutcomes(scene: string, source: string): ExperienceOutcome[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare(
          "SELECT value FROM memory_experience_outcomes WHERE scene=? AND (source=? OR json_extract(value,'$.target.messageId')=?)",
        )
        .all(scene, source, source) as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  putUse(root: string, scene: string, use: TrustedPolicyUse) {
    this.writing()
    this.db
      .prepare('INSERT OR IGNORE INTO memory_experience_uses VALUES(?,?,?,?,?)')
      .run(root, scene, use.policyRef.id, use.targetMessageId ?? null, JSON.stringify(use))
  }
  uses(scene: string, target: string): TrustedPolicyUse[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare('SELECT value FROM memory_experience_uses WHERE scene=? AND target=?')
        .all(scene, target) as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  useCounts(scene: string, policy: string): Record<'exposed' | 'applied' | 'declined', number> {
    assertDomainReady(this.db)
    const counts = { exposed: 0, applied: 0, declined: 0 }
    const rows = this.db
      .prepare(
        `
      SELECT kind, count(*) AS count FROM (
        SELECT DISTINCT json_extract(value,'$.turnId') AS turn_id,
          json_extract(value,'$.policyRef.id') AS policy_id,
          json_extract(value,'$.policyRef.version') AS policy_version,
          json_extract(value,'$.kind') AS kind
        FROM memory_experience_uses WHERE scene=? AND policy=?
      ) GROUP BY kind
    `,
      )
      .all(scene, policy) as { kind: string; count: number }[]
    for (const row of rows)
      if (row.kind === 'exposed' || row.kind === 'applied' || row.kind === 'declined')
        counts[row.kind] = row.count
    return counts
  }
  erasePolicy(id: string) {
    this.writing()
    this.db.prepare('DELETE FROM memory_experience_validations WHERE policy=?').run(id)
    this.db.prepare('DELETE FROM memory_experience_uses WHERE policy=?').run(id)
    this.db.prepare('DELETE FROM memory_experience_policies WHERE id=?').run(id)
  }
  eraseOutcome(id: string) {
    this.writing()
    this.db.prepare('DELETE FROM memory_experience_outcomes WHERE id=?').run(id)
  }
}
