import { parentBindingState } from '../domain/proofs.ts'
import { z } from 'zod'
import { assertDomainReady } from '../recovery/ledger.ts'
import { CommitmentRefSchema, CommitmentTermsSchema } from '../domain/commitments.ts'
import { SourceEventRefSchema } from '../domain/events.ts'
import { isDeepStrictEqual } from 'node:util'
import type Database from 'better-sqlite3'
import type { CommitmentHead, CommitmentRevision } from '../domain/commitments.ts'
const id = z.string().min(1).max(512)
const version = z.number().int().positive().safe()
export const CommitmentHeadSchema = z
  .object({
    id,
    sceneId: id,
    latestVersion: version,
    activeVersion: version.nullable(),
    everActivatedVersion: version.nullable(),
    everTerminal: z
      .object({ version, state: z.enum(['done', 'cancelled']) })
      .strict()
      .nullable(),
    deletionEpoch: z.number().int().nonnegative().safe(),
    mutationVersion: version,
    deleted: z.boolean(),
  })
  .strict()
  .refine(
    (h) =>
      (h.activeVersion === null || h.activeVersion <= h.latestVersion) &&
      (h.everActivatedVersion === null || h.everActivatedVersion <= h.latestVersion) &&
      (h.everTerminal === null || h.everTerminal.version <= h.latestVersion),
  )
const sourceShape = {
  sceneId: id,
  messageId: id,
  senderId: id,
  eventRef: SourceEventRefSchema.optional(),
}
const sourceSchema = z.object(sourceShape).strict()
const witnessSchema = z
  .object({
    kind: z.enum(['self', 'mentioned', 'legacy_sender', 'unresolved']),
    personId: id.optional(),
    sourceMessageId: id.optional(),
    mentionPartIndex: z.number().int().nonnegative().safe().optional(),
  })
  .strict()
const proofSchema = z
  .object({
    proofId: id,
    sources: z.array(sourceSchema).min(1).max(64),
    parents: z.array(id).max(64),
    parentReferences: z.array(CommitmentRefSchema).max(64).optional(),
    subjectWitness: witnessSchema.optional(),
    sourceObservations: z
      .array(
        z
          .object({
            ...sourceShape,
            sentAt: z.iso.datetime({ offset: true }).optional(),
            receivedAt: z.iso.datetime({ offset: true }).optional(),
          })
          .strict(),
      )
      .max(64)
      .optional(),
    invalidatedBy: id.optional(),
  })
  .strict()
  .refine((proof) => parentBindingState(proof) !== 'invalid')
export const CommitmentEvidenceSchema = z
  .object({
    backing: CommitmentRefSchema,
    qualificationRef: CommitmentRefSchema.optional(),
    backingFingerprint: z.string().regex(/^[a-f0-9]{64}$/),
    proofs: z.array(proofSchema).min(1).max(64),
    personId: id.optional(),
    kind: z.enum(['proposal', 'confirm', 'complete', 'cancel']),
  })
  .strict()
export const CommitmentRevisionSchema = z
  .object({
    ref: CommitmentRefSchema,
    sceneId: id,
    terms: CommitmentTermsSchema,
    recordedAt: z.iso.datetime({ offset: true }),
    evidence: z.array(CommitmentEvidenceSchema).min(1).max(64),
    requiredProposalGroups: z
      .array(
        z
          .array(CommitmentEvidenceSchema.refine((e) => e.kind === 'proposal'))
          .min(1)
          .max(64),
      )
      .max(64)
      .refine((groups) => groups.reduce((sum, g) => sum + g.length, 0) <= 64),
  })
  .strict()
export type CommitmentDatabase = Pick<
  Database.Database,
  'exec' | 'prepare' | 'transaction' | 'inTransaction'
>
export class CommitmentStorage {
  constructor(readonly db: CommitmentDatabase) {
    assertDomainReady(db)
    db.transaction(() =>
      db.exec(`CREATE TABLE IF NOT EXISTS memory_commitment_heads(id TEXT PRIMARY KEY,scene_id TEXT NOT NULL,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS memory_commitment_revisions(id TEXT NOT NULL,version INTEGER NOT NULL,value TEXT NOT NULL,PRIMARY KEY(id,version));
CREATE TABLE IF NOT EXISTS memory_commitment_commands(key TEXT PRIMARY KEY,id TEXT NOT NULL,version INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS memory_commitment_scene ON memory_commitment_heads(scene_id);`),
    )()
  }
  requireTransaction() {
    assertDomainReady(this.db)
    if (!this.db.inTransaction) throw Error('commitment_transaction_required')
  }
  head(id: string): CommitmentHead | undefined {
    assertDomainReady(this.db)
    const row = this.db
      .prepare('SELECT scene_id,value FROM memory_commitment_heads WHERE id=?')
      .get(id) as { scene_id: string; value: string } | undefined
    if (!row) return undefined
    const head = CommitmentHeadSchema.parse(JSON.parse(row.value))
    if (head.id !== id || head.sceneId !== row.scene_id) throw Error('commitment_corrupt')
    return head
  }
  revision(id: string, version: number): CommitmentRevision | undefined {
    assertDomainReady(this.db)
    const row = this.db
      .prepare('SELECT value FROM memory_commitment_revisions WHERE id=? AND version=?')
      .get(id, version) as { value: string } | undefined
    if (!row) return undefined
    const revision = CommitmentRevisionSchema.parse(JSON.parse(row.value))
    const head = this.head(id)
    if (
      revision.ref.id !== id ||
      revision.ref.version !== version ||
      !head ||
      revision.sceneId !== head.sceneId ||
      revision.ref.deletionEpoch !== head.deletionEpoch
    )
      throw Error('commitment_corrupt')
    return revision
  }
  heads(scene: string): CommitmentHead[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare(
          'SELECT id FROM memory_commitment_heads WHERE scene_id=? ORDER BY rowid DESC LIMIT 201',
        )
        .all(scene) as { id: string }[]
    ).map((row) => {
      const head = this.head(row.id)!
      if (head.sceneId !== scene) throw Error('commitment_corrupt')
      return head
    })
  }
  allHeads(): CommitmentHead[] {
    assertDomainReady(this.db)
    return (
      this.db.prepare('SELECT id FROM memory_commitment_heads').all() as { id: string }[]
    ).map((row) => this.head(row.id)!)
  }
  revisions(id: string): CommitmentRevision[] {
    assertDomainReady(this.db)
    return (
      this.db
        .prepare('SELECT version FROM memory_commitment_revisions WHERE id=? ORDER BY version')
        .all(id) as { version: number }[]
    ).map((row) => this.revision(id, row.version)!)
  }
  put(head: CommitmentHead, revision?: CommitmentRevision) {
    this.requireTransaction()
    CommitmentHeadSchema.parse(head)
    if (revision) {
      CommitmentRevisionSchema.parse(revision)
      if (
        revision.ref.id !== head.id ||
        revision.sceneId !== head.sceneId ||
        revision.ref.deletionEpoch !== head.deletionEpoch
      )
        throw Error('commitment_corrupt')
    }
    if (revision) {
      const prior = this.revision(head.id, revision.ref.version)
      if (
        prior &&
        (!isDeepStrictEqual(prior.terms, revision.terms) ||
          !isDeepStrictEqual(prior.ref, revision.ref) ||
          prior.sceneId !== revision.sceneId ||
          prior.recordedAt !== revision.recordedAt)
      )
        throw Error('commitment_immutable_terms')
    }
    this.db
      .prepare(
        'INSERT INTO memory_commitment_heads VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
      )
      .run(head.id, head.sceneId, JSON.stringify(head))
    if (revision)
      this.db
        .prepare(
          'INSERT INTO memory_commitment_revisions VALUES(?,?,?) ON CONFLICT(id,version) DO UPDATE SET value=excluded.value',
        )
        .run(head.id, revision.ref.version, JSON.stringify(revision))
  }
  command(key: string) {
    assertDomainReady(this.db)
    return this.db
      .prepare('SELECT id,version FROM memory_commitment_commands WHERE key=?')
      .get(key) as { id: string; version: number } | undefined
  }
  remember(key: string, id: string, version: number) {
    this.requireTransaction()
    this.db.prepare('INSERT INTO memory_commitment_commands VALUES(?,?,?)').run(key, id, version)
  }
  removeRevision(id: string, version: number) {
    this.requireTransaction()
    this.db
      .prepare('DELETE FROM memory_commitment_revisions WHERE id=? AND version=?')
      .run(id, version)
    this.db
      .prepare('DELETE FROM memory_commitment_commands WHERE id=? AND version=?')
      .run(id, version)
  }
  clear(id: string) {
    this.requireTransaction()
    this.db.prepare('DELETE FROM memory_commitment_revisions WHERE id=?').run(id)
    this.db.prepare('DELETE FROM memory_commitment_commands WHERE id=?').run(id)
  }
}
