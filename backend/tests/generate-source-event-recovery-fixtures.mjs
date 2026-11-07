/** Actual dual-SQLite source/recovery fault oracle; synthetic, isolated files only. */
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { SourceEventStore } from '../../src/memory/storage/event-revisions.ts'
import {
  RecoveryAuthority,
  bindRecovery,
  saveGate,
  metadata,
  assertRecoveryReady,
} from '../../src/memory/recovery/ledger.ts'
import { recordSourceEventMutation } from '../../src/memory/recovery/source-events.ts'
assert.equal(process.version, 'v24.22.1')
const identity = {
  sceneId: 'scene',
  adapterId: 'synthetic',
  accountId: 'account',
  messageId: 'message',
  senderId: 'speaker',
}
const ingress = { kind: 'ingress', identity, bodyHash: 'a'.repeat(64), occurredAt: null }
const edit = {
  kind: 'edit',
  identity,
  bodyHash: 'b'.repeat(64),
  occurredAt: null,
  editId: 'edit-1',
  order: { kind: 'platform_sequence', sequence: 1 },
}
const cases = []
function run(
  name,
  {
    stage,
    raise,
    catchInside = true,
    continueWrite = false,
    outerFailure = false,
    enrollment = false,
  } = {},
) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.source-event-oracle-'))
  let db, authority, external
  try {
    db = new Database(join(root, 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    db.exec("CREATE TABLE marker(value TEXT UNIQUE); INSERT INTO marker VALUES('original')")
    authority = new RecoveryAuthority(
      join(root, 'authority'),
      '12345678-1234-4123-8123-123456789012',
    )
    saveGate(db, {
      head: authority.head(),
      authority: authority.path,
      minimumReader: 1,
      state: 'ready',
    })
    bindRecovery(db, authority)
    const timeline = []
    let armed = false
    const sample = (label) =>
      timeline.push({
        label,
        inTransaction: db.inTransaction,
        gateSequence: metadata(db).head.sequence,
        minimumReader: metadata(db).minimumReader,
        authoritySequence: authority.head().sequence,
        revision: db.prepare('SELECT value FROM source_event_heads').get()
          ? JSON.parse(db.prepare('SELECT value FROM source_event_heads').get().value).metadata.ref
              .revision
          : null,
      })
    const source = new SourceEventStore(db, {
      id: () => 'event-1',
      now: () => '2026-10-28T00:00:00.000Z',
      assertReady: () => assertRecoveryReady(db),
      beforeWrite: (m) => {
        if (armed) sample('before-record')
        recordSourceEventMutation(db, m)
        if (armed) sample('after-record')
        if (armed && stage === 'after-hook') throw Error('synthetic-after-hook')
      },
    })
    if (!enrollment) source.observe(ingress)
    const originalAppend = authority.append.bind(authority)
    authority.append = (...args) => {
      if (armed) sample('append-entry')
      if (armed && stage === 'before-append') throw Error('synthetic-before-append')
      const head = originalAppend(...args)
      if (armed) sample('append-durable')
      if (armed && stage === 'after-append') throw Error('synthetic-after-append')
      return head
    }
    if (stage === 'reader-save')
      db.exec(
        `CREATE TRIGGER reader_fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.minimumReader')>json_extract(OLD.value,'$.minimumReader') BEGIN SELECT RAISE(ABORT,'synthetic-reader-save'); END`,
      )
    if (stage === 'gate-save')
      db.exec(
        `CREATE TRIGGER gate_fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.head.sequence')>json_extract(OLD.value,'$.head.sequence') BEGIN SELECT RAISE(${raise ?? 'ABORT'},'synthetic-gate-save'); END`,
      )
    if (stage === 'source-sql')
      db.exec(
        `CREATE TRIGGER source_fault BEFORE UPDATE ON source_event_heads BEGIN SELECT RAISE(${raise ?? 'ABORT'},'synthetic-source-sql'); END`,
      )
    if (stage === 'ordinary-constraint')
      db.exec(
        `CREATE TRIGGER source_constraint BEFORE UPDATE ON source_event_heads BEGIN INSERT INTO marker VALUES('original'); END`,
      )
    source.subscribeCommitted((m) => {
      if (!armed) return
      sample('committed-listener')
      if (stage === 'subscriber') throw Error('synthetic-subscriber')
      if (stage === 'external-advance') {
        external = new RecoveryAuthority(authority.path)
        external.append(
          { kind: 'source', sceneId: 'external', messageId: 'advance' },
          external.head(),
        )
        sample('external-advanced')
      }
    })
    armed = true
    let result = null,
      caught = null,
      outerError = null
    try {
      db.transaction(() => {
        db.prepare('INSERT INTO marker VALUES(?)').run('outer-before')
        if (catchInside) {
          try {
            result = source.observe(enrollment ? ingress : edit)
          } catch (e) {
            caught = { message: e.message, code: e.code ?? null }
            sample('caught')
          }
        } else result = source.observe(enrollment ? ingress : edit)
        if (continueWrite) {
          db.prepare('INSERT INTO marker VALUES(?)').run('outer-after')
          sample('continued-write')
        }
        if (outerFailure) throw Error('synthetic-outer')
        sample('outer-return')
      }).immediate()
    } catch (e) {
      outerError = { message: e.message, code: e.code ?? null }
    }
    let readiness = null
    try {
      assertRecoveryReady(db)
    } catch (e) {
      readiness = e.message
    }
    const gate = metadata(db)
    const persisted = {
      gate: { head: gate.head, minimumReader: gate.minimumReader, state: gate.state },
      authorityHead: authority.head(),
      entries: authority.entries(),
      heads: db.prepare('SELECT * FROM source_event_heads').all(),
      revisions: db.prepare('SELECT * FROM source_event_revisions ORDER BY revision').all(),
      editKeys: db.prepare('SELECT * FROM source_event_edit_keys').all(),
      markers: db.prepare('SELECT value FROM marker ORDER BY value').all(),
      inTransaction: db.inTransaction,
      readiness,
    }
    cases.push({
      name,
      stage: stage ?? null,
      raise: raise ?? null,
      catchInside,
      continueWrite,
      outerFailure,
      enrollment,
      result,
      caught,
      outerError,
      timeline,
      persisted,
    })
  } finally {
    external?.close()
    authority?.close()
    db?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
for (const stage of [
  'before-append',
  'after-append',
  'gate-save',
  'after-hook',
  'subscriber',
  'external-advance',
  'ordinary-constraint',
]) {
  run(`${stage}-caught`, { stage, continueWrite: true })
  if (stage !== 'external-advance') run(`${stage}-uncaught`, { stage, catchInside: false })
}
for (const stage of ['gate-save', 'source-sql'])
  for (const raise of ['ABORT', 'ROLLBACK'])
    for (const continueWrite of [false, true])
      run(`${stage}-${raise}-${continueWrite ? 'continue' : 'return'}`, {
        stage,
        raise,
        continueWrite,
      })
for (const stage of ['reader-save', 'after-hook', 'subscriber'])
  run(`enrollment-${stage}-caught`, { stage, enrollment: true, continueWrite: true })
run('outer-rollback-after-success', { outerFailure: true })
// Review regressions: preserve per-row recall failure ordering and snapshot
// caller-owned source input before invoking any application callbacks.
const reviewCases = []
function reviewCase(name, { recall = false, catchInside = true, callback = null } = {}) {
  const root = mkdtempSync(join(process.cwd(), 'backend/.source-event-review-oracle-'))
  let db, authority
  try {
    db = new Database(join(root, 'main.sqlite'))
    db.pragma('journal_mode=WAL')
    authority = new RecoveryAuthority(
      join(root, 'authority'),
      '12345678-1234-4123-8123-123456789012',
    )
    saveGate(db, {
      head: authority.head(),
      authority: authority.path,
      minimumReader: 1,
      state: 'ready',
    })
    bindRecovery(db, authority)
    let armed = false
    const input = structuredClone(callback === 'id' ? ingress : edit)
    input.occurredAt = '2026-10-28T01:02:03.000Z'
    const callbacks = []
    const mutate = (phase) => {
      if (armed && callback === phase) input.occurredAt = '2026-10-29T04:05:06.000Z'
    }
    const record = (phase, m) => {
      if (armed)
        callbacks.push({
          phase,
          kind: m.kind,
          occurredAt: m.metadata?.occurredAt ?? null,
          headHash: m.headHash ?? null,
        })
    }
    const source = new SourceEventStore(db, {
      id: () => {
        mutate('id')
        return 'event-1'
      },
      now: () => {
        mutate('now')
        return '2026-10-28T00:00:00.000Z'
      },
      assertReady: () => {
        mutate('readiness')
        assertRecoveryReady(db)
      },
      beforeWrite: (m) => {
        mutate('before-write')
        record('before-write', m)
        recordSourceEventMutation(db, m)
      },
    })
    source.subscribeCommitted((m) => {
      mutate('subscriber')
      record('subscriber', m)
    })
    source.subscribeCommitted((m) => record('second-subscriber', m))
    if (callback !== 'id') source.observe(ingress)
    if (recall) {
      db.prepare('INSERT INTO source_event_heads VALUES(?,?,?,?,?)').run(
        'corrupt-later',
        'corrupt-2',
        'scene',
        'message',
        '{',
      )
      assert.deepEqual(
        db
          .prepare('SELECT event_id FROM source_event_heads WHERE scene_id=? AND message_id=?')
          .all('scene', 'message'),
        [{ event_id: 'event-1' }, { event_id: 'corrupt-2' }],
      )
    }
    armed = true
    let caught = null,
      outerError = null
    const action = () => (recall ? source.revokeAll('scene', 'message') : source.observe(input))
    try {
      db.transaction(() => {
        if (catchInside) {
          try {
            action()
          } catch (e) {
            caught = e.message
          }
        } else action()
      }).immediate()
    } catch (e) {
      outerError = e.message
    }
    const tables = [
      'source_event_edit_keys',
      'source_event_heads',
      'source_event_revisions',
      'source_event_tombstones',
    ]
    const gate = metadata(db)
    reviewCases.push({
      name,
      recall,
      catchInside,
      callback,
      caught,
      outerError,
      inputOccurredAt: input.occurredAt,
      callbacks,
      rows: JSON.stringify(
        Object.fromEntries(
          tables.map((table) => [table, db.prepare(`SELECT * FROM ${table} ORDER BY 1,2`).all()]),
        ),
      ),
      gate: { head: gate.head, minimumReader: gate.minimumReader },
      authorityHead: authority.head(),
    })
  } finally {
    authority?.close()
    db?.close()
    rmSync(root, { recursive: true, force: true })
  }
}
reviewCase('recall-corrupt-later-caught', { recall: true })
reviewCase('recall-corrupt-later-uncaught', { recall: true, catchInside: false })
for (const callback of ['readiness', 'id', 'now', 'before-write', 'subscriber'])
  reviewCase(`input-mutation-${callback}`, { callback })
const output = JSON.stringify({ node: process.version, cases, reviewCases }, null, 2) + '\n'
const path = 'backend/internal/sourceevents/testdata/source-event-recovery.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), output)
else writeFileSync(path, output)
console.log(
  `Source/recovery oracle: ${cases.length} fault cases, ${reviewCases.length} review regressions`,
)
