import { createHash, randomUUID } from 'node:crypto'
import type { Store } from './store.ts'
import type { MemorySource, SourceEvidence } from './memory.ts'

export interface HabitContext {
  sceneId: string
  simulation: boolean
  sourceEvidence?: (SourceEvidence & { origin: 'live' | 'history' })[]
}
export type ExpressionPreference = 'concise' | 'detail' | 'questions' | 'emoji' | 'meme_frequency'
export type HabitInput =
  | { kind: 'sceneRule'; content: string; sources: MemorySource[] }
  | {
      kind: 'botExpression'
      preference: ExpressionPreference
      direction: 1 | -1
      sources: MemorySource[]
    }
export interface HabitRecord {
  id: string
  kind: 'sceneRule' | 'botExpression'
  sceneId?: string
  content?: string
  preference?: ExpressionPreference
  sources: (MemorySource & { direction: 1 | -1 })[]
  weight: number
  confidence: number
  status: 'active' | 'disputed' | 'invalidated'
  locked: boolean
  learnedAt: string
}
export type HabitResult =
  { status: 'saved'; habit: HabitRecord } | { status: 'rejected'; reason: string }
const preferences = new Set(['concise', 'detail', 'questions', 'emoji', 'meme_frequency'])
const key = (s: Pick<MemorySource, 'sceneId' | 'messageId'>) =>
  JSON.stringify([s.sceneId, s.messageId])
const fingerprint = (ctx: HabitContext, input: HabitInput) =>
  createHash('sha256')
    .update(
      JSON.stringify(
        input.kind === 'sceneRule'
          ? ['scene', ctx.sceneId, input.content.trim().replace(/\s+/g, ' ').toLowerCase()]
          : ['bot', input.preference],
      ),
    )
    .digest('hex')

/** Soft learned hints only: consumers must always give fixed and manual rules precedence. */
export class HabitService {
  constructor(private readonly store: Store) {
    store.db
      .exec(`CREATE TABLE IF NOT EXISTS habits (id TEXT PRIMARY KEY, fingerprint TEXT UNIQUE NOT NULL, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS habit_deleted (fingerprint TEXT PRIMARY KEY);`)
  }
  adminList(): HabitRecord[] {
    return (
      this.store.db.prepare('SELECT value FROM habits ORDER BY rowid').all() as { value: string }[]
    ).map((row) => JSON.parse(row.value))
  }
  list(ctx: HabitContext): HabitRecord[] {
    return this.adminList()
      .filter(
        (record) =>
          record.status === 'active' &&
          (record.kind === 'botExpression' || (!ctx.simulation && record.sceneId === ctx.sceneId)),
      )
      .map((record) => this.sanitize(record))
  }
  propose(ctx: HabitContext, input: HabitInput): HabitResult {
    return this.store.db.transaction((): HabitResult => {
      const reason = this.validate(ctx, input.sources)
      if (reason) return { status: 'rejected', reason }
      if (
        input.kind === 'sceneRule'
          ? typeof input.content !== 'string' ||
            !input.content.trim() ||
            input.content.length > 1000
          : input.kind !== 'botExpression' ||
            !preferences.has(input.preference) ||
            ![1, -1].includes(input.direction)
      )
        return { status: 'rejected', reason: 'invalid_candidate' }
      const fp = fingerprint(ctx, input)
      if (this.store.db.prepare('SELECT 1 FROM habit_deleted WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'manually_deleted' }
      const row = this.store.db.prepare('SELECT value FROM habits WHERE fingerprint=?').get(fp) as
        { value: string } | undefined
      const record: HabitRecord = row
        ? JSON.parse(row.value)
        : {
            id: randomUUID(),
            kind: input.kind,
            ...(input.kind === 'sceneRule'
              ? { sceneId: ctx.sceneId, content: input.content.trim() }
              : { preference: input.preference }),
            sources: [],
            weight: 0,
            confidence: 0,
            status: 'active',
            locked: false,
            learnedAt: new Date().toISOString(),
          }
      if (record.locked || record.status === 'invalidated')
        return { status: 'rejected', reason: 'locked_or_invalidated' }
      this.add(record, input.sources, input.kind === 'sceneRule' ? 1 : input.direction)
      this.store.db
        .prepare(
          'INSERT INTO habits VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
        )
        .run(record.id, fp, JSON.stringify(record))
      return { status: 'saved', habit: this.sanitize(record) }
    })()
  }
  feedback(
    ctx: HabitContext,
    id: string,
    input: { direction: 1 | -1; sources: MemorySource[] },
  ): HabitResult {
    return this.store.db.transaction((): HabitResult => {
      const reason = this.validate(ctx, input.sources)
      if (reason) return { status: 'rejected', reason }
      const record = this.get(id)
      if (!record || (record.kind === 'sceneRule' && record.sceneId !== ctx.sceneId))
        return { status: 'rejected', reason: 'not_available' }
      if (record.locked || record.status === 'invalidated' || ![1, -1].includes(input.direction))
        return { status: 'rejected', reason: 'locked_or_invalid_feedback' }
      this.add(record, input.sources, input.direction)
      this.save(record)
      return { status: 'saved', habit: this.sanitize(record) }
    })()
  }
  update(id: string, patch: { locked?: boolean }): HabitRecord {
    const record = this.get(id)
    if (!record) throw new Error('habit_not_found')
    if (
      Object.keys(patch).some((k) => k !== 'locked') ||
      (patch.locked !== undefined && typeof patch.locked !== 'boolean')
    )
      throw new Error('invalid_habit_patch')
    if (patch.locked !== undefined) record.locked = patch.locked
    this.save(record)
    return record
  }
  delete(id: string): void {
    this.store.db.transaction(() => {
      this.store.db
        .prepare('INSERT OR IGNORE INTO habit_deleted SELECT fingerprint FROM habits WHERE id=?')
        .run(id)
      this.store.db.prepare('DELETE FROM habits WHERE id=?').run(id)
    })()
  }
  recall(source: Pick<MemorySource, 'sceneId' | 'messageId'>): number {
    return this.store.db.transaction(() => {
      this.store.recall(source.sceneId, source.messageId)
      let changed = 0
      for (const record of this.adminList()) {
        const remaining = record.sources.filter((s) => key(s) !== key(source))
        if (remaining.length === record.sources.length) continue
        record.sources = remaining
        this.recalculate(record)
        this.save(record)
        changed++
      }
      return changed
    })()
  }
  private validate(ctx: HabitContext, sources: MemorySource[]): string | undefined {
    if (ctx.simulation) return 'simulation_read_only'
    const scene = this.store.scene(ctx.sceneId)
    if (!scene || !scene.learningEnabled) return 'learning_disabled'
    if (
      !Array.isArray(sources) ||
      sources.length < 1 ||
      sources.length > 32 ||
      sources.some(
        (s) =>
          !s ||
          !s.messageId ||
          !s.senderId ||
          s.sceneId !== ctx.sceneId ||
          this.store.recalled(s.sceneId, s.messageId) ||
          !ctx.sourceEvidence?.some(
            (e) =>
              e.sceneId === s.sceneId &&
              e.messageId === s.messageId &&
              e.senderId === s.senderId &&
              e.available &&
              !e.isBot &&
              e.origin === 'live',
          ),
      )
    )
      return 'invalid_source_evidence'
  }
  private add(record: HabitRecord, sources: MemorySource[], direction: 1 | -1) {
    const seen = new Set(record.sources.map(key))
    for (const source of sources)
      if (!seen.has(key(source))) {
        record.sources.push({
          sceneId: source.sceneId,
          messageId: source.messageId,
          senderId: source.senderId,
          direction,
        })
        seen.add(key(source))
      }
    this.recalculate(record)
  }
  private recalculate(record: HabitRecord) {
    // Each scene-local author has at most one vote; conflicting evidence weakens it.
    const authors = new Map<string, Set<number>>()
    for (const source of record.sources) {
      const id = JSON.stringify([source.sceneId, source.senderId])
      const votes = authors.get(id) ?? new Set<number>()
      votes.add(source.direction)
      authors.set(id, votes)
    }
    const votes = [...authors.values()].map((v) => (v.size === 1 ? [...v][0]! : 0))
    const net = votes.reduce((a, b) => a + b, 0)
    record.confidence = Math.min(0.75, Math.abs(net) / (authors.size + 2))
    record.weight = Math.sign(net) * Math.min(0.3, Math.abs(net) * 0.05)
    record.status = !record.sources.length
      ? 'invalidated'
      : net === 0 || (record.kind === 'sceneRule' && net < 0)
        ? 'disputed'
        : 'active'
  }
  private get(id: string): HabitRecord | undefined {
    const row = this.store.db.prepare('SELECT value FROM habits WHERE id=?').get(id) as
      { value: string } | undefined
    return row ? JSON.parse(row.value) : undefined
  }
  private save(record: HabitRecord) {
    this.store.db
      .prepare('UPDATE habits SET value=? WHERE id=?')
      .run(JSON.stringify(record), record.id)
  }
  private sanitize(record: HabitRecord): HabitRecord {
    return record.kind === 'botExpression' ? { ...record, sources: [] } : record
  }
}
