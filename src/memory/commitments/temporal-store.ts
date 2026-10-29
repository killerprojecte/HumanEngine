import { assertDomainReady } from '../recovery/ledger.ts'
import type { CommitmentDatabase } from './storage.ts'
import {
  CommitmentTransitionSchema,
  CommitmentWindowSchema,
  type CommitmentTimeline,
  type CommitmentTransition,
} from './temporal-contracts.ts'
/** Forward-only recorded states; constructor and missing-history reads never create/backfill rows. */
export class CommitmentTemporalStore {
  constructor(private readonly db: CommitmentDatabase) {}
  private exists() {
    assertDomainReady(this.db)
    const rows = this.db
      .prepare(
        "SELECT name,type FROM sqlite_master WHERE name IN ('memory_commitment_transitions','memory_commitment_temporal_windows')",
      )
      .all() as { name: string; type: string }[]
    if (rows.length !== 0 && (rows.length !== 2 || rows.some((r) => r.type !== 'table')))
      throw new Error('commitment_temporal_schema')
    return rows.length === 2
  }
  allIds(): string[] {
    if (!this.exists()) return []
    return (
      this.db
        .prepare(
          'SELECT id FROM memory_commitment_transitions UNION SELECT id FROM memory_commitment_temporal_windows',
        )
        .all() as { id: string }[]
    ).map((r) => {
      if (typeof r.id !== 'string' || !r.id || r.id.length > 512)
        throw new Error('commitment_temporal_corrupt')
      return r.id
    })
  }
  read(id: string): CommitmentTimeline | undefined {
    if (!this.exists()) return undefined
    const row = this.db
      .prepare('SELECT scene_id,value FROM memory_commitment_temporal_windows WHERE id=?')
      .get(id) as { scene_id: string; value: unknown } | undefined
    const rows = this.db
      .prepare(
        'SELECT scene_id,mutation_version,value FROM memory_commitment_transitions WHERE id=? ORDER BY mutation_version',
      )
      .all(id) as { scene_id: string; mutation_version: number; value: unknown }[]
    if (!row) {
      if (rows.length) throw new Error('commitment_temporal_corrupt')
      return undefined
    }
    if (typeof row.value !== 'string') throw new Error('commitment_temporal_corrupt')
    const window = CommitmentWindowSchema.parse(JSON.parse(row.value))
    if (window.id !== id || window.sceneId !== row.scene_id || rows.length > 64)
      throw new Error('commitment_temporal_corrupt')
    const transitions = rows.map((r) => {
      if (typeof r.value !== 'string') throw new Error('commitment_temporal_corrupt')
      const value = CommitmentTransitionSchema.parse(JSON.parse(r.value))
      if (
        value.id !== id ||
        value.sceneId !== window.sceneId ||
        r.scene_id !== window.sceneId ||
        value.mutationVersion !== r.mutation_version
      )
        throw new Error('commitment_temporal_corrupt')
      return value
    })
    return this.normalize({ window, transitions })
  }
  normalize(timeline: CommitmentTimeline): CommitmentTimeline {
    const window = CommitmentWindowSchema.parse(timeline.window),
      transitions = timeline.transitions.map((t) => CommitmentTransitionSchema.parse(t))
    if (
      transitions.length === 0 ||
      transitions.length > 64 ||
      new Set(transitions.map((t) => t.mutationVersion)).size !== transitions.length ||
      transitions.some(
        (t) =>
          t.id !== window.id ||
          t.sceneId !== window.sceneId ||
          t.mutationVersion <= window.prunedThroughMutationVersion ||
          t.mutationVersion < window.firstMutationVersion,
      )
    )
      throw new Error('commitment_temporal_corrupt')
    return {
      window,
      transitions: [...transitions].sort((a, b) => a.mutationVersion - b.mutationVersion),
    }
  }
  append(previous: CommitmentTimeline | undefined, raw: CommitmentTransition): CommitmentTimeline {
    const next = CommitmentTransitionSchema.parse(raw)
    if (previous?.transitions.some((t) => t.mutationVersion >= next.mutationVersion))
      throw new Error('commitment_temporal_version_conflict')
    const all = [...(previous?.transitions ?? []), next],
      pruned = all.slice(0, Math.max(0, all.length - 64))
    const window = previous
      ? { ...previous.window }
      : {
          schemaVersion: 1 as const,
          id: next.id,
          sceneId: next.sceneId,
          firstMutationVersion: next.mutationVersion,
          firstRecordedAt: next.recordedAt,
          prunedThroughMutationVersion: 0,
          clockNonmonotonic: false,
        }
    if (pruned.length) window.prunedThroughMutationVersion = pruned.at(-1)!.mutationVersion
    if (
      previous?.transitions.at(-1) &&
      Date.parse(next.recordedAt) < Date.parse(previous.transitions.at(-1)!.recordedAt)
    )
      window.clockNonmonotonic = true
    return this.normalize({ window, transitions: all.slice(-64) })
  }
  replace(id: string, timeline: CommitmentTimeline | undefined) {
    assertDomainReady(this.db)
    if (!this.db.inTransaction) throw new Error('commitment_transaction_required')
    if (timeline) timeline = this.normalize(timeline)
    if (timeline && timeline.window.id !== id) throw new Error('commitment_temporal_corrupt')
    if (!timeline && !this.exists()) return
    this.db.exec(
      'CREATE TABLE IF NOT EXISTS memory_commitment_transitions(id TEXT NOT NULL,mutation_version INTEGER NOT NULL,scene_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(id,mutation_version)); CREATE TABLE IF NOT EXISTS memory_commitment_temporal_windows(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,value TEXT NOT NULL);',
    )
    this.db.prepare('DELETE FROM memory_commitment_transitions WHERE id=?').run(id)
    this.db.prepare('DELETE FROM memory_commitment_temporal_windows WHERE id=?').run(id)
    if (!timeline) return
    this.db
      .prepare('INSERT INTO memory_commitment_temporal_windows VALUES(?,?,?)')
      .run(id, timeline.window.sceneId, JSON.stringify(timeline.window))
    const put = this.db.prepare('INSERT INTO memory_commitment_transitions VALUES(?,?,?,?)')
    for (const t of timeline.transitions)
      put.run(id, t.mutationVersion, t.sceneId, JSON.stringify(t))
  }
}
