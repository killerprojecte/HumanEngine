import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'
import { validateKnowledgePredicate } from './registry.ts'
import type Database from 'better-sqlite3'
import { z } from 'zod'
import { SourceEventRefSchema } from '../domain/events.ts'
import { parentBindingState } from '../domain/proofs.ts'
import {
  EntityKindSchema,
  KnowledgeRefSchema,
  KnowledgeProposalSchema,
  type KnowledgeEdge,
  type KnowledgeEntity,
} from './contracts.ts'
const id = z.string().min(1).max(512),
  time = z.iso.datetime({ offset: true })
const source = z
  .object({ sceneId: id, messageId: id, senderId: id, eventRef: SourceEventRefSchema.optional() })
  .strict()
const witness = z
  .object({
    kind: z.enum(['self', 'mentioned', 'legacy_sender', 'unresolved']),
    personId: id.optional(),
    sourceMessageId: id.optional(),
    mentionPartIndex: z.number().int().nonnegative().optional(),
  })
  .strict()
const proof = z
  .object({
    proofId: id,
    sources: z.array(source).max(128),
    parents: z.array(id).max(128),
    parentReferences: z.array(KnowledgeRefSchema).max(128).optional(),
    subjectWitness: witness.optional(),
    sourceObservations: z
      .array(source.extend({ sentAt: time.optional(), receivedAt: time.optional() }))
      .max(128)
      .optional(),
    invalidatedBy: id.optional(),
  })
  .strict()
  .refine((p) => parentBindingState(p) !== 'invalid')
const entity = z
  .object({
    id,
    sceneId: id,
    kind: EntityKindSchema,
    title: z.string().min(1).max(500),
    backing: KnowledgeRefSchema,
    originEdgeId: id,
  })
  .strict()
const literal = z.union([
  z
    .object({
      kind: z.literal('literal'),
      valueType: z.literal('text'),
      value: z.string().min(1).max(2000),
    })
    .strict(),
  z
    .object({
      kind: z.literal('literal'),
      valueType: z.literal('number'),
      value: z.number().finite(),
      unit: z.string().min(1).max(80).nullable(),
    })
    .strict(),
  z
    .object({ kind: z.literal('literal'), valueType: z.literal('boolean'), value: z.boolean() })
    .strict(),
])
const endpoint = z.union([
  z.object({ kind: z.literal('person'), personId: id }).strict(),
  z.object({ kind: z.literal('entity'), entity }).strict(),
  literal,
])
export const KnowledgeEdgeSchema = z
  .object({
    id,
    version: z.number().int().positive(),
    sceneId: id,
    registryVersion: z.literal(1),
    predicate: KnowledgeProposalSchema.shape.predicate,
    property: KnowledgeProposalSchema.shape.property,
    subject: endpoint,
    object: endpoint,
    polarity: KnowledgeProposalSchema.shape.polarity,
    modality: KnowledgeProposalSchema.shape.modality,
    temporalScope: KnowledgeProposalSchema.shape.temporalScope,
    conditions: z.array(z.string().min(1).max(2000)).max(8),
    validFrom: time.optional(),
    validUntil: time.optional(),
    backing: KnowledgeRefSchema,
    backingFingerprint: z.string().regex(/^[a-f0-9]{64}$/),
    proofs: z.array(proof).min(1).max(128),
    spans: z
      .array(
        z
          .object({
            source,
            rootId: z.string().regex(/^[a-f0-9]{64}$/),
            partIndex: z.number().int().nonnegative(),
            start: z.number().int().nonnegative(),
            end: z.number().int().positive(),
            spanHash: z.string().regex(/^[a-f0-9]{64}$/),
            stance: z.enum(['self', 'asserted', 'reported', 'quoted']),
          })
          .strict(),
      )
      .min(1)
      .max(8),
    dependencies: z
      .array(
        z
          .object({
            entity,
            backingFingerprint: z.string().regex(/^[a-f0-9]{64}$/),
            proofs: z.array(proof).min(1).max(128),
          })
          .strict(),
      )
      .max(2),
    readDependencies: z
      .array(
        z
          .object({
            edgeId: id.optional(),
            edgeVersion: z.number().int().positive().max(Number.MAX_SAFE_INTEGER).optional(),
            backing: KnowledgeRefSchema,
            backingFingerprint: z.string().regex(/^[a-f0-9]{64}$/),
            proofs: z.array(proof).min(1).max(128),
          })
          .strict()
          .refine((d) => (d.edgeId === undefined) === (d.edgeVersion === undefined)),
      )
      .max(168),
    status: z.enum(['candidate', 'accepted']),
    admission: z.enum([
      'qualified_candidate',
      'self_explicit_no_visible_conflict',
      'direct_assertion_no_visible_conflict',
    ]),
    admissionPolicyVersion: z.literal(1),
    createdAt: time,
  })
  .strict()
  .superRefine((edge, ctx) => {
    const invalid = () =>
      ctx.addIssue({ code: 'custom', message: 'knowledge_storage_inconsistent' })
    try {
      validateKnowledgePredicate(edge.predicate, edge.subject, edge.object, edge.property)
    } catch {
      invalid()
    }
    if ((edge.predicate === 'has_property') !== (edge.property !== undefined)) invalid()
    if (
      edge.validFrom &&
      edge.validUntil &&
      Date.parse(edge.validFrom) > Date.parse(edge.validUntil)
    )
      invalid()
    for (const span of edge.spans) {
      const root = createHash('sha256')
        .update(JSON.stringify([edge.sceneId, span.source.messageId]))
        .digest('hex')
      if (
        span.source.sceneId !== edge.sceneId ||
        span.rootId !== root ||
        span.start >= span.end ||
        span.end > 1000000 ||
        span.partIndex > 4095 ||
        !edge.proofs.some((p) => p.sources.some((s) => isDeepStrictEqual(s, span.source)))
      )
        invalid()
    }
    for (const point of [edge.subject, edge.object])
      if (point.kind === 'entity') {
        if (point.entity.sceneId !== edge.sceneId) invalid()
        if (point.entity.originEdgeId === edge.id) {
          if (!isDeepStrictEqual(point.entity.backing, edge.backing)) invalid()
        } else if (!edge.dependencies.some((d) => isDeepStrictEqual(d.entity, point.entity)))
          invalid()
      }
    if (edge.dependencies.some((d) => d.entity.sceneId !== edge.sceneId)) invalid()
  })

export class KnowledgeStorage {
  constructor(
    readonly db: Database.Database,
    private readonly ready: () => void,
    private readonly beforeWrite?: () => void,
  ) {}
  private exists() {
    this.ready()
    return !!this.db
      .prepare("SELECT 1 FROM sqlite_master WHERE name='memory_knowledge_edges' AND type='table'")
      .get()
  }
  requireTransaction() {
    this.ready()
    if (!this.db.inTransaction) throw new Error('knowledge_transaction_required')
  }
  private writeBoundary() {
    const result: unknown = this.beforeWrite?.()
    if (result && typeof (result as { then?: unknown }).then === 'function')
      throw new Error('knowledge_async_write_hook')
  }
  private initialize() {
    this.requireTransaction()
    this.db.exec(
      'CREATE TABLE IF NOT EXISTS memory_knowledge_edges(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,assertion_id TEXT NOT NULL,admission_key TEXT NOT NULL UNIQUE,value TEXT NOT NULL); CREATE INDEX IF NOT EXISTS memory_knowledge_scene ON memory_knowledge_edges(scene_id); CREATE INDEX IF NOT EXISTS memory_knowledge_assertion ON memory_knowledge_edges(assertion_id);',
    )
  }
  private decode(row: unknown): KnowledgeEdge {
    if (!row || typeof row !== 'object') throw new Error('knowledge_storage_corrupt')
    const r = row as { id: unknown; scene_id: unknown; assertion_id: unknown; value: unknown }
    if (typeof r.value !== 'string') throw new Error('knowledge_storage_corrupt')
    try {
      const e = KnowledgeEdgeSchema.parse(JSON.parse(r.value))
      if (e.id !== r.id || e.sceneId !== r.scene_id || e.backing.id !== r.assertion_id)
        throw new Error()
      return e
    } catch {
      throw new Error('knowledge_storage_corrupt')
    }
  }
  rows(sceneId?: string, limit?: number): KnowledgeEdge[] {
    if (!this.exists()) return []
    const where = sceneId === undefined ? '' : ' WHERE scene_id=?',
      suffix = limit === undefined ? '' : ' LIMIT ?'
    const args: unknown[] = []
    if (sceneId !== undefined) args.push(sceneId)
    if (limit !== undefined) args.push(limit)
    return this.db
      .prepare('SELECT * FROM memory_knowledge_edges' + where + ' ORDER BY id' + suffix)
      .all(...args)
      .map((row) => this.decode(row))
  }
  get(id: string): KnowledgeEdge | undefined {
    if (!this.exists()) return
    const r = this.db.prepare('SELECT * FROM memory_knowledge_edges WHERE id=?').get(id)
    return r ? this.decode(r) : undefined
  }
  byKey(key: string): KnowledgeEdge | undefined {
    if (!this.exists()) return
    const r = this.db.prepare('SELECT * FROM memory_knowledge_edges WHERE admission_key=?').get(key)
    return r ? this.decode(r) : undefined
  }
  entity(id: string, sceneId: string): KnowledgeEntity | undefined {
    // Entity IDs never merge by title; only its originating edge owns its identity.
    const rows = this.rows(sceneId, 201)
    if (rows.length > 200) throw new Error('knowledge_scan_limit')
    for (const edge of rows)
      for (const endpoint of [edge.subject, edge.object])
        if (
          endpoint.kind === 'entity' &&
          endpoint.entity.id === id &&
          endpoint.entity.originEdgeId === edge.id
        )
          return endpoint.entity
  }
  put(edge: KnowledgeEdge, key: string) {
    this.requireTransaction()
    const safe = KnowledgeEdgeSchema.parse(edge)
    this.writeBoundary()
    this.initialize()
    this.db
      .prepare('INSERT INTO memory_knowledge_edges VALUES(?,?,?,?,?)')
      .run(edge.id, edge.sceneId, edge.backing.id, key, JSON.stringify(safe))
  }
  replace(edge: KnowledgeEdge) {
    this.requireTransaction()
    const safe = KnowledgeEdgeSchema.parse(edge)
    this.writeBoundary()
    this.db
      .prepare('UPDATE memory_knowledge_edges SET value=? WHERE id=?')
      .run(JSON.stringify(safe), edge.id)
  }
  remove(id: string) {
    this.requireTransaction()
    if (this.exists()) {
      this.writeBoundary()
      this.db.prepare('DELETE FROM memory_knowledge_edges WHERE id=?').run(id)
    }
  }
}
