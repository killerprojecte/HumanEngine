/** Real TS outbox/processor/coordinator. Synthetic eligibility is an explicit
 * port; native actual assertion-authority composition is tested separately. */
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { syncBuiltinESMExports } from 'node:module'
import Database from 'better-sqlite3'
import { mkdtempSync, readFileSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { MemoryProjections } from '../../src/memory/projections/coordinator.ts'
assert.equal(process.version, 'v24.22.1')
const clone = structuredClone
const state = (id = 'a', version = 1, epoch = 0, deleted = 0) => ({
  assertion_id: id,
  version,
  deletion_epoch: epoch,
  deleted,
})
const snapshot = (kind = 'lexical', version = 1) => ({
  assertionId: 'a',
  version,
  deletionEpoch: 0,
  kind,
})
const record = (extra = {}) => ({
  id: 'a',
  scope: { kind: 'scene', sceneId: 'scene', personId: 'person' },
  content: 'Synthetic café shared-plan alpha',
  status: 'active',
  lineageStatus: 'verified',
  validFrom: '2099-01-01T00:00:00Z',
  ...extra,
})
const enqueue = (kind = 'lexical', version = 1) => ({
  op: 'enqueue',
  snapshot: snapshot(kind, version),
})
const claim = (leaseMs = 100, limit = 1) => ({ op: 'claim', leaseMs, limit })
const scenarios = []
const add = (name, steps, extra = {}) =>
  scenarios.push({
    name,
    states: [state()],
    records: { a: record() },
    enabled: true,
    steps,
    ...extra,
  })
add('enqueue transaction and stale preconditions', [
  { ...enqueue(), transaction: false },
  { op: 'state', value: state('a', 2) },
  enqueue(),
  { op: 'enqueue', snapshot: snapshot('unknown') },
  { op: 'enqueue', snapshot: { ...snapshot(), version: 0 } },
])
add('duplicate keeps terminal idempotency without clock', [
  enqueue(),
  enqueue(),
  claim(),
  { op: 'complete', leaseIndex: 0, callback: { effect: { id: 'a', value: 'done' } } },
  { op: 'clock', value: -1 },
  enqueue(),
  { op: 'clock', value: 10000 },
  claim(),
])
add('new snapshot invalidates older pending and leased kinds only', [
  enqueue(),
  enqueue('person'),
  claim(),
  { op: 'state', value: state('a', 2) },
  enqueue('lexical', 2),
  claim(100, 16),
  { op: 'complete', leaseIndex: 0, callback: { error: 'must_not_execute' } },
])
add('stale scan precedes simultaneous lease expiry', [
  enqueue(),
  claim(),
  { op: 'clock', value: 10100 },
  { op: 'state', value: state('a', 2) },
  claim(),
])
add('expiry attempts and third expiry terminal', [
  enqueue(),
  claim(),
  { op: 'clock', value: 10100 },
  claim(),
  { op: 'clock', value: 10200 },
  claim(),
  { op: 'clock', value: 10300 },
  claim(),
])
add('owned lease compares token attempts and snapshot', [
  enqueue(),
  claim(),
  {
    op: 'complete',
    leaseIndex: 0,
    override: { attempts: 2 },
    callback: { error: 'must_not_execute' },
  },
  { op: 'fail', leaseIndex: 0, override: { leaseToken: 'wrong' }, reason: 'projection_failed' },
  {
    op: 'complete',
    leaseIndex: 0,
    override: { version: 2 },
    callback: { error: 'must_not_execute' },
  },
  { op: 'complete', leaseIndex: 0, callback: { effect: { id: 'done', value: 'correct' } } },
])
add('callback failure rolls back SQL but preserves lease', [
  enqueue(),
  claim(),
  {
    op: 'complete',
    leaseIndex: 0,
    callback: {
      effect: { id: 'leak', value: 'PRIVATE_CALLBACK_CANARY' },
      error: 'synthetic_callback_failure',
    },
  },
  { op: 'complete', leaseIndex: 0, callback: { effect: { id: 'kept', value: 'success' } } },
])
add('callback snapshot change rolls back local material', [
  enqueue(),
  claim(),
  {
    op: 'complete',
    leaseIndex: 0,
    callback: { effect: { id: 'leak', value: 'no' }, state: state('a', 2) },
  },
  { op: 'complete', leaseIndex: 0, callback: { invalidate: 'a' } },
  { op: 'complete', leaseIndex: 0, callback: {} },
])
add('callback exact expiry rolls back then old owner cannot fail', [
  enqueue(),
  claim(),
  {
    op: 'complete',
    leaseIndex: 0,
    callback: { effect: { id: 'leak', value: 'no' }, clock: 10100 },
  },
  { op: 'fail', leaseIndex: 0, reason: 'projection_failed' },
  claim(),
  { op: 'complete', leaseIndex: 1, callback: {} },
])
add('operational backoff and third failure terminal', [
  enqueue(),
  claim(),
  { op: 'fail', leaseIndex: 0, reason: 'projection_failed' },
  claim(),
  { op: 'clock', value: 11000 },
  claim(),
  { op: 'fail', leaseIndex: 1, reason: 'projection_failed' },
  { op: 'clock', value: 13000 },
  claim(),
  { op: 'fail', leaseIndex: 2, reason: 'projection_failed' },
  { op: 'clock', value: 17000 },
  claim(),
  enqueue(),
])
add('unavailable terminal retains duplicate idempotency', [
  enqueue(),
  claim(),
  { op: 'fail', leaseIndex: 0, reason: 'source_unavailable' },
  enqueue(),
  { op: 'fail', leaseIndex: 0, reason: 'raw_private_error' },
  claim(),
])
add('invalid options and clock', [
  claim(0),
  claim(300001),
  claim(10, 17),
  { op: 'prune', limit: 0 },
  { op: 'prune', limit: 1001 },
  { op: 'drain', limit: 0 },
  { op: 'drain', limit: 65 },
  { op: 'clock', value: -1 },
  enqueue(),
  claim(),
  { op: 'invalidate', id: 'a' },
  { op: 'clock', value: 1.5 },
  claim(),
])
add('outer rollback removes enqueued job', [{ ...enqueue(), rollback: true }, claim(), enqueue()])
add('outer rollback undoes callback and completion state', [
  enqueue(),
  claim(),
  {
    op: 'complete',
    leaseIndex: 0,
    callback: { effect: { id: 'a', value: 'rollback' } },
    outer: true,
    rollback: true,
  },
  { op: 'complete', leaseIndex: 0, callback: {} },
])
add('coordinator lexical then person and actual FTS future record', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'flush' },
  { op: 'match', query: 'cafe' },
  { op: 'match', query: 'alpha', scope: 'scene:scene', person: 'person' },
  { op: 'match', query: 'alpha', scope: 'scene:other' },
  { op: 'committed', snapshot: snapshot() },
  { op: 'flush' },
])
add('replacement removes old term and retains scope', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'flush' },
  { op: 'state', value: state('a', 2) },
  {
    op: 'record',
    id: 'a',
    value: record({
      content: 'Synthetic beta replacement',
      scope: { kind: 'scene', sceneId: 'second' },
    }),
  },
  { op: 'committed', snapshot: snapshot('lexical', 2) },
  { op: 'flush' },
  { op: 'match', query: 'alpha' },
  { op: 'match', query: 'beta' },
])
add('shared projection and null person', [
  { op: 'record', id: 'a', value: record({ scope: { kind: 'shared' } }) },
  { op: 'committed', snapshot: snapshot() },
  { op: 'flush' },
  { op: 'match', query: 'plan', scope: 'shared:public', person: null },
])
add('missing and wrong id sources become invalid', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'record', id: 'a', value: null },
  { op: 'flush' },
  { op: 'state', value: state('a', 2) },
  { op: 'committed', snapshot: snapshot('lexical', 2) },
  { op: 'record', id: 'a', value: record({ id: 'wrong' }) },
  { op: 'flush' },
])
add('invalidated or quarantined source cannot project', [
  enqueue(),
  { op: 'record', id: 'a', value: record({ status: 'invalidated' }) },
  { op: 'drain', limit: 1 },
  enqueue('person'),
  { op: 'record', id: 'a', value: record({ lineageStatus: 'quarantined' }) },
  { op: 'drain', limit: 1 },
])
add('raw eligible exception not persisted in job reason', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'eligibleFault', id: 'a', value: 'PRIVATE_ELIGIBILITY_CANARY' },
  { op: 'flush' },
  { op: 'eligibleFault', id: 'a', value: null },
  { op: 'clock', value: 11000 },
  { op: 'flush' },
])
add(
  'OFF constructor creates tables without backfill',
  [
    { op: 'committed', snapshot: snapshot() },
    { op: 'flush' },
    { op: 'flag', value: true },
    { op: 'flush' },
    { op: 'committed', snapshot: snapshot() },
    { op: 'flush' },
  ],
  { enabled: false },
)
add('OFF removal invalidates late lease and clears all indexes', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'flush' },
  { op: 'state', value: state('a', 2) },
  { op: 'committed', snapshot: snapshot('lexical', 2) },
  claim(),
  { op: 'flag', value: false },
  { op: 'remove', id: 'a' },
  { op: 'complete', leaseIndex: 0, callback: { error: 'must_not_execute' } },
  { op: 'match', query: 'alpha' },
])
add('remove outer rollback restores indexes and pending work', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'flush' },
  { op: 'state', value: state('a', 2) },
  enqueue('lexical', 2),
  { op: 'remove', id: 'a', rollback: true },
  { op: 'match', query: 'alpha' },
  { op: 'remove', id: 'a' },
])
add('restart preserves queue and requires explicit drain', [
  { op: 'committed', snapshot: snapshot() },
  { op: 'reopen' },
  { op: 'match', query: 'alpha' },
  { op: 'flush' },
  { op: 'reopen' },
  { op: 'match', query: 'alpha' },
])
add('claim ordered available then id and limit', [
  enqueue('person'),
  enqueue(),
  { op: 'sql', statement: 'UPDATE memory_projection_outbox SET available_at=10001 WHERE id=1' },
  claim(100, 1),
  { op: 'clock', value: 10001 },
  claim(100, 1),
])
add(
  'prune keeps current terminal and inflight beyond retention',
  [
    { op: 'seedRetention' },
    { op: 'prune', limit: 2 },
    { op: 'prune', limit: 1000 },
    { op: 'prune', limit: 1000 },
  ],
  { compact: true },
)
function run(input) {
  const directory = mkdtempSync(join(tmpdir(), 'humanengine-projection-source-')),
    path = join(directory, 'main.sqlite')
  let db = new Database(path),
    now = 10000,
    enabled = input.enabled,
    calls = [],
    token = 0,
    records = clone(input.records),
    faults = {},
    leases = [],
    owner
  const savedUUID = crypto.randomUUID,
    savedNow = Date.now
  crypto.randomUUID = () => {
    const value = `00000000-0000-4000-8000-${String(++token).padStart(12, '0')}`
    calls.push({ op: 'token', value })
    return value
  }
  syncBuiltinESMExports()
  const clock = () => {
    calls.push({ op: 'now', value: now })
    return now
  }
  const initialize = () => {
    Date.now = clock
    try {
      owner = new MemoryProjections(
        db,
        () => {
          calls.push({ op: 'enabled', value: enabled })
          return enabled
        },
        (id) => {
          calls.push({ op: 'eligible', id })
          if (faults[id]) throw new Error(faults[id])
          return clone(records[id] ?? undefined)
        },
      )
    } finally {
      Date.now = savedNow
    }
  }
  const writeState = (s) =>
    db
      .prepare(
        'INSERT INTO memory_assertion_state VALUES(?,?,?,?) ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version,deletion_epoch=excluded.deletion_epoch,deleted=excluded.deleted',
      )
      .run(s.assertion_id, s.version, s.deletion_epoch, s.deleted)
  try {
    db.exec(
      'CREATE TABLE memory_assertion_state(assertion_id TEXT PRIMARY KEY,version INTEGER NOT NULL,deletion_epoch INTEGER NOT NULL,deleted INTEGER NOT NULL);CREATE TABLE fixture_effect(id TEXT PRIMARY KEY,value TEXT)',
    )
    for (const s of input.states) writeState(s)
    initialize()
    const inspect = () => {
      const outbox = db.prepare('SELECT * FROM memory_projection_outbox ORDER BY id').all()
      const summary = {
        count: outbox.length,
        sha256: crypto.createHash('sha256').update(JSON.stringify(outbox)).digest('hex'),
        first: outbox.slice(0, 3),
        last: outbox.slice(-3),
        states: Object.fromEntries(
          ['pending', 'leased', 'done', 'invalid', 'failed'].map((s) => [
            s,
            outbox.filter((r) => r.state === s).length,
          ]),
        ),
      }
      return {
        outbox: input.compact ? summary : outbox,
        fts: db
          .prepare(
            'SELECT rowid,assertion_id,scope_key,person_id,content FROM memory_fts ORDER BY rowid',
          )
          .all(),
        person: db
          .prepare('SELECT * FROM memory_person_projection_state ORDER BY assertion_id')
          .all(),
        effects: db.prepare('SELECT * FROM fixture_effect ORDER BY id').all(),
        assertions: db.prepare('SELECT * FROM memory_assertion_state ORDER BY assertion_id').all(),
      }
    }
    const trace = input.steps.map((step) => {
      calls = []
      let result, error
      const action = () => {
        const outbox = owner.outbox,
          processor = owner.processor
        const lease =
          step.leaseIndex === undefined
            ? undefined
            : { ...leases[step.leaseIndex], ...step.override }
        switch (step.op) {
          case 'enqueue':
            return outbox.enqueue(step.snapshot)
          case 'claim': {
            const found = outbox.claim({ leaseMs: step.leaseMs, limit: step.limit })
            leases.push(...clone(found))
            return found
          }
          case 'complete':
            return outbox.complete(lease, () => {
              calls.push({ op: 'apply' })
              const a = step.callback ?? {}
              if (a.effect)
                db.prepare(
                  'INSERT INTO fixture_effect VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value',
                ).run(a.effect.id, a.effect.value)
              if (a.state) writeState(a.state)
              if (a.clock !== undefined) now = a.clock
              if (a.invalidate) outbox.invalidate(a.invalidate)
              if (a.error) throw new Error(a.error)
            })
          case 'fail':
            return outbox.fail(lease, step.reason)
          case 'invalidate':
            return outbox.invalidate(step.id)
          case 'prune':
            return outbox.pruneTerminal(step.limit)
          case 'committed':
            return owner.committed(
              step.snapshot.assertionId,
              step.snapshot.version,
              step.snapshot.deletionEpoch,
            )
          case 'flush':
            return owner.flush()
          case 'drain':
            return processor.drain(step.limit)
          case 'remove':
            return owner.remove(step.id)
          case 'state':
            writeState(step.value)
            return
          case 'record':
            records[step.id] = clone(step.value)
            return
          case 'eligibleFault':
            faults[step.id] = step.value
            return
          case 'clock':
            now = step.value
            return
          case 'flag':
            enabled = step.value
            return
          case 'sql':
            db.exec(step.statement)
            return
          case 'reopen':
            db.close()
            db = new Database(path)
            initialize()
            return
          case 'match': {
            let sql =
                'SELECT assertion_id,scope_key,person_id,content FROM memory_fts WHERE memory_fts MATCH ?',
              args = [step.query]
            if ('scope' in step) {
              sql += ' AND scope_key=?'
              args.push(step.scope)
            }
            if ('person' in step) {
              sql += ' AND person_id IS ?'
              args.push(step.person)
            }
            return db.prepare(sql + ' ORDER BY assertion_id').all(...args)
          }
          case 'seedRetention':
            db.transaction(() => {
              const insert = db.prepare(
                "INSERT INTO memory_projection_outbox(assertion_id,version,deletion_epoch,kind,state,available_at,updated_at) VALUES(?,1,0,'lexical',?,10000,10000)",
              )
              insert.run('a', 'done')
              for (let i = 0; i < 4100; i++)
                insert.run('old-' + i, ['done', 'invalid', 'failed'][i % 3])
              insert.run('pending', 'pending')
              insert.run('leased', 'leased')
            })()
            return
          default:
            throw new Error('unknown projection step ' + step.op)
        }
      }
      const outer =
        step.outer ||
        step.rollback ||
        (['enqueue', 'committed', 'remove', 'invalidate'].includes(step.op) &&
          step.transaction !== false)
      if (outer) db.exec('BEGIN')
      try {
        result = action()
      } catch (e) {
        error = e.message
      }
      if (outer && db.inTransaction) db.exec(step.rollback ? 'ROLLBACK' : 'COMMIT')
      return {
        ...(error === undefined
          ? result === undefined
            ? { undefined: true }
            : { result }
          : { error }),
        calls,
        db: inspect(),
      }
    })
    return { ...input, trace }
  } finally {
    Date.now = savedNow
    crypto.randomUUID = savedUUID
    syncBuiltinESMExports()
    db.close()
    rmSync(directory, { recursive: true, force: true })
  }
}
const output = {
  source:
    'Actual TypeScript MemoryProjections/ProjectionOutbox/MemoryProjectionProcessor; deterministic UUID and explicit synthetic eligibility ports',
  scenarios: scenarios.map(run),
}
const text = JSON.stringify(output, null, 2) + '\n',
  destination = new URL('../internal/projections/testdata/source-oracle.json', import.meta.url)
mkdirSync(new URL('../internal/projections/testdata/', import.meta.url), { recursive: true })
if (process.argv.includes('--check')) assert.equal(readFileSync(destination, 'utf8'), text)
else writeFileSync(destination, text)
console.log(`${output.scenarios.length} actual projection/outbox traces`)
