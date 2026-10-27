import { createHash, randomUUID } from 'node:crypto'
import type { Store } from './store.ts'
import type { MemorySource, SourceEvidence } from './memory.ts'

export interface HabitContext {
  sceneId: string
  simulation: boolean
  sourceEvidence?: (SourceEvidence & {
    origin: 'live' | 'history'
    text?: string
    timestamp?: number
  })[]
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
  context?: 'technical'
  evidenceVersion?: 2
  sourceCutoff?: number
  authorWatermarks?: Record<string, number>
  sources: (MemorySource & { direction: 1 | -1; timestamp?: number; authorKey?: string })[]
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
const fingerprint = (ctx: HabitContext, input: HabitInput, scope?: string, context?: string) =>
  createHash('sha256')
    .update(
      JSON.stringify(
        input.kind === 'sceneRule'
          ? ['scene', ctx.sceneId, input.content.trim().replace(/\s+/g, ' ').toLowerCase()]
          : ['bot-v2', scope ?? 'shared', context ?? 'general', input.preference],
      ),
    )
    .digest('hex')

export const DEFAULT_HABIT_POLICY = { halfLifeDays: 30, retentionDays: 90, maxSources: 128 }
export interface HabitOptions {
  now?: () => number
  policy?: Partial<typeof DEFAULT_HABIT_POLICY>
}
const DAY = 86_400_000

/** Soft learned hints only: consumers must always give fixed and manual rules precedence. */
export class HabitService {
  private readonly now: () => number
  private readonly policy: typeof DEFAULT_HABIT_POLICY
  constructor(
    private readonly store: Store,
    options: HabitOptions = {},
  ) {
    this.now = options.now ?? Date.now
    this.policy = { ...DEFAULT_HABIT_POLICY, ...options.policy }
    const p = this.policy
    if (
      !Number.isFinite(p.halfLifeDays) ||
      p.halfLifeDays < 1 ||
      p.halfLifeDays > 365 ||
      !Number.isFinite(p.retentionDays) ||
      p.retentionDays < p.halfLifeDays ||
      p.retentionDays > 730 ||
      !Number.isInteger(p.maxSources) ||
      p.maxSources < 8 ||
      p.maxSources > 512
    )
      throw Error('invalid_habit_policy')
    store.db
      .exec(`CREATE TABLE IF NOT EXISTS habits (id TEXT PRIMARY KEY, fingerprint TEXT UNIQUE NOT NULL, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS habit_deleted (fingerprint TEXT PRIMARY KEY);`)
  }
  adminList(): HabitRecord[] {
    return (
      this.store.db.prepare('SELECT value FROM habits ORDER BY rowid').all() as { value: string }[]
    ).map((row) => {
      const record = JSON.parse(row.value) as HabitRecord
      this.recalculate(record)
      return record
    })
  }
  list(ctx: HabitContext): HabitRecord[] {
    return this.adminList()
      .filter(
        (record) =>
          record.status === 'active' &&
          ((record.kind === 'botExpression' && !record.sceneId) ||
            (!ctx.simulation && record.sceneId === ctx.sceneId)),
      )
      .map((record) => this.sanitize(record))
  }
  // Time-derived magnitudes are not revisions. Keep raw administrative changes,
  // effective evidence expiry/recall, scope and policy in the trusted identity.
  validationIdentity(ctx: HabitContext, id: string): string | undefined {
    const raw = this.get(id)
    if (!raw) return
    const effective = structuredClone(raw)
    this.recalculate(effective)
    if (
      effective.status !== 'active' ||
      !(
        (effective.kind === 'botExpression' && !effective.sceneId) ||
        (!ctx.simulation && effective.sceneId === ctx.sceneId)
      ) ||
      effective.sources.some((source) => this.store.recalled(source.sceneId, source.messageId))
    )
      return
    const { weight: _weight, confidence: _confidence, ...stable } = effective
    return createHash('sha256')
      .update(JSON.stringify({ raw, effective: stable, policy: this.policy }))
      .digest('hex')
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
      const originalFingerprint = fingerprint(ctx, input)
      const legacyFingerprint =
        input.kind === 'botExpression'
          ? createHash('sha256')
              .update(JSON.stringify(['bot', input.preference]))
              .digest('hex')
          : originalFingerprint
      if (
        this.adminList().some(
          (r) =>
            r.locked &&
            r.evidenceVersion !== 2 &&
            r.kind === input.kind &&
            (input.kind === 'botExpression'
              ? r.preference === input.preference ||
                (['concise', 'detail'].includes(r.preference ?? '') &&
                  ['concise', 'detail'].includes(input.preference))
              : r.sceneId === ctx.sceneId && r.content === input.content),
        )
      )
        return { status: 'rejected', reason: 'locked_legacy_candidate' }
      const prepared = this.prepare(ctx, input)
      if (typeof prepared === 'string') return { status: 'rejected', reason: prepared }
      input = prepared.input
      const fp = fingerprint(ctx, input, prepared.sceneId, prepared.context)
      if (
        [
          fp,
          originalFingerprint,
          legacyFingerprint,
          ...(input.kind === 'botExpression' && input.preference === 'concise'
            ? ['concise', 'detail'].map((preference) =>
                createHash('sha256')
                  .update(JSON.stringify(['bot', preference]))
                  .digest('hex'),
              )
            : []),
        ].some((candidate) =>
          this.store.db.prepare('SELECT 1 FROM habit_deleted WHERE fingerprint=?').get(candidate),
        )
      )
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
            ...(prepared.sceneId ? { sceneId: prepared.sceneId } : {}),
            ...(prepared.context ? { context: prepared.context } : {}),
            evidenceVersion: 2,
            sources: [],
            weight: 0,
            confidence: 0,
            status: 'active',
            locked: false,
            learnedAt: new Date(this.now()).toISOString(),
          }
      if (record.locked || record.status === 'invalidated')
        return { status: 'rejected', reason: 'locked_or_invalidated' }
      this.add(ctx, record, input.sources, input.kind === 'sceneRule' ? 1 : input.direction)
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
      if (!record || (record.sceneId && record.sceneId !== ctx.sceneId))
        return { status: 'rejected', reason: 'not_available' }
      if (record.locked || record.status === 'invalidated' || ![1, -1].includes(input.direction))
        return { status: 'rejected', reason: 'locked_or_invalid_feedback' }
      if (record.kind === 'botExpression') {
        const prepared = this.prepare(ctx, {
          kind: 'botExpression',
          preference: record.preference!,
          ...input,
        })
        if (
          typeof prepared === 'string' ||
          prepared.sceneId !== record.sceneId ||
          prepared.context !== record.context
        )
          return { status: 'rejected', reason: 'incompatible_evidence_scope' }
      }
      this.add(ctx, record, input.sources, input.direction)
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
        if (!remaining.length) record.status = 'invalidated'
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
              e.origin === 'live' &&
              Number.isFinite(e.timestamp) &&
              e.timestamp! <= this.now() + 300_000 &&
              e.timestamp! > this.now() - this.policy.retentionDays * DAY,
          ),
      )
    )
      return 'invalid_source_evidence'
  }
  private prepare(
    ctx: HabitContext,
    input: HabitInput,
  ): { input: HabitInput; sceneId?: string; context?: 'technical' } | string {
    const texts = input.sources.map(
      (s) => ctx.sourceEvidence?.find((e) => key(e) === key(s))?.text ?? '',
    )
    // Refuse ambiguous scope rather than trusting the model's proposed target.
    const limited =
      /那张|这张|该图|少用它|这次|今天|对我|给我|我个人|this (?:image|meme|sticker|time)|that (?:image|meme|sticker)|for me|personally|today|just kidding|开玩笑|引用|quote/i
    if (texts.some((text) => !text.trim() || limited.test(text)))
      return 'ambiguous_expression_scope'
    if (input.kind === 'sceneRule') {
      // Canonicalize only the explicitly supported technical-verbosity axis, not arbitrary rules.
      if (
        texts.every((text) => !limited.test(text)) &&
        /技术|technical/i.test(input.content) &&
        /详细|完整|简短|简洁|detail|concise|short repl/i.test(input.content) &&
        texts.every((text) => /详细|完整|简短|简洁|短答案|步骤|detail|concise|short/i.test(text)) &&
        (texts.some((text) => /技术|technical/i.test(text)) ||
          this.adminList().some(
            (r) =>
              r.sceneId === ctx.sceneId &&
              r.context === 'technical' &&
              r.status === 'active' &&
              input.sources.every((s) =>
                r.sources.some((prior) => this.author(prior) === this.author(s)),
              ),
          ))
      ) {
        const detail = /详细|完整|detail/i.test(input.content)
        return {
          input: {
            kind: 'botExpression',
            preference: 'concise',
            direction: detail ? -1 : 1,
            sources: input.sources,
          },
          sceneId: ctx.sceneId,
          context: 'technical',
        }
      }
      return { input, sceneId: ctx.sceneId }
    }
    if (texts.some((text) => !text.trim() || limited.test(text)))
      return 'ambiguous_expression_scope'
    const axis =
      input.preference === 'concise' || input.preference === 'detail'
        ? /简短|简洁|详细|完整|短答案|步骤|concise|detail|short|brief/i
        : input.preference === 'meme_frequency'
          ? /表情包|表情|meme|sticker/i
          : input.preference === 'emoji'
            ? /emoji|表情/i
            : /问题|提问|反问|question/i
    if (texts.some((text) => !axis.test(text))) return 'unsupported_expression_target'
    const technical = texts.some((text) => /技术|technical/i.test(text))
    let local =
      technical ||
      texts.some((text) => /本群|群里|这个群|in this (?:group|chat)|our group/i.test(text))
    const general = texts.every(
      (text) =>
        /机器人|bot|assistant/i.test(text) &&
        /以后|通常|默认|总是|generally|always|by default|from now on/i.test(text),
    )
    if (!local && !general) {
      if (texts.some((text) => /机器人|bot|assistant/i.test(text))) local = true
      else return 'ambiguous_expression_scope'
    }
    return {
      input:
        input.preference === 'detail'
          ? { ...input, preference: 'concise', direction: input.direction === 1 ? -1 : 1 }
          : input,
      ...(local ? { sceneId: ctx.sceneId } : {}),
      ...(technical ? { context: 'technical' as const } : {}),
    }
  }
  private author(source: MemorySource): string {
    const scene = this.store.scene(source.sceneId)
    // Adapter/account namespaces identify authenticated platform users, not display names.
    return JSON.stringify([
      scene?.adapterId ?? source.sceneId,
      scene?.accountId ?? source.sceneId,
      source.senderId,
    ])
  }
  private add(ctx: HabitContext, record: HabitRecord, sources: MemorySource[], direction: 1 | -1) {
    const now = this.now()
    for (const source of sources) {
      const evidence = ctx.sourceEvidence!.find((e) => key(e) === key(source))!
      const timestamp = evidence.timestamp!
      if (
        !Number.isFinite(timestamp) ||
        timestamp > now + 300_000 ||
        timestamp <=
          Math.max(record.sourceCutoff ?? -Infinity, now - this.policy.retentionDays * DAY)
      )
        continue
      const authorKey = this.author(source)
      const authorHash = createHash('sha256').update(authorKey).digest('hex')
      if (timestamp <= (record.authorWatermarks?.[authorHash] ?? -Infinity)) continue
      const previous = record.sources.find((s) => (s.authorKey ?? this.author(s)) === authorKey)
      // Same-source replay cannot change direction, refresh age, or manufacture new support.
      if (previous && (key(previous) === key(source) || timestamp <= (previous.timestamp ?? 0)))
        continue
      record.sources = record.sources.filter((s) => (s.authorKey ?? this.author(s)) !== authorKey)
      record.sources.push({ ...source, direction, timestamp, authorKey })
      record.authorWatermarks ??= {}
      record.authorWatermarks[authorHash] = timestamp
    }
    const watermarks = Object.entries(record.authorWatermarks ?? {}).sort((a, b) => b[1] - a[1])
    const discardedMarks = watermarks.slice(this.policy.maxSources)
    if (discardedMarks.length)
      record.sourceCutoff = Math.max(
        record.sourceCutoff ?? 0,
        ...discardedMarks.map(([, time]) => time),
      )
    record.authorWatermarks = Object.fromEntries(watermarks.slice(0, this.policy.maxSources))
    record.evidenceVersion = 2
    record.sources.sort((a, b) => (b.timestamp ?? 0) - (a.timestamp ?? 0))
    const dropped = record.sources.slice(this.policy.maxSources)
    if (dropped.length)
      record.sourceCutoff = Math.max(
        record.sourceCutoff ?? 0,
        ...dropped.map((s) => s.timestamp ?? 0),
      )
    record.sources = record.sources.slice(0, this.policy.maxSources)
    this.recalculate(record)
  }
  private recalculate(record: HabitRecord) {
    if (record.evidenceVersion !== 2) {
      // Preserve legacy evidence for administration; do not infer its missing scope or chronology.
      record.weight = record.confidence = 0
      if (record.status !== 'invalidated') record.status = 'disputed'
      return
    }
    const now = this.now()
    record.sources = record.sources.filter(
      (s) => s.timestamp !== undefined && now - s.timestamp < this.policy.retentionDays * DAY,
    )
    const net = record.sources.reduce(
      (sum, s) =>
        sum +
        s.direction * 2 ** (-Math.max(0, now - s.timestamp!) / (this.policy.halfLifeDays * DAY)),
      0,
    )
    record.confidence = Math.min(0.75, Math.abs(net) / (record.sources.length + 2))
    record.weight = Math.sign(net) * Math.min(0.3, Math.abs(net) * 0.05)
    if (record.status !== 'invalidated')
      record.status =
        Math.abs(net) < 1e-9 || (record.kind === 'sceneRule' && net < 0) ? 'disputed' : 'active'
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
    const { authorWatermarks: _watermarks, ...visible } = record
    return record.kind === 'botExpression' ? { ...visible, sources: [] } : visible
  }
}
