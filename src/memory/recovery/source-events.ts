import type Database from 'better-sqlite3'
import type { Store } from '../../store.ts'
import {
  sourceIdentityKey,
  sourceEventHeadHash,
  sameSourceRef,
  type SourceEventMutation,
} from '../domain/events.ts'
import {
  SourceEventStore,
  SourceEventHeadSchema,
  SourceEventMetadataSchema,
} from '../storage/event-revisions.ts'
import { revoke, requireSourceEventReader, type Revocation } from './ledger.ts'
import { parseSourceEditHeadEvent, type SourceEditHeadEvent } from './source-event-contract.ts'

/** Trusted store callback; no model or generic API accepts this payload. */
export function recordSourceEventMutation(
  db: Database.Database,
  mutation: Readonly<SourceEventMutation>,
): void {
  requireSourceEventReader(db)
  if (mutation.kind === 'recalled') {
    revoke(db, {
      kind: 'source',
      sceneId: mutation.identity.sceneId,
      messageId: mutation.identity.messageId,
    })
  } else if (mutation.kind === 'edited') {
    if (
      !mutation.metadata ||
      !mutation.headHash ||
      !sameSourceRef(mutation.metadata.ref, mutation.ref) ||
      sourceIdentityKey(mutation.identity) !== sourceIdentityKey(mutation.metadata.identity)
    )
      throw new Error('recovery_source_event_invalid')
    revoke(
      db,
      parseSourceEditHeadEvent({
        kind: 'source_edit_head',
        schemaVersion: 1,
        sceneId: mutation.identity.sceneId,
        messageId: mutation.identity.messageId,
        ...mutation.ref,
        identityHash: sourceIdentityKey(mutation.identity),
        headHash: mutation.headHash,
      }),
    )
  }
}

/** Strict metadata qualification, never copy a newer journal head over stale source evidence. */
export function createRecoverySourceEventPort(store: Store) {
  const known: Record<string, readonly string[]> = {
    source_event_heads: ['identity_key', 'event_id', 'scene_id', 'message_id', 'value'],
    source_event_revisions: ['event_id', 'revision', 'value'],
    source_event_edit_keys: ['event_id', 'edit_key', 'revision', 'change_hash'],
    source_event_tombstones: ['scene_id', 'message_id'],
  }
  function assertSupported() {
    const tables = store.db
      .prepare("SELECT name,type FROM sqlite_master WHERE name LIKE 'source_event_%'")
      .all() as { name: string; type: string }[]
    for (const row of tables) {
      const columns = known[row.name]
      if (row.type !== 'table' || !columns) throw new Error('recovery_unknown_source_event_schema')
      const actual = (
        store.db.prepare(`PRAGMA table_xinfo("${row.name}")`).all() as { name: string }[]
      )
        .map((c) => c.name)
        .sort()
      if (JSON.stringify(actual) !== JSON.stringify([...columns].sort()))
        throw new Error('recovery_unknown_source_event_schema')
    }
    if (tables.length && tables.length !== Object.keys(known).length)
      throw new Error('recovery_unknown_source_event_schema')
    return tables.length > 0
  }
  const events = new SourceEventStore(store.db, { assertReady: () => store.assertRecoveryReady() })
  function recall(sceneId: string, messageId: string) {
    if (!assertSupported()) return
    events.revokeAll(sceneId, messageId)
    const rows = store.db.prepare('SELECT value FROM source_event_revisions').all() as {
      value: string
    }[]
    for (const row of rows) {
      const value = SourceEventMetadataSchema.parse(JSON.parse(row.value))
      if (value.identity.sceneId === sceneId && value.identity.messageId === messageId)
        throw new Error('recovery_source_event_purge_incomplete')
    }
    if (
      store.db
        .prepare('SELECT 1 FROM source_event_heads WHERE scene_id=? AND message_id=?')
        .get(sceneId, messageId)
    )
      throw new Error('recovery_source_event_purge_incomplete')
  }
  function verify(entries: readonly Revocation[]) {
    const present = assertSupported()
    const latest = new Map<string, SourceEditHeadEvent>()
    for (const event of entries)
      if (event.kind === 'source_edit_head')
        latest.set(JSON.stringify([event.sceneId, event.messageId]), event)
    for (const event of latest.values()) {
      if (store.recalled(event.sceneId, event.messageId)) continue
      if (!present) throw new Error('recovery_source_event_material_missing')
      const row = store.db
        .prepare(
          'SELECT identity_key,event_id,scene_id,message_id,value FROM source_event_heads WHERE identity_key=?',
        )
        .get(event.identityHash) as
        | {
            identity_key: string
            event_id: string
            scene_id: string
            message_id: string
            value: string
          }
        | undefined
      if (!row) throw new Error('recovery_source_event_material_missing')
      const parsed = SourceEventHeadSchema.safeParse(JSON.parse(row.value))
      if (!parsed.success) throw new Error('recovery_source_event_invalid')
      const head = parsed.data,
        metadata = head.metadata
      if (
        head.deleted ||
        row.event_id !== event.eventId ||
        row.scene_id !== event.sceneId ||
        row.message_id !== event.messageId ||
        sourceIdentityKey(metadata.identity) !== event.identityHash ||
        metadata.identity.sceneId !== event.sceneId ||
        metadata.identity.messageId !== event.messageId ||
        !sameSourceRef(metadata.ref, event) ||
        sourceEventHeadHash(metadata, head.sequence, head.orderMode) !== event.headHash
      )
        throw new Error('recovery_source_event_head_mismatch')
      const current = events.current(metadata.ref, metadata.identity)
      if (
        !current.eligible ||
        !current.metadata ||
        sourceEventHeadHash(current.metadata, head.sequence, head.orderMode) !== event.headHash
      )
        throw new Error('recovery_source_event_material_missing')
    }
  }
  return { assertSupported, recall, verify }
}
