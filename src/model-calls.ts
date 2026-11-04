import { z } from '@hono/zod-openapi'
import type Database from 'better-sqlite3'
import type { ModelLog } from './models.ts'
import { canonicalModelRole } from './model-roles.ts'
import { validTokenCount } from './model-cost.ts'

/** Metadata only: never persist prompts, outputs, error text, credentials or scene/person IDs. */
export interface ModelCallObservation extends ModelLog {
  at: string
  attempt: number
  transportMode?: 'stream' | 'non_stream' | 'custom' | 'unknown'
  firstTokenMs?: number | null
  firstTokenKind?: 'text' | 'reasoning' | 'tool' | null
  failureReason?:
    | 'cancelled'
    | 'first_content_timeout'
    | 'request_timeout'
    | 'stream_idle_timeout'
    | 'stream_interrupted'
    | 'request_failed'
    | null
}
const count = z.number().int().nonnegative().nullable()
export const ModelCallSchema = z.object({
  id: z.number().int().positive(),
  at: z.string(),
  modelId: z.string(),
  providerId: z.string(),
  apiModelName: z.string(),
  role: z.string(),
  status: z.enum(['completed', 'failed']),
  attempt: z.number().int().positive(),
  inputTokens: count,
  outputTokens: count,
  cacheReadInputTokens: count,
  cacheWriteInputTokens: count,
  durationMs: z.number().nonnegative(),
  firstTokenMs: z.number().nonnegative().nullable(),
  firstTokenKind: z.enum(['text', 'reasoning', 'tool']).nullable(),
  transportMode: z.enum(['stream', 'non_stream', 'custom', 'unknown']),
  failureReason: z
    .enum([
      'cancelled',
      'first_content_timeout',
      'request_timeout',
      'stream_idle_timeout',
      'stream_interrupted',
      'request_failed',
    ])
    .nullable(),
  tokensPerSecond: z.number().nonnegative().nullable(),
})
export const ModelCallsQuerySchema = z.object({
  limit: z.coerce.number().int().min(1).max(100).default(50),
  beforeId: z.coerce.number().int().positive().optional(),
  role: z.enum(['planner', 'replyer', 'learning', 'vision', 'embedding']).optional(),
  modelId: z.string().max(200).optional(),
  status: z.enum(['completed', 'failed']).optional(),
})
export const ModelCallsPageSchema = z.object({
  items: z.array(ModelCallSchema),
  nextCursor: z.number().int().positive().nullable(),
  retentionDays: z.number(),
  maxRecords: z.number(),
})
export type ModelCallsQuery = z.infer<typeof ModelCallsQuerySchema>
const RETENTION_DAYS = 30,
  MAX_RECORDS = 100_000

export function initializeModelCalls(db: Database.Database) {
  db.exec(`CREATE TABLE IF NOT EXISTS model_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, model_id TEXT NOT NULL,
    provider_id TEXT NOT NULL, api_model TEXT NOT NULL, role TEXT NOT NULL, status TEXT NOT NULL,
    attempt INTEGER NOT NULL, input_tokens INTEGER, output_tokens INTEGER,
    cache_read_tokens INTEGER, cache_write_tokens INTEGER, duration_ms REAL NOT NULL
  ); CREATE INDEX IF NOT EXISTS model_calls_at ON model_calls(at);
  CREATE INDEX IF NOT EXISTS model_calls_role_id ON model_calls(role,id);
  CREATE INDEX IF NOT EXISTS model_calls_model_id ON model_calls(model_id,id);`)
  const columns = new Set(
    (db.pragma('table_info(model_calls)') as Array<{ name: string }>).map((row) => row.name),
  )
  db.transaction(() => {
    if (!columns.has('transport_mode'))
      db.exec("ALTER TABLE model_calls ADD COLUMN transport_mode TEXT NOT NULL DEFAULT 'unknown'")
    if (!columns.has('first_token_ms'))
      db.exec('ALTER TABLE model_calls ADD COLUMN first_token_ms REAL')
    if (!columns.has('failure_reason'))
      db.exec('ALTER TABLE model_calls ADD COLUMN failure_reason TEXT')
    if (!columns.has('first_token_kind'))
      db.exec('ALTER TABLE model_calls ADD COLUMN first_token_kind TEXT')
  })()
}
export function recordModelCall(db: Database.Database, entry: ModelCallObservation) {
  if (entry.role === 'models') return
  const usage = entry.usage
  const known = usage && usage.known !== false
  const token = (value: unknown) => (known && validTokenCount(value) ? (value as number) : null)
  const duration = Number.isFinite(entry.durationMs) ? Math.max(0, entry.durationMs) : 0
  db.transaction(() => {
    db.prepare(
      `INSERT INTO model_calls(at,model_id,provider_id,api_model,role,status,attempt,
      input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,transport_mode,first_token_ms,first_token_kind,failure_reason)
      VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
    ).run(
      entry.at,
      entry.modelId,
      entry.providerId,
      entry.pricing?.apiModelName ?? 'unknown',
      canonicalModelRole(entry.role),
      entry.event === 'model.completed' ? 'completed' : 'failed',
      entry.attempt,
      token(usage?.inputTokens),
      token(usage?.outputTokens),
      token(usage?.cacheReadInputTokens),
      token(usage?.cacheWriteInputTokens),
      duration,
      entry.transportMode ?? 'unknown',
      entry.transportMode === 'stream' &&
        typeof entry.firstTokenMs === 'number' &&
        Number.isFinite(entry.firstTokenMs) &&
        entry.firstTokenMs >= 0
        ? entry.firstTokenMs
        : null,
      entry.transportMode === 'stream' ? (entry.firstTokenKind ?? null) : null,
      entry.failureReason ?? null,
    )
    db.prepare('DELETE FROM model_calls WHERE at < ?').run(
      new Date(Date.now() - RETENTION_DAYS * 86400000).toISOString(),
    )
    // AUTOINCREMENT IDs are monotonic: index seek rather than count/offset scan per call.
    const latest = db.prepare('SELECT MAX(id) AS id FROM model_calls').get() as { id: number }
    db.prepare('DELETE FROM model_calls WHERE id <= ?').run(latest.id - MAX_RECORDS)
  })()
}
export function modelCalls(db: Database.Database, query: ModelCallsQuery) {
  const clauses = ['at >= @cutoff'],
    params: Record<string, string | number> = {
      cutoff: new Date(Date.now() - RETENTION_DAYS * 86400000).toISOString(),
      limit: query.limit + 1,
    }
  for (const [key, column] of [
    ['role', 'role'],
    ['modelId', 'model_id'],
    ['status', 'status'],
  ] as const)
    if (query[key] !== undefined) {
      clauses.push(`${column}=@${key}`)
      params[key] = query[key]!
    }
  if (query.beforeId !== undefined) {
    clauses.push('id < @beforeId')
    params.beforeId = query.beforeId
  }
  const rows = db
    .prepare(
      `SELECT id,at,model_id AS modelId,provider_id AS providerId,
    api_model AS apiModelName,role,status,attempt,input_tokens AS inputTokens,
    output_tokens AS outputTokens,cache_read_tokens AS cacheReadInputTokens,
    cache_write_tokens AS cacheWriteInputTokens,duration_ms AS durationMs,transport_mode AS transportMode,
    first_token_ms AS firstTokenMs,first_token_kind AS firstTokenKind,failure_reason AS failureReason
    FROM model_calls WHERE ${clauses.join(' AND ')} ORDER BY id DESC LIMIT @limit`,
    )
    .all(params) as Array<Omit<z.infer<typeof ModelCallSchema>, 'tokensPerSecond'>>
  const hasMore = rows.length > query.limit
  const items = rows.slice(0, query.limit).map((row) => ({
    ...row,
    tokensPerSecond:
      row.status === 'completed' && row.outputTokens !== null && row.durationMs > 0
        ? (row.outputTokens * 1000) / row.durationMs
        : null,
  }))
  return {
    items,
    nextCursor: hasMore ? items.at(-1)!.id : null,
    retentionDays: RETENTION_DAYS,
    maxRecords: MAX_RECORDS,
  }
}
