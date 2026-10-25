import { createHash, randomUUID } from 'node:crypto'
import type { Store } from './store.ts'

export type MemoryScope =
  { kind: 'scene'; sceneId: string; personId?: string } | { kind: 'sharedPublicKnowledge' }
export type MemoryKind = 'fact' | 'claim' | 'term' | 'experience' | 'summary'
export interface MemorySource {
  sceneId: string
  messageId: string
  senderId: string
}
export interface SourceEvidence extends MemorySource {
  available: boolean
  isBot: boolean
}
export interface MemoryContext {
  sceneId: string
  simulation: boolean
  sourceEvidence?: SourceEvidence[]
}
export interface MemoryRecord {
  id: string
  scope: MemoryScope
  kind: MemoryKind
  content: string
  sources: MemorySource[]
  importance: 'low' | 'medium' | 'high'
  confidence: number
  learnedAt: string
  occurredAt?: string
  validUntil?: string
  status: 'active' | 'disputed' | 'superseded' | 'invalidated'
  locked: boolean
  origin: 'manual' | 'learned'
  supersedesId?: string
  score?: number
}
export interface MemoryCandidate {
  content: string
  kind: MemoryKind
  personId?: string
  sources: MemorySource[]
  importance?: MemoryRecord['importance']
  confidence?: number
  occurredAt?: string
  validUntil?: string
  supersedesId?: string
}
export interface VectorSpace {
  providerId: string
  modelId: string
  apiModelName: string
  deployment: string
  dimensions: number
  preprocessing: string
  normalized: boolean
}
export type ProposalResult =
  { status: 'saved'; memory: MemoryRecord } | { status: 'rejected'; reason: string }
type Row = {
  id: string
  scope_key: string
  person_id: string | null
  content: string
  value: string
  fingerprint: string
  distance?: number | null
}
const hash = (value: string) => createHash('sha256').update(value).digest('hex')
const scopeKey = (scope: MemoryScope) =>
  scope.kind === 'scene' ? `scene:${scope.sceneId}` : 'shared:public'
const fingerprint = (scope: MemoryScope, content: string) =>
  hash(
    JSON.stringify([
      scopeKey(scope),
      scope.kind === 'scene' ? (scope.personId ?? '') : '',
      content.trim().replace(/\s+/g, ' ').toLowerCase(),
    ]),
  )
const read = (row: Row): MemoryRecord => JSON.parse(row.value) as MemoryRecord
const date = (value: string | undefined) =>
  value === undefined || (typeof value === 'string' && Number.isFinite(Date.parse(value)))

/** SQLite is authoritative; embeddings are replaceable, scope-filtered derived data. */
export class MemoryService {
  constructor(private readonly store: Store) {
    store.db.exec(`
      CREATE TABLE IF NOT EXISTS memories (
        id TEXT PRIMARY KEY, scope_key TEXT NOT NULL, person_id TEXT,
        content TEXT NOT NULL, fingerprint TEXT NOT NULL UNIQUE, value TEXT NOT NULL
      );
      CREATE INDEX IF NOT EXISTS memories_scope ON memories(scope_key,person_id);
      CREATE TABLE IF NOT EXISTS memory_sources (
        memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
        scene_id TEXT NOT NULL, message_id TEXT NOT NULL, sender_id TEXT NOT NULL,
        PRIMARY KEY(memory_id,scene_id,message_id)
      );
      CREATE INDEX IF NOT EXISTS memory_source_lookup ON memory_sources(scene_id,message_id);
      CREATE TABLE IF NOT EXISTS memory_deleted (fingerprint TEXT PRIMARY KEY, deleted_at TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS vector_spaces (id TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS memory_vectors (
        memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
        space_id TEXT NOT NULL REFERENCES vector_spaces(id) ON DELETE CASCADE,
        embedding BLOB NOT NULL, PRIMARY KEY(memory_id,space_id)
      );
    `)
    let cachedTermsJson = ''
    let cachedTerms: string[] = []
    store.db.function('humanengine_lexical', { deterministic: true }, (content, termsJson) => {
      if (termsJson !== cachedTermsJson) {
        cachedTermsJson = String(termsJson)
        cachedTerms = JSON.parse(cachedTermsJson) as string[]
      }
      const lower = String(content).toLowerCase()
      return cachedTerms.length
        ? cachedTerms.filter((term) => lower.includes(term)).length / cachedTerms.length
        : 0
    })
  }

  list(ctx: MemoryContext, personId?: string): MemoryRecord[] {
    return this.scopedRows(ctx, personId)
      .map(read)
      .map((record) => this.sanitize(record))
  }
  adminList(): MemoryRecord[] {
    return (this.store.db.prepare('SELECT * FROM memories ORDER BY rowid DESC').all() as Row[]).map(
      read,
    )
  }

  search(
    ctx: MemoryContext,
    query: string,
    personId?: string,
    options?: { spaceId: string; vector: number[]; limit?: number },
  ): MemoryRecord[] {
    const terms = [...new Set(query.toLowerCase().match(/[\p{L}\p{N}_:.+-]+/gu) ?? [])]
    // Validate the selected space even for an empty permitted scope, as before.
    const vector = options ? this.vector(options.spaceId, options.vector) : undefined
    const scope = this.scopeFilter(ctx, personId)
    if (!scope) return []
    const lexical = terms.length ? 'humanengine_lexical(scoped.content, ?)' : '0.0'
    const semantic = options
      ? 'CASE WHEN v.embedding IS NULL THEN 0.0 ELSE max(0.0, 1.0 - vec_distance_cosine(v.embedding, ?)) END'
      : '0.0'
    const join = options
      ? 'LEFT JOIN memory_vectors v ON v.memory_id=scoped.id AND v.space_id=?'
      : ''
    const limit = Math.max(1, Math.min(options?.limit ?? 20, 100))
    const rows = this.store.db
      .prepare(
        `
      WITH scoped AS MATERIALIZED (
        SELECT rowid AS ordinal, * FROM memories
        WHERE ${scope.sql} AND json_extract(value,'$.status') IN ('active','disputed')
          AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now'))
      ), relevant AS MATERIALIZED (
        SELECT scoped.value, scoped.ordinal, max(${lexical}, ${semantic}) AS relevance
        FROM scoped ${join}
      ), ranked AS (
        SELECT value, ordinal, relevance,
          relevance * 0.8 + json_extract(value,'$.confidence') * 0.1 +
          CASE json_extract(value,'$.importance') WHEN 'low' THEN 0.02 WHEN 'medium' THEN 0.06 ELSE 0.1 END AS score
        FROM relevant
      )
      SELECT value, score FROM ranked WHERE ? = 0 OR relevance > 0
      ORDER BY score DESC, json_extract(value,'$.learnedAt') DESC, ordinal DESC LIMIT ?
    `,
      )
      .all(
        ...scope.params,
        ...(terms.length ? [JSON.stringify(terms)] : []),
        ...(options ? [vector!, options.spaceId] : []),
        query.trim() ? 1 : 0,
        Math.trunc(limit),
      ) as { value: string; score: number }[]
    return rows.map((row) => ({
      ...this.sanitize(JSON.parse(row.value) as MemoryRecord),
      score: row.score,
    }))
  }

  propose(ctx: MemoryContext, candidate: MemoryCandidate): ProposalResult {
    if (ctx.simulation) return { status: 'rejected', reason: 'simulation_does_not_learn' }
    return this.store.db.transaction((): ProposalResult => {
      if (!this.store.scene(ctx.sceneId)?.learningEnabled)
        return { status: 'rejected', reason: 'learning_disabled' }
      if (!candidate.sources?.length)
        return { status: 'rejected', reason: 'missing_source_evidence' }
      if (
        candidate.sources.some(
          (source) =>
            source.sceneId !== ctx.sceneId ||
            this.store.recalled(source.sceneId, source.messageId) ||
            !ctx.sourceEvidence?.some(
              (evidence) =>
                evidence.sceneId === source.sceneId &&
                evidence.messageId === source.messageId &&
                evidence.senderId === source.senderId &&
                evidence.available &&
                !evidence.isBot,
            ),
        )
      )
        return { status: 'rejected', reason: 'invalid_source_evidence' }
      const scope: MemoryScope = {
        kind: 'scene',
        sceneId: ctx.sceneId,
        ...(candidate.personId ? { personId: candidate.personId } : {}),
      }
      const fp = fingerprint(scope, candidate.content)
      if (this.store.db.prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'manually_deleted' }
      const existingRow = this.store.db
        .prepare('SELECT * FROM memories WHERE fingerprint=?')
        .get(fp) as Row | undefined
      if (existingRow) {
        const existing = read(existingRow)
        if (
          existing.locked ||
          existing.origin === 'manual' ||
          existing.status === 'superseded' ||
          existing.status === 'invalidated'
        )
          return { status: 'rejected', reason: 'manual_or_invalid_record' }
        const sources = this.uniqueSources([...existing.sources, ...candidate.sources])
        const independent = new Set(sources.map((source) => source.senderId)).size
        const next = {
          ...existing,
          sources,
          confidence: Math.min(
            0.85,
            Math.max(existing.confidence, Math.min(candidate.confidence ?? 0.5, 0.7)) +
              (independent > new Set(existing.sources.map((source) => source.senderId)).size
                ? 0.05
                : 0),
          ),
        }
        this.validate(next)
        this.write(next)
        return { status: 'saved', memory: this.sanitize(next) }
      }
      if (candidate.supersedesId) {
        const previous = this.get(candidate.supersedesId)
        if (
          !previous ||
          previous.locked ||
          previous.origin === 'manual' ||
          scopeKey(previous.scope) !== scopeKey(scope) ||
          (previous.scope.kind === 'scene' && previous.scope.personId !== candidate.personId)
        )
          return { status: 'rejected', reason: 'correction_not_allowed' }
        this.write({ ...previous, status: 'superseded' })
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(previous.id)
      }
      const memory: MemoryRecord = {
        id: randomUUID(),
        scope,
        content: candidate.content.trim(),
        kind: candidate.kind,
        sources: this.uniqueSources(candidate.sources),
        importance: candidate.importance ?? 'medium',
        confidence: Math.min(candidate.confidence ?? 0.5, 0.7),
        learnedAt: new Date().toISOString(),
        status: 'active',
        locked: false,
        origin: 'learned',
        ...(candidate.occurredAt ? { occurredAt: candidate.occurredAt } : {}),
        ...(candidate.validUntil ? { validUntil: candidate.validUntil } : {}),
        ...(candidate.supersedesId ? { supersedesId: candidate.supersedesId } : {}),
      }
      this.validate(memory)
      this.write(memory)
      return { status: 'saved', memory: this.sanitize(memory) }
    })()
  }

  create(input: {
    scope: MemoryScope
    content: string
    kind: MemoryKind
    sources?: MemorySource[]
    importance?: MemoryRecord['importance']
    confidence?: number
    locked?: boolean
    occurredAt?: string
    validUntil?: string
  }): MemoryRecord {
    const memory: MemoryRecord = {
      id: randomUUID(),
      scope: input.scope,
      content: input.content.trim(),
      kind: input.kind,
      sources: this.uniqueSources(input.sources ?? []),
      importance: input.importance ?? 'medium',
      confidence: input.confidence ?? 1,
      learnedAt: new Date().toISOString(),
      status: 'active',
      locked: input.locked ?? false,
      origin: 'manual',
      ...(input.occurredAt ? { occurredAt: input.occurredAt } : {}),
      ...(input.validUntil ? { validUntil: input.validUntil } : {}),
    }
    this.validate(memory)
    this.store.db.transaction(() => {
      if (memory.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)))
        throw new Error('recalled_source')
      this.write(memory)
    })()
    return memory
  }

  update(
    id: string,
    patch: Partial<
      Pick<
        MemoryRecord,
        | 'content'
        | 'kind'
        | 'importance'
        | 'confidence'
        | 'status'
        | 'locked'
        | 'occurredAt'
        | 'validUntil'
      >
    >,
  ): MemoryRecord {
    return this.store.db.transaction(() => {
      const previous = this.get(id)
      if (!previous) throw new Error('memory_not_found')
      const lockOnly = Object.keys(patch).every((key) => key === 'locked')
      const next: MemoryRecord = {
        ...previous,
        ...patch,
        id: previous.id,
        scope: previous.scope,
        sources: previous.sources,
        origin: lockOnly ? previous.origin : 'manual',
      }
      this.validate(next)
      if (next.content !== previous.content)
        this.store.db
          .prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
          .run(fingerprint(previous.scope, previous.content), new Date().toISOString())
      this.write(next)
      if (
        next.content !== previous.content ||
        next.status === 'invalidated' ||
        next.status === 'superseded'
      )
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(id)
      return next
    })()
  }
  lock(id: string, locked: boolean): MemoryRecord {
    return this.update(id, { locked })
  }
  delete(id: string): boolean {
    return this.store.db.transaction(() => {
      const memory = this.get(id)
      if (!memory) return false
      this.store.db
        .prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
        .run(fingerprint(memory.scope, memory.content), new Date().toISOString())
      this.store.db.prepare('DELETE FROM memories WHERE id=?').run(id)
      return true
    })()
  }
  recall(sceneId: string, messageId: string): string[] {
    return this.store.db.transaction(() => {
      this.store.recall(sceneId, messageId)
      const rows = this.store.db
        .prepare(
          'SELECT m.* FROM memories m JOIN memory_sources s ON s.memory_id=m.id WHERE s.scene_id=? AND s.message_id=?',
        )
        .all(sceneId, messageId) as Row[]
      for (const row of rows) {
        const previous = read(row)
        const sources = previous.sources.filter(
          (source) => source.sceneId !== sceneId || source.messageId !== messageId,
        )
        const next: MemoryRecord = {
          ...previous,
          sources,
          confidence: (previous.confidence * sources.length) / previous.sources.length,
          status: ['superseded', 'invalidated'].includes(previous.status)
            ? previous.status
            : sources.length
              ? 'disputed'
              : 'invalidated',
        }
        this.write(next)
        if (!sources.length)
          this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(previous.id)
      }
      return rows.map((row) => row.id)
    })()
  }

  registerSpace(space: VectorSpace): string {
    if (
      !space.providerId ||
      !space.modelId ||
      !space.apiModelName ||
      !space.deployment ||
      !space.preprocessing ||
      !Number.isSafeInteger(space.dimensions) ||
      space.dimensions < 1 ||
      space.dimensions > 16384 ||
      typeof space.normalized !== 'boolean'
    )
      throw new Error('invalid_vector_space')
    const value = JSON.stringify({
      providerId: space.providerId,
      modelId: space.modelId,
      apiModelName: space.apiModelName,
      deployment: space.deployment,
      dimensions: space.dimensions,
      preprocessing: space.preprocessing,
      normalized: space.normalized,
    })
    const id = hash(value)
    this.store.db.prepare('INSERT OR IGNORE INTO vector_spaces VALUES(?,?)').run(id, value)
    return id
  }
  index(id: string, spaceId: string, vector: number[]): void {
    const bytes = this.vector(spaceId, vector)
    const memory = this.get(id)
    if (!memory || !['active', 'disputed'].includes(memory.status))
      throw new Error('memory_not_indexable')
    this.store.db
      .prepare(
        'INSERT INTO memory_vectors VALUES(?,?,?) ON CONFLICT(memory_id,space_id) DO UPDATE SET embedding=excluded.embedding',
      )
      .run(id, spaceId, bytes)
  }
  private vector(spaceId: string, vector: number[]): Buffer {
    const row = this.store.db.prepare('SELECT value FROM vector_spaces WHERE id=?').get(spaceId) as
      { value: string } | undefined
    if (!row) throw new Error('vector_space_not_found')
    const space = JSON.parse(row.value) as VectorSpace
    if (
      vector.length !== space.dimensions ||
      vector.some((value) => !Number.isFinite(value)) ||
      vector.every((value) => value === 0)
    )
      throw new Error('invalid_vector')
    const norm = space.normalized ? Math.hypot(...vector) : 1
    const float = new Float32Array(vector.map((value) => value / norm))
    if (float.some((value) => !Number.isFinite(value))) throw new Error('invalid_vector')
    return Buffer.from(float.buffer)
  }
  private scopeFilter(
    ctx: MemoryContext,
    personId?: string,
  ): { sql: string; params: string[] } | undefined {
    if (ctx.simulation)
      return personId ? undefined : { sql: "scope_key='shared:public'", params: [] }
    if (personId)
      return { sql: 'scope_key=? AND person_id=?', params: [`scene:${ctx.sceneId}`, personId] }
    return { sql: "(scope_key=? OR scope_key='shared:public')", params: [`scene:${ctx.sceneId}`] }
  }
  private scopedRows(ctx: MemoryContext, personId?: string): Row[] {
    const scope = this.scopeFilter(ctx, personId)
    if (!scope) return []
    return this.store.db
      .prepare(
        `SELECT * FROM memories WHERE ${scope.sql} AND json_extract(value,'$.status') IN ('active','disputed') AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now')) ORDER BY rowid DESC`,
      )
      .all(...scope.params) as Row[]
  }
  private get(id: string): MemoryRecord | undefined {
    const row = this.store.db.prepare('SELECT * FROM memories WHERE id=?').get(id) as
      Row | undefined
    return row ? read(row) : undefined
  }
  private sanitize(memory: MemoryRecord): MemoryRecord {
    return memory.scope.kind === 'sharedPublicKnowledge'
      ? { ...memory, sources: [], supersedesId: undefined }
      : memory
  }
  private uniqueSources(sources: MemorySource[]): MemorySource[] {
    return [
      ...new Map(
        sources.map((source) => [
          JSON.stringify([source.sceneId, source.messageId]),
          { sceneId: source.sceneId, messageId: source.messageId, senderId: source.senderId },
        ]),
      ).values(),
    ]
  }
  private validate(memory: MemoryRecord) {
    if (
      typeof memory.content !== 'string' ||
      !memory.content ||
      memory.content.length > 20_000 ||
      typeof memory.locked !== 'boolean' ||
      !['active', 'disputed', 'superseded', 'invalidated'].includes(memory.status) ||
      !['fact', 'claim', 'term', 'experience', 'summary'].includes(memory.kind) ||
      !['low', 'medium', 'high'].includes(memory.importance) ||
      !Number.isFinite(memory.confidence) ||
      memory.confidence < 0 ||
      memory.confidence > 1 ||
      !date(memory.occurredAt) ||
      !date(memory.validUntil)
    )
      throw new Error('invalid_memory')
    if (memory.scope.kind === 'scene') {
      const sceneId = memory.scope.sceneId
      if (!sceneId || memory.sources.some((source) => source.sceneId !== sceneId))
        throw new Error('memory_scope_mismatch')
    } else if (
      memory.scope.kind !== 'sharedPublicKnowledge' ||
      'personId' in memory.scope ||
      'sceneId' in memory.scope
    )
      throw new Error('invalid_public_scope')
    if (memory.sources.some((source) => !source.sceneId || !source.messageId || !source.senderId))
      throw new Error('invalid_source')
  }
  private write(memory: MemoryRecord) {
    this.store.db
      .prepare(
        'INSERT INTO memories VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET scope_key=excluded.scope_key,person_id=excluded.person_id,content=excluded.content,fingerprint=excluded.fingerprint,value=excluded.value',
      )
      .run(
        memory.id,
        scopeKey(memory.scope),
        memory.scope.kind === 'scene' ? (memory.scope.personId ?? null) : null,
        memory.content,
        fingerprint(memory.scope, memory.content),
        JSON.stringify(memory),
      )
    this.store.db.prepare('DELETE FROM memory_sources WHERE memory_id=?').run(memory.id)
    const insert = this.store.db.prepare('INSERT INTO memory_sources VALUES(?,?,?,?)')
    for (const source of memory.sources)
      insert.run(memory.id, source.sceneId, source.messageId, source.senderId)
  }
}
