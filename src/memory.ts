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
  /** Trusted extraction boundary; never populated from model arguments. */
  provenance?: 'source-only' | 'contextual'
  dependencyMemoryIds?: string[]
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
  lineageStatus?: 'verified' | 'unverified' | 'quarantined'
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
  mergeIntoId?: string
}
export interface VectorSpace {
  providerId: string
  modelId: string
  apiModelName: string
  deployment: string
  dimensions: number
  preprocessing: string
  normalized: boolean
  generation?: string
  encoderRevision?: string
}
export type ProposalResult =
  { status: 'saved'; memory: MemoryRecord } | { status: 'rejected'; reason: string }
type Support = { sources: MemorySource[]; parents: string[]; invalidatedBy?: string }
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
const historicalFingerprint = (memory: MemoryRecord) =>
  `history:${fingerprint(memory.scope, memory.content)}:${memory.id}`
const read = (row: Row): MemoryRecord => JSON.parse(row.value) as MemoryRecord

// Chinese questions need word boundaries even when no embedding model is configured.
// Keep technical tokens intact; never turn individual Han characters into broad matches.
const hanWords = new Intl.Segmenter('zh', { granularity: 'word' })
const genericHanTerms = new Set([
  '什么',
  '怎么',
  '如何',
  '是否',
  '能否',
  '请问',
  '一下',
  '一个',
  '这个',
  '那个',
  '哪些',
  '这样',
  '那样',
  '之前',
  '现在',
  '以后',
  '曾经',
  '记得',
  '记住',
  '知道',
  '事情',
  '东西',
  '内容',
  '问题',
  '信息',
  '有关',
  '相关',
  '说过',
  '过的',
  '应该',
  '可以',
  '需要',
  '不是',
  '没有',
  '不要',
  '还有',
  '以及',
  '因为',
  '所以',
  '但是',
  '然后',
  '情况',
  '时候',
  '用户',
  '我们',
  '你们',
  '他们',
  '她们',
  '大家',
])
function memorySearchTerms(query: string): string[] {
  const terms = new Set<string>()
  const add = (term: string) => {
    if (terms.size >= 64 || genericHanTerms.has(term)) return
    if (/^\p{Script=Han}+$/u.test(term) && [...term].length < 2) return
    terms.add(term)
  }
  for (const token of query
    .toLowerCase()
    .slice(0, 2000)
    .match(/[\p{L}\p{N}_:.+-]+/gu) ?? []) {
    add(token)
    if (/\p{Script=Han}/u.test(token)) {
      for (const word of hanWords.segment(token)) {
        if (word.isWordLike && /\p{Script=Han}/u.test(word.segment)) add(word.segment)
      }
    }
  }
  return [...terms]
}
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
    // Atomic, idempotent migration: legacy content is retained but never silently
    // asserted to have independent provenance. No historical messages are replayed.
    store.db.transaction(() => {
      store.db.exec(`CREATE TABLE IF NOT EXISTS memory_supports (
        memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
        value TEXT NOT NULL
      );`)
      store.db
        .prepare(
          `UPDATE memories SET value=json_set(value,'$.lineageStatus','unverified')
        WHERE json_extract(value,'$.origin')='learned'
          AND json_extract(value,'$.lineageStatus') IS NULL`,
        )
        .run()
    })()
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
    if (!ctx.simulation) this.refreshSupport()
    return this.scopedRows(ctx, personId)
      .map(read)
      .filter((record) => this.supported(record))
      .map((record) => this.sanitize(record))
  }
  /** Bounded learning-only targets. Candidate metadata is relevance, never read authority. */
  reconciliationTargets(
    ctx: Omit<MemoryContext, 'sourceEvidence'> & {
      sourceEvidence: (SourceEvidence & { origin: 'live' | 'history' | 'simulation' })[]
    },
    candidates: {
      candidateIndex: number
      content: string
      kind: MemoryKind
      personId?: string
      sourceMessageIds: string[]
    }[],
    semantic: MemoryRecord[],
  ) {
    const limits = { perCandidate: 4, perGroup: 8, total: 16, semantic: 8 }
    const selected = new Map<string, { memory: MemoryRecord; candidateIndexes: number[] }>()
    let capped = candidates.length > 8
    if (ctx.simulation || !this.store.scene(ctx.sceneId)?.learningEnabled)
      return { targets: [], coverage: 'complete' as const, limits }
    this.refreshSupport()
    const kindGroup = (kind: MemoryKind) => (['fact', 'claim'].includes(kind) ? 'statement' : kind)
    const groups = new Map<
      string,
      { personId?: string; kind: MemoryKind; candidates: typeof candidates }
    >()
    for (const candidate of candidates.slice(0, 8)) {
      const sources = candidate.sourceMessageIds.map((id) =>
        ctx.sourceEvidence.find(
          (evidence) =>
            evidence.messageId === id &&
            evidence.sceneId === ctx.sceneId &&
            evidence.origin === 'live' &&
            evidence.available &&
            !evidence.isBot &&
            !this.store.recalled(ctx.sceneId, id),
        ),
      )
      if (
        !sources.length ||
        sources.some((source) => !source) ||
        (candidate.personId && !sources.some((source) => source!.senderId === candidate.personId))
      )
        continue
      const key = JSON.stringify([candidate.personId ?? null, kindGroup(candidate.kind)])
      const group = groups.get(key) ?? {
        personId: candidate.personId,
        kind: candidate.kind,
        candidates: [] as typeof candidates,
      }
      group.candidates.push(candidate)
      groups.set(key, group)
    }
    if (groups.size && semantic.length > limits.semantic) capped = true
    for (const group of groups.values()) {
      const kinds = kindGroup(group.kind) === 'statement' ? ['fact', 'claim'] : [group.kind]
      const rows = this.store.db
        .prepare(
          `SELECT * FROM memories
        WHERE scope_key=? AND person_id IS ? AND json_extract(value,'$.kind') IN (SELECT value FROM json_each(?))
          AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined'
          AND json_extract(value,'$.status') IN ('active','disputed')
          AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now'))
        ORDER BY rowid DESC LIMIT ?`,
        )
        .all(
          `scene:${ctx.sceneId}`,
          group.personId ?? null,
          JSON.stringify(kinds),
          limits.perGroup + 1,
        ) as Row[]
      if (rows.length > limits.perGroup) capped = true
      const metadata = rows
        .slice(0, limits.perGroup)
        .map(read)
        .filter((record) => this.supported(record))
      const semanticMatches = semantic.slice(0, limits.semantic).flatMap((record) => {
        const current = this.get(record.id)
        if (!current || !this.supported(current) || !kinds.includes(current.kind)) return []
        if (current.scope.kind === 'scene') {
          if (current.scope.sceneId !== ctx.sceneId || current.scope.personId !== group.personId)
            return []
        } else if (group.personId) return []
        return [current]
      })
      const groupIds = new Set<string>()
      for (const candidate of group.candidates) {
        const tokens = [
          ...new Set(candidate.content.toLowerCase().match(/[\p{L}\p{N}_]+/gu) ?? []),
        ].filter((token) => token.length > 1)
        const score = (record: MemoryRecord) =>
          tokens.filter((token) => record.content.toLowerCase().includes(token)).length
        const ranked = [...metadata].sort((a, b) => score(b) - score(a))
        // Reserve metadata slots before semantic hits: missing vectors cannot erase this path.
        const choices = [
          ...new Map(
            [...ranked.slice(0, 2), ...semanticMatches, ...ranked.slice(2)].map((record) => [
              record.id,
              record,
            ]),
          ).values(),
        ]
        if (choices.length > limits.perCandidate) capped = true
        for (const memory of choices.slice(0, limits.perCandidate)) {
          if (
            (!groupIds.has(memory.id) && groupIds.size >= limits.perGroup) ||
            (!selected.has(memory.id) && selected.size >= limits.total)
          ) {
            capped = true
            continue
          }
          groupIds.add(memory.id)
          const entry = selected.get(memory.id) ?? {
            memory: this.sanitize(memory),
            candidateIndexes: [],
          }
          if (!entry.candidateIndexes.includes(candidate.candidateIndex))
            entry.candidateIndexes.push(candidate.candidateIndex)
          selected.set(memory.id, entry)
        }
      }
    }
    return {
      targets: [...selected.values()],
      coverage: capped ? ('capped' as const) : ('complete' as const),
      limits,
    }
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
    if (!ctx.simulation) this.refreshSupport()
    const terms = memorySearchTerms(query)
    // Validate the selected space even for an empty permitted scope, as before.
    const vector = options ? this.vector(options.spaceId, options.vector) : undefined
    const scope = this.scopeFilter(ctx, personId)
    if (!scope) return []
    const unsupported = ctx.simulation
      ? this.adminList()
          .filter((record) => !this.supported(record))
          .map((record) => record.id)
      : []
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
        WHERE ${scope.sql} AND id NOT IN (SELECT value FROM json_each(?)) AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(value,'$.status') IN ('active','disputed')
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
        JSON.stringify(unsupported),
        ...(terms.length ? [JSON.stringify(terms)] : []),
        ...(options ? [vector!, options.spaceId] : []),
        query.trim() ? 1 : 0,
        Math.trunc(limit),
      ) as { value: string; score: number }[]
    return rows
      .filter((row) => this.supported(JSON.parse(row.value) as MemoryRecord))
      .map((row) => ({
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
      const parents = [...new Set(ctx.dependencyMemoryIds ?? [])]
      if (
        !ctx.provenance ||
        (ctx.provenance === 'contextual' && !ctx.dependencyMemoryIds) ||
        (ctx.provenance === 'source-only' && parents.length) ||
        parents.some((id) => !this.validParent(scope, id))
      )
        return { status: 'rejected', reason: 'invalid_memory_dependencies' }
      const merging = candidate.mergeIntoId !== undefined
      if (merging && (!candidate.mergeIntoId || candidate.supersedesId !== undefined))
        return { status: 'rejected', reason: 'invalid_merge_request' }
      const fp = fingerprint(scope, candidate.content)
      if (this.store.db.prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'manually_deleted' }
      const existingRow = this.store.db
        .prepare(
          merging
            ? 'SELECT * FROM memories WHERE id=?'
            : 'SELECT * FROM memories WHERE fingerprint=?',
        )
        .get(merging ? candidate.mergeIntoId : fp) as Row | undefined
      if (merging) {
        const target = existingRow ? read(existingRow) : undefined
        if (
          target &&
          ctx.provenance === 'source-only' &&
          fp !== fingerprint(target.scope, target.content)
        )
          return { status: 'rejected', reason: 'merge_requires_exact_content' }
        if (
          !target ||
          target.locked ||
          target.origin !== 'learned' ||
          target.status !== 'active' ||
          target.scope.kind !== 'scene' ||
          target.scope.sceneId !== ctx.sceneId ||
          target.scope.personId !== scope.personId ||
          (target.validUntil !== undefined && Date.parse(target.validUntil) <= Date.now()) ||
          target.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)) ||
          this.store.db
            .prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?')
            .get(existingRow!.fingerprint)
        )
          return { status: 'rejected', reason: 'merge_not_allowed' }
      }
      let superseded: MemoryRecord | undefined
      if (candidate.supersedesId) {
        const previous = this.get(candidate.supersedesId)
        const sameScene =
          previous?.scope.kind === 'scene' &&
          previous.scope.sceneId === ctx.sceneId &&
          previous.scope.personId === candidate.personId
        const owningPublicSource =
          previous?.scope.kind === 'sharedPublicKnowledge' &&
          previous.origin === 'learned' &&
          previous.status === 'active' &&
          previous.sources.length > 0 &&
          previous.sources.every((source) => source.sceneId === ctx.sceneId)
        if (
          !previous ||
          previous.locked ||
          previous.origin === 'manual' ||
          !['active', 'disputed'].includes(previous.status) ||
          (!sameScene && !owningPublicSource) ||
          existingRow?.id === previous.id
        )
          return { status: 'rejected', reason: 'correction_not_allowed' }
        superseded = previous
      }
      if (superseded && parents.includes(superseded.id))
        return { status: 'rejected', reason: 'correction_depends_on_superseded_memory' }
      const applyCorrection = () => {
        if (!superseded) return
        this.write({ ...superseded, status: 'superseded' })
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(superseded.id)
      }
      const existing = existingRow ? read(existingRow) : undefined
      if (
        existing &&
        (parents.includes(existing.id) || parents.some((id) => this.dependsOn(id, existing.id)))
      )
        return { status: 'rejected', reason: 'cyclic_memory_dependencies' }
      const history =
        existing && superseded ? this.correctionHistory(existing.id, superseded) : undefined
      const reverting =
        existing?.status === 'superseded' &&
        !existing.locked &&
        existing.origin === 'learned' &&
        existing.sources.length > 0 &&
        !existing.sources.some((source) => this.store.recalled(source.sceneId, source.messageId)) &&
        superseded !== undefined &&
        history !== undefined &&
        candidate.sources.every(
          (source) =>
            !history.some((record) =>
              record.sources.some(
                (old) => old.sceneId === source.sceneId && old.messageId === source.messageId,
              ),
            ),
        )
      if (existingRow && !reverting) {
        const existing = read(existingRow)
        if (
          existing.locked ||
          existing.origin === 'manual' ||
          existing.status === 'superseded' ||
          existing.status === 'invalidated' ||
          existing.lineageStatus === 'quarantined'
        )
          return { status: 'rejected', reason: 'manual_or_invalid_record' }
        if (existing.supersedesId) {
          const history = this.correctionHistory(existing.id, existing)
          if (
            !history ||
            candidate.sources.some(
              (source) =>
                !existing.sources.some(
                  (current) =>
                    current.sceneId === source.sceneId && current.messageId === source.messageId,
                ) &&
                history
                  .slice(1)
                  .some((record) =>
                    record.sources.some(
                      (old) => old.sceneId === source.sceneId && old.messageId === source.messageId,
                    ),
                  ),
            )
          )
            return { status: 'rejected', reason: 'invalid_source_evidence' }
        }
        // Both exact and semantic duplicates preserve the target identity, content, and metadata.
        const sources = this.uniqueSources([...existing.sources, ...candidate.sources])
        const independent = new Set(sources.map((source) => source.senderId)).size
        const next = {
          ...existing,
          sources,
          confidence: Math.min(
            0.85,
            existing.confidence +
              (independent > new Set(existing.sources.map((source) => source.senderId)).size
                ? 0.05
                : 0),
          ),
        }
        this.validate(next)
        applyCorrection()
        this.write(next)
        // Semantic duplicates preserve existing text, so only exact-content
        // corroboration can establish an alternative proof of that text.
        if (ctx.provenance && fp === fingerprint(existing.scope, existing.content)) {
          const supports = this.supports(existing.id)
          supports.push({ sources: this.uniqueSources(candidate.sources), parents })
          this.saveSupports(existing.id, supports)
          next.lineageStatus = 'verified'
          this.write(next)
        }
        if (superseded) this.refreshSupport()
        return { status: 'saved', memory: this.sanitize(next) }
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
        lineageStatus: ctx.provenance ? 'verified' : 'unverified',
        ...(candidate.occurredAt ? { occurredAt: candidate.occurredAt } : {}),
        ...(candidate.validUntil ? { validUntil: candidate.validUntil } : {}),
        ...(candidate.supersedesId ? { supersedesId: candidate.supersedesId } : {}),
      }
      this.validate(memory)
      // A return to an older preference is a new statement, not a resurrection:
      // retain the old version and its sources without creating a supersession cycle.
      if (reverting)
        this.store.db
          .prepare('UPDATE memories SET fingerprint=? WHERE id=?')
          .run(historicalFingerprint(existing!), existing!.id)
      applyCorrection()
      this.write(memory)
      if (ctx.provenance) this.saveSupports(memory.id, [{ sources: memory.sources, parents }])
      if (superseded) this.refreshSupport()
      return { status: 'saved', memory: this.sanitize(memory) }
    })()
  }

  /** Internal engine operation only, after conservative public-technical eligibility review. */
  promotePublic(ctx: MemoryContext, id: string): ProposalResult {
    if (ctx.simulation) return { status: 'rejected', reason: 'simulation_does_not_learn' }
    return this.store.db.transaction((): ProposalResult => {
      if (!this.store.scene(ctx.sceneId)?.learningEnabled)
        return { status: 'rejected', reason: 'learning_disabled' }
      const memory = this.get(id)
      if (
        !memory ||
        memory.origin !== 'learned' ||
        memory.locked ||
        memory.status !== 'active' ||
        memory.scope.kind !== 'scene' ||
        memory.scope.sceneId !== ctx.sceneId ||
        memory.scope.personId !== undefined ||
        memory.lineageStatus !== 'verified' ||
        this.supports(id).some((proof) =>
          proof.parents.some((parent) => this.get(parent)?.scope.kind !== 'sharedPublicKnowledge'),
        ) ||
        !['fact', 'term'].includes(memory.kind) ||
        memory.validUntil !== undefined ||
        !memory.sources.length ||
        memory.sources.some(
          (source) =>
            source.sceneId !== ctx.sceneId || this.store.recalled(source.sceneId, source.messageId),
        )
      )
        return { status: 'rejected', reason: 'public_promotion_not_allowed' }
      const scope: MemoryScope = { kind: 'sharedPublicKnowledge' }
      const fp = fingerprint(scope, memory.content)
      if (this.store.db.prepare('SELECT 1 FROM memory_deleted WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'manually_deleted' }
      if (this.store.db.prepare('SELECT 1 FROM memories WHERE fingerprint=?').get(fp))
        return { status: 'rejected', reason: 'public_memory_already_exists' }
      const promoted: MemoryRecord = { ...memory, scope }
      this.validate(promoted)
      this.write(promoted)
      this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(id)
      return { status: 'saved', memory: this.sanitize(promoted) }
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
      // Revalidation requires an explicit reviewed content field, even when unchanged.
      const reviewedContent = Object.hasOwn(patch, 'content')
      const lockOnly = Object.keys(patch).every((key) => key === 'locked')
      const next: MemoryRecord = {
        ...previous,
        ...patch,
        id: previous.id,
        scope: previous.scope,
        sources: previous.sources,
        origin: lockOnly ? previous.origin : 'manual',
        lineageStatus: reviewedContent ? undefined : previous.lineageStatus,
      }
      this.validate(next)
      if (next.content !== previous.content)
        this.store.db
          .prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
          .run(fingerprint(previous.scope, previous.content), new Date().toISOString())
      if (next.content !== previous.content) this.invalidateDependents(id)
      if (reviewedContent)
        this.store.db.prepare('DELETE FROM memory_supports WHERE memory_id=?').run(id)
      this.write(next)
      this.refreshSupport()
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
      if (memory.origin === 'learned' && memory.scope.kind === 'sharedPublicKnowledge') {
        const deletedAt = new Date().toISOString()
        const insert = this.store.db.prepare('INSERT OR REPLACE INTO memory_deleted VALUES(?,?)')
        for (const sceneId of new Set(memory.sources.map((source) => source.sceneId)))
          insert.run(fingerprint({ kind: 'scene', sceneId }, memory.content), deletedAt)
      }
      this.invalidateDependents(id)
      this.store.db.prepare('DELETE FROM memories WHERE id=?').run(id)
      this.refreshSupport()
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
      const affected = new Set(rows.map((row) => row.id))
      for (const memory of this.adminList()) {
        if (memory.lineageStatus !== 'unverified') continue
        if (
          (memory.scope.kind === 'scene' && memory.scope.sceneId === sceneId) ||
          memory.sources.some((source) => source.sceneId === sceneId)
        ) {
          this.write({ ...memory, lineageStatus: 'quarantined' })
          this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(memory.id)
          affected.add(memory.id)
        }
      }
      for (const id of this.refreshSupport()) affected.add(id)
      return [...affected]
    })()
  }

  /** Authoritative, provenance-safe rebuild view; never replay source messages. */
  indexable(): MemoryRecord[] {
    this.refreshSupport()
    return this.adminList().filter(
      (memory) =>
        (memory.origin === 'manual' || memory.lineageStatus === 'verified') &&
        this.supported(memory),
    )
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
      ...(space.generation ? { generation: space.generation } : {}),
      ...(space.encoderRevision ? { encoderRevision: space.encoderRevision } : {}),
    })
    const id = hash(value)
    this.store.db.prepare('INSERT OR IGNORE INTO vector_spaces VALUES(?,?)').run(id, value)
    return id
  }
  index(id: string, spaceId: string, vector: number[]): void {
    const bytes = this.vector(spaceId, vector)
    const memory = this.get(id)
    if (
      !memory ||
      (memory.origin === 'manual' && memory.lineageStatus === undefined
        ? !['active', 'disputed'].includes(memory.status)
        : !this.supported(memory))
    ) {
      this.refreshSupport()
      throw new Error('memory_not_indexable')
    }
    this.store.db
      .prepare(
        'INSERT INTO memory_vectors VALUES(?,?,?) ON CONFLICT(memory_id,space_id) DO UPDATE SET embedding=excluded.embedding',
      )
      .run(id, spaceId, bytes)
  }
  private supports(id: string): Support[] {
    const row = this.store.db
      .prepare('SELECT value FROM memory_supports WHERE memory_id=?')
      .get(id) as { value: string } | undefined
    return row ? (JSON.parse(row.value) as Support[]) : []
  }
  private saveSupports(id: string, supports: Support[]) {
    // Replaying identical evidence must neither inflate confidence nor append
    // unbounded duplicate proofs. Keep genuinely distinct alternatives/history.
    const unique = new Map<string, Support>()
    for (const proof of supports) {
      const key = JSON.stringify([
        proof.sources
          .map((source) => JSON.stringify([source.sceneId, source.messageId, source.senderId]))
          .sort(),
        [...proof.parents].sort(),
        proof.invalidatedBy ?? null,
      ])
      unique.set(key, proof)
    }
    this.store.db
      .prepare(
        'INSERT INTO memory_supports VALUES(?,?) ON CONFLICT(memory_id) DO UPDATE SET value=excluded.value',
      )
      .run(id, JSON.stringify([...unique.values()]))
  }
  private validParent(scope: MemoryScope, id: string): boolean {
    const parent = this.get(id)
    if (!parent || !this.supported(parent)) return false
    if (parent.scope.kind === 'sharedPublicKnowledge') return true
    return (
      scope.kind === 'scene' &&
      parent.scope.sceneId === scope.sceneId &&
      (parent.scope.personId === undefined || parent.scope.personId === scope.personId)
    )
  }
  private dependsOn(id: string, wanted: string, seen = new Set<string>()): boolean {
    if (id === wanted) return true
    if (seen.has(id)) return false
    seen.add(id)
    return this.supports(id).some((proof) =>
      proof.parents.some((parent) => this.dependsOn(parent, wanted, seen)),
    )
  }
  private supported(
    memory: MemoryRecord,
    path = new Set<string>(),
    cache = new Map<string, boolean>(),
  ): boolean {
    if (
      !['active', 'disputed'].includes(memory.status) ||
      memory.lineageStatus === 'quarantined' ||
      (memory.validUntil !== undefined && Date.parse(memory.validUntil) <= Date.now()) ||
      path.has(memory.id)
    )
      return false
    if (cache.has(memory.id)) return cache.get(memory.id)!
    if (memory.lineageStatus !== 'verified') return true
    const nextPath = new Set(path).add(memory.id)
    const supported = this.supports(memory.id).some(
      (proof) =>
        !proof.invalidatedBy &&
        proof.sources.length > 0 &&
        proof.sources.every((source) => !this.store.recalled(source.sceneId, source.messageId)) &&
        proof.parents.every((id) => {
          const parent = this.get(id)
          return parent !== undefined && this.supported(parent, nextPath, cache)
        }),
    )
    cache.set(memory.id, supported)
    return supported
  }
  /** Re-evaluate proof alternatives atomically; locks never override recalled evidence. */
  private refreshSupport(): string[] {
    return this.store.db.transaction(() => {
      const invalidated: string[] = []
      const cache = new Map<string, boolean>()
      const rows = this.store.db
        .prepare(
          `SELECT * FROM memories
        WHERE json_extract(value,'$.lineageStatus')='verified'
          AND json_extract(value,'$.status') IN ('active','disputed')`,
        )
        .all() as Row[]
      for (const memory of rows.map(read)) {
        if (
          memory.lineageStatus !== 'verified' ||
          !['active', 'disputed'].includes(memory.status) ||
          this.supported(memory, new Set(), cache)
        )
          continue
        this.write({ ...memory, status: 'invalidated' })
        this.store.db.prepare('DELETE FROM memory_vectors WHERE memory_id=?').run(memory.id)
        invalidated.push(memory.id)
      }
      return invalidated
    })()
  }
  private invalidateDependents(id: string) {
    // Replacing/deleting a premise invalidates only proofs requiring it. Other
    // independently sourced proofs remain valid; evidence history is retained.
    for (const memory of this.adminList()) {
      const supports = this.supports(memory.id)
      if (!supports.some((proof) => proof.parents.includes(id))) continue
      this.saveSupports(
        memory.id,
        supports.map((proof) =>
          proof.parents.includes(id) ? { ...proof, invalidatedBy: id } : proof,
        ),
      )
    }
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
        `SELECT * FROM memories WHERE ${scope.sql} AND COALESCE(json_extract(value,'$.lineageStatus'),'') != 'quarantined' AND json_extract(value,'$.status') IN ('active','disputed') AND (json_extract(value,'$.validUntil') IS NULL OR julianday(json_extract(value,'$.validUntil')) > julianday('now')) ORDER BY rowid DESC`,
      )
      .all(...scope.params) as Row[]
  }
  private get(id: string): MemoryRecord | undefined {
    const row = this.store.db.prepare('SELECT * FROM memories WHERE id=?').get(id) as
      Row | undefined
    return row ? read(row) : undefined
  }
  private correctionHistory(id: string, current: MemoryRecord): MemoryRecord[] | undefined {
    const visited = new Set<string>()
    const history: MemoryRecord[] = []
    let found = false
    let previous: MemoryRecord | undefined = current
    while (previous && !visited.has(previous.id)) {
      history.push(previous)
      if (previous.id === id) found = true
      visited.add(previous.id)
      if (!previous.supersedesId) break
      previous = this.get(previous.supersedesId)
      if (!previous) return undefined
    }
    return found && previous && !previous.supersedesId ? history : undefined
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
    const stored = this.store.db
      .prepare('SELECT fingerprint FROM memories WHERE id=?')
      .get(memory.id) as { fingerprint: string } | undefined
    // Historical source retractions and lock changes must not reclaim the current
    // version's unique content key. Explicit manual reactivation still checks it.
    const fp =
      ['superseded', 'invalidated'].includes(memory.status) &&
      stored?.fingerprint === historicalFingerprint(memory)
        ? stored.fingerprint
        : fingerprint(memory.scope, memory.content)
    this.store.db
      .prepare(
        'INSERT INTO memories VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET scope_key=excluded.scope_key,person_id=excluded.person_id,content=excluded.content,fingerprint=excluded.fingerprint,value=excluded.value',
      )
      .run(
        memory.id,
        scopeKey(memory.scope),
        memory.scope.kind === 'scene' ? (memory.scope.personId ?? null) : null,
        memory.content,
        fp,
        JSON.stringify(memory),
      )
    this.store.db.prepare('DELETE FROM memory_sources WHERE memory_id=?').run(memory.id)
    const insert = this.store.db.prepare('INSERT INTO memory_sources VALUES(?,?,?,?)')
    for (const source of memory.sources)
      insert.run(memory.id, source.sceneId, source.messageId, source.senderId)
  }
}
