import assert from 'node:assert/strict'
import test from 'node:test'
import { createHash } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import {
  RecoveryAuthority,
  beginDurableRevocation,
  captureRecoveryEffects,
  commitDurableRevocation,
  metadata,
  revoke,
  saveGate,
  type Head,
} from '../src/memory/recovery/ledger.ts'
import { Store } from '../src/store.ts'

const zero = '0'.repeat(64)
const digest = (value: string) => createHash('sha256').update(value).digest('hex')
function fixture(count = 8) {
  const root = mkdtempSync(join(tmpdir(), 'recovery-validation-cache-'))
  const path = join(root, 'authority')
  const authority = new RecoveryAuthority(path, 'synthetic-instance')
  let previous = zero
  authority.db.transaction(() => {
    const insert = authority.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)')
    for (let sequence = 1; sequence <= count; sequence++) {
      const payload = JSON.stringify({ kind: 'source', sceneId: 's', messageId: `m${sequence}` })
      const hash = digest(`synthetic-instance|${sequence}|${previous}|${payload}`)
      insert.run(sequence, previous, payload, hash)
      previous = hash
    }
  })()
  const initial = authority.head()
  return {
    authority,
    path,
    initial,
    close() {
      if (authority.db.open) {
        if (authority.db.inTransaction) authority.db.exec('ROLLBACK')
        authority.close()
      }
      rmSync(root, { recursive: true, force: true })
    },
  }
}
function hashCalls(run: () => unknown) {
  const prototype = Object.getPrototypeOf(createHash('sha256'))
  const original = prototype.update
  let calls = 0
  prototype.update = function (...args: unknown[]) {
    calls++
    return original.apply(this, args)
  }
  try {
    run()
    return calls
  } finally {
    prototype.update = original
  }
}
function replaceLast(authority: RecoveryAuthority, previous: Head, messageId: string) {
  const row = authority.db
    .prepare('SELECT previous_hash FROM revocations WHERE sequence=?')
    .get(previous.sequence) as { previous_hash: string }
  const payload = JSON.stringify({ kind: 'source', sceneId: 's', messageId })
  const hash = digest(`${previous.instanceId}|${previous.sequence}|${row.previous_hash}|${payload}`)
  authority.db
    .prepare('UPDATE revocations SET payload=?,hash=? WHERE sequence=?')
    .run(payload, hash, previous.sequence)
  return { ...previous, hash }
}

test('committed integrity reuse is constant work; own append hashes only the new link and preserves dedup', () => {
  const f = fixture(128)
  try {
    assert.equal(
      hashCalls(() => {
        for (let i = 0; i < 20; i++) assert.deepEqual(f.authority.head(), f.initial)
      }),
      0,
    )
    const event = { kind: 'source' as const, sceneId: 's', messageId: 'new' }
    let next!: Head
    assert.equal(
      hashCalls(() => {
        next = f.authority.append(event, f.initial)
      }),
      1,
    )
    assert.equal(next.sequence, f.initial.sequence + 1)
    assert.equal(
      hashCalls(() => assert.deepEqual(f.authority.append(event, next), next)),
      0,
    )
    assert.equal(
      hashCalls(() => {
        next = f.authority.append(event, next, true)
      }),
      1,
    )
    assert.equal(next.sequence, f.initial.sequence + 2)
    assert.equal(
      hashCalls(() => assert.deepEqual(f.authority.head(), next)),
      0,
    )
    assert.equal(f.authority.entries().length, f.initial.sequence + 2)
  } finally {
    f.close()
  }
})

test('an external same-head edit of an old row invalidates integrity even when the latest row is unchanged', () => {
  const f = fixture()
  const other = new RecoveryAuthority(f.path)
  try {
    other.db
      .prepare('UPDATE revocations SET payload=? WHERE sequence=1')
      .run(JSON.stringify({ kind: 'source', sceneId: 's', messageId: 'changed' }))
    const last = other.db
      .prepare('SELECT sequence,hash FROM revocations ORDER BY sequence DESC LIMIT 1')
      .get()
    assert.deepEqual(last, { sequence: f.initial.sequence, hash: f.initial.hash })
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    assert.throws(
      () => f.authority.append({ kind: 'source', sceneId: 's', messageId: 'later' }, f.initial),
      /recovery_chain_invalid/,
    )
  } finally {
    other.close()
    f.close()
  }
})

test('a valid external replacement is reverified and caller rollback cannot poison a committed head', () => {
  const f = fixture()
  try {
    f.authority.db.exec('BEGIN')
    const transient = replaceLast(f.authority, f.initial, 'transient')
    assert.deepEqual(f.authority.head(), transient)
    assert.deepEqual(f.authority.head(), transient)
    f.authority.db.exec('ROLLBACK')
    assert.deepEqual(f.authority.head(), f.initial)
    assert.equal(f.authority.entries().at(-1)!.revocation.kind, 'source')
    f.authority.db.exec('BEGIN')
    const committed = replaceLast(f.authority, f.initial, 'committed')
    assert.deepEqual(f.authority.head(), committed)
    f.authority.db.exec('COMMIT')
    assert.deepEqual(f.authority.head(), committed)
  } finally {
    f.close()
  }
})

test('failed validation and a transient repair followed by rollback never publish the repaired head', () => {
  const f = fixture()
  try {
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run(zero)
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    f.authority.db.exec('BEGIN')
    const row = f.authority.db
      .prepare('SELECT previous_hash,payload FROM revocations WHERE sequence=1')
      .get() as { previous_hash: string; payload: string }
    f.authority.db
      .prepare('UPDATE revocations SET hash=? WHERE sequence=1')
      .run(digest(`${f.initial.instanceId}|1|${row.previous_hash}|${row.payload}`))
    assert.deepEqual(f.authority.head(), f.initial)
    f.authority.db.exec('ROLLBACK')
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('a nested append rolled back to a savepoint does not poison head or duplicate membership', () => {
  const f = fixture()
  const value = { kind: 'source' as const, sceneId: 's', messageId: 'savepoint' }
  try {
    f.authority.db.exec('BEGIN; SAVEPOINT caller')
    const temporary = f.authority.append(value, f.initial)
    assert.equal(temporary.sequence, f.initial.sequence + 1)
    assert.deepEqual(f.authority.head(), temporary)
    f.authority.db.exec('ROLLBACK TO caller; RELEASE caller; COMMIT')
    assert.deepEqual(f.authority.head(), f.initial)
    const committed = f.authority.append(value, f.initial)
    assert.equal(committed.sequence, f.initial.sequence + 1)
    assert.equal(f.authority.entries().length, committed.sequence)
  } finally {
    f.close()
  }
})

test('a failed authority COMMIT neither acknowledges intent nor remembers an uncommitted duplicate', () => {
  const f = fixture()
  const reader = new RecoveryAuthority(f.path)
  const value = { kind: 'source' as const, sceneId: 's', messageId: 'commit-failure' }
  try {
    f.authority.db.pragma('busy_timeout = 0')
    reader.db.exec('BEGIN')
    reader.db.prepare('SELECT id FROM identity').get()
    assert.throws(() => f.authority.append(value, f.initial), /locked/)
    assert.equal(f.authority.db.inTransaction, false)
    reader.db.exec('ROLLBACK')
    assert.deepEqual(f.authority.head(), f.initial)
    const committed = f.authority.append(value, f.initial)
    assert.equal(committed.sequence, f.initial.sequence + 1)
    assert.equal(f.authority.entries().length, committed.sequence)
  } finally {
    if (reader.db.inTransaction) reader.db.exec('ROLLBACK')
    reader.close()
    f.close()
  }
})

test('identity and schema changes invalidate the cached committed integrity on each connection', () => {
  const f = fixture()
  const other = new RecoveryAuthority(f.path)
  try {
    other.db.exec('CREATE TABLE additional_metadata(value TEXT)')
    assert.equal(
      hashCalls(() => assert.deepEqual(f.authority.head(), f.initial)),
      f.initial.sequence,
    )
    assert.equal(
      hashCalls(() => assert.deepEqual(f.authority.head(), f.initial)),
      0,
    )
    f.authority.db.exec('CREATE TABLE own_metadata(value TEXT)')
    assert.equal(
      hashCalls(() => assert.deepEqual(f.authority.head(), f.initial)),
      f.initial.sequence,
    )
    other.db.prepare('UPDATE identity SET id=?').run('wrong-instance')
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
  } finally {
    other.close()
    f.close()
  }
})

test('authority snapshot is pinned before its stamp can admit a concurrent SQLite write', () => {
  const f = fixture()
  const other = new RecoveryAuthority(f.path)
  const prototype = Object.getPrototypeOf(f.authority.db.prepare('SELECT 1'))
  const originalGet = prototype.get
  let attempted = false
  try {
    other.db.pragma('busy_timeout = 0')
    prototype.get = function (...args: unknown[]) {
      const result = originalGet.apply(this, args)
      if (/^SELECT id FROM (?:main\.)?identity$/.test(this.source) && !attempted) {
        attempted = true
        assert.throws(
          () => other.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run(zero),
          /locked/,
        )
      }
      return result
    }
    assert.deepEqual(f.authority.head(), f.initial)
    assert.equal(attempted, true)
    assert.deepEqual(f.authority.head(), f.initial)
  } finally {
    prototype.get = originalGet
    other.close()
    f.close()
  }
})

test('a commit after verification unlocks cannot be stamped as already verified', () => {
  const f = fixture()
  const other = new RecoveryAuthority(f.path)
  const original = f.authority.db.transaction.bind(f.authority.db)
  let injected = false
  try {
    f.authority.db.transaction = ((run: (...args: unknown[]) => unknown) => {
      const transaction = original(run)
      const wrapped = (...args: unknown[]) => {
        const result = transaction(...args)
        if (!injected) {
          injected = true
          other.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run(zero)
        }
        return result
      }
      return Object.assign(wrapped, {
        deferred: wrapped,
        immediate: transaction.immediate,
        exclusive: transaction.exclusive,
      })
    }) as typeof f.authority.db.transaction
    // The completed read is valid at its pinned snapshot. The following operation
    // must notice the newer external commit rather than reuse the old verification.
    assert.deepEqual(f.authority.head(), f.initial)
    assert.equal(injected, true)
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
  } finally {
    f.authority.db.transaction = original
    other.close()
    f.close()
  }
})

for (const [name, body] of [
  ['ignore insert', 'BEFORE INSERT ON revocations BEGIN SELECT RAISE(IGNORE); END'],
  [
    'mutate historical row and ignore insert',
    `BEFORE INSERT ON revocations BEGIN UPDATE revocations SET hash='${zero}' WHERE sequence=1; SELECT RAISE(IGNORE); END`,
  ],
  [
    'mutate new row',
    `AFTER INSERT ON revocations BEGIN UPDATE revocations SET hash='${zero}' WHERE sequence=NEW.sequence; END`,
  ],
  [
    'mutate historical row',
    `AFTER INSERT ON revocations BEGIN UPDATE revocations SET hash='${zero}' WHERE sequence=1; END`,
  ],
] as const)
  test(`an externally installed trigger cannot bless append when it would ${name}`, () => {
    const f = fixture()
    const other = new RecoveryAuthority(f.path)
    try {
      other.db.exec(`CREATE TRIGGER change_append ${body}`)
      assert.throws(
        () =>
          f.authority.append({ kind: 'source', sceneId: 's', messageId: 'triggered' }, f.initial),
        /recovery_/,
      )
      assert.equal(f.authority.db.inTransaction, false)
      assert.deepEqual(f.authority.head(), f.initial)
      assert.equal(f.authority.entries().length, f.initial.sequence)
      other.db.exec('DROP TRIGGER change_append')
      assert.equal(
        f.authority.append({ kind: 'source', sceneId: 's', messageId: 'triggered' }, f.initial)
          .sequence,
        f.initial.sequence + 1,
      )
    } finally {
      other.close()
      f.close()
    }
  })

test('a legacy authority receives the dedup index without changing chain format or exact events', () => {
  const f = fixture()
  let reopened: RecoveryAuthority | undefined
  try {
    f.authority.db.exec('DROP INDEX revocations_payload')
    const before = f.authority.db.prepare('SELECT * FROM revocations ORDER BY sequence').all()
    f.authority.close()
    assert.equal(
      hashCalls(() => {
        reopened = new RecoveryAuthority(f.path)
      }),
      f.initial.sequence,
    )
    assert.deepEqual(reopened!.head(), f.initial)
    assert.deepEqual(
      reopened!.db.prepare('SELECT * FROM revocations ORDER BY sequence').all(),
      before,
    )
    const plan = reopened!.db
      .prepare('EXPLAIN QUERY PLAN SELECT 1 FROM revocations WHERE payload=?')
      .all('synthetic-missing-payload') as Array<{ detail: string }>
    assert(plan.some((row) => row.detail.includes('COVERING INDEX revocations_payload')))
    assert.deepEqual(reopened!.db.prepare('SELECT format FROM identity').get(), { format: 1 })
    const duplicate = { kind: 'source' as const, sceneId: 's', messageId: 'm1' }
    assert.deepEqual(reopened!.append(duplicate, f.initial), f.initial)
  } finally {
    reopened?.close()
    f.close()
  }
})

test('simultaneous legacy openers converge on one compatible dedup index', async () => {
  const f = fixture()
  try {
    f.authority.db.exec('DROP INDEX revocations_payload')
    f.authority.close()
    const program = `
      import { RecoveryAuthority } from './src/memory/recovery/ledger.ts';
      const a = new RecoveryAuthority(process.argv[1]);
      try { process.stdout.write(JSON.stringify(a.head())); } finally { a.close(); }
    `
    const run = promisify(execFile)
    const outputs = await Promise.all(
      [0, 1].map(() =>
        run(process.execPath, ['--import', 'tsx', '--input-type=module', '-e', program, f.path], {
          cwd: process.cwd(),
        }),
      ),
    )
    for (const output of outputs) assert.deepEqual(JSON.parse(output.stdout), f.initial)
    const reopened = new RecoveryAuthority(f.path)
    try {
      const indexes = reopened.db
        .prepare("SELECT name FROM sqlite_master WHERE type='index' AND name='revocations_payload'")
        .all()
      assert.equal(indexes.length, 1)
      assert.deepEqual(reopened.head(), f.initial)
    } finally {
      reopened.close()
    }
  } finally {
    f.close()
  }
})

test('building the additive index cannot allow a corrupted legacy authority to open', () => {
  const f = fixture()
  try {
    f.authority.db.exec('DROP INDEX revocations_payload')
    f.authority.db.prepare('UPDATE revocations SET hash=? WHERE sequence=1').run(zero)
    f.authority.close()
    assert.throws(() => new RecoveryAuthority(f.path), /recovery_chain_invalid/)
  } finally {
    f.close()
  }
})

test('a view substituted for authority storage cannot acquire trusted incremental append semantics', () => {
  const f = fixture()
  try {
    f.authority.db.exec(`
      ALTER TABLE revocations RENAME TO original_revocations;
      CREATE VIEW revocations AS SELECT * FROM original_revocations;
    `)
    assert.throws(() => f.authority.head(), /recovery_/)
    f.authority.close()
    assert.throws(() => new RecoveryAuthority(f.path), /recovery_|views may not be indexed/)
  } finally {
    f.close()
  }
})

test('a replacement table with hidden ON CONFLICT REPLACE deletes is not the authority storage format', () => {
  const f = fixture()
  try {
    f.authority.db.exec(`
      ALTER TABLE revocations RENAME TO original_revocations;
      CREATE TABLE revocations (
        sequence INTEGER PRIMARY KEY,
        previous_hash TEXT NOT NULL,
        payload TEXT NOT NULL UNIQUE ON CONFLICT REPLACE,
        hash TEXT NOT NULL
      );
      INSERT INTO revocations SELECT * FROM original_revocations;
      DROP TABLE original_revocations;
    `)
    // Existing rows still form a valid chain. The changed table's hidden delete
    // behavior must not qualify for the single owned append optimization.
    assert.throws(() => f.authority.head(), /recovery_/)
    f.authority.close()
    assert.throws(() => new RecoveryAuthority(f.path), /recovery_/)
  } finally {
    f.close()
  }
})

function storeFixture() {
  const root = mkdtempSync(join(tmpdir(), 'recovery-durable-boundary-'))
  const database = join(root, 'data', 'state.sqlite')
  const store = new Store(database)
  const authority = (store as unknown as { recoveryAuthority: RecoveryAuthority }).recoveryAuthority
  return {
    root,
    database,
    store,
    authority,
    close() {
      if (authority.db.open && authority.db.inTransaction) authority.db.exec('ROLLBACK')
      if (store.db.open) store.close()
      rmSync(root, { recursive: true, force: true })
    },
  }
}

test('TEMP identity and revocations cannot shadow persistent head, prefix, deduplication or append', () => {
  const f = fixture()
  const stamp = () => ({
    totalChanges: f.authority.db.prepare('SELECT total_changes() n').get(),
    schemaVersion: f.authority.db.pragma('main.schema_version', { simple: true }),
    dataVersion: f.authority.db.pragma('main.data_version', { simple: true }),
  })
  try {
    const first = f.authority.prefix(1)
    const before = stamp()
    f.authority.db.exec(`
      CREATE TEMP TABLE identity AS SELECT 'shadow-instance' AS id, format FROM main.identity;
      CREATE TEMP TABLE revocations AS
        SELECT sequence, previous_hash, 'shadow-payload' AS payload, '${zero}' AS hash
        FROM main.revocations;
    `)
    assert.deepEqual(stamp(), before, 'TEMP CREATE AS does not change the main invalidation stamp')
    assert.deepEqual(f.authority.head(), f.initial)
    assert.deepEqual(f.authority.prefix(1), first)
    assert.deepEqual(
      f.authority.append({ kind: 'source', sceneId: 's', messageId: 'm1' }, f.initial),
      f.initial,
    )
    const next = f.authority.append(
      { kind: 'source', sceneId: 's', messageId: 'persistent-new' },
      f.initial,
    )
    assert.equal(next.sequence, f.initial.sequence + 1)
    assert.deepEqual(f.authority.prefix(next.sequence), next)
    assert.deepEqual(f.authority.db.prepare('SELECT count(*) n FROM main.revocations').get(), {
      n: next.sequence,
    })
    assert.deepEqual(f.authority.db.prepare('SELECT count(*) n FROM temp.revocations').get(), {
      n: f.initial.sequence,
    })
    assert.equal(
      f.authority.entries().length,
      next.sequence,
      'explicit audit also uses persistent storage',
    )
    assert.deepEqual(f.authority.head(), next)
  } finally {
    f.close()
  }
})

test('TEMP pragma tables cannot impersonate main-schema or external-write stamp values', () => {
  const f = fixture()
  const other = new RecoveryAuthority(f.path)
  try {
    const dataVersion = f.authority.db.pragma('main.data_version', { simple: true })
    const schemaVersion = f.authority.db.pragma('main.schema_version', { simple: true })
    f.authority.db.exec(`
      CREATE TEMP TABLE pragma_data_version AS SELECT ${dataVersion} AS data_version;
      CREATE TEMP TABLE pragma_schema_version AS SELECT ${schemaVersion} AS schema_version;
    `)
    assert.deepEqual(f.authority.head(), f.initial)
    f.authority.db.exec('CREATE TABLE main.stamp_schema_marker(value TEXT)')
    assert.equal(
      hashCalls(() => assert.deepEqual(f.authority.head(), f.initial)),
      f.initial.sequence,
    )
    other.db.prepare('UPDATE main.revocations SET hash=? WHERE sequence=1').run(zero)
    assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
  } finally {
    other.close()
    f.close()
  }
})

test('TEMP recovery_gate cannot replace persistent gate reads or acknowledgements', () => {
  const f = storeFixture()
  try {
    const gate = metadata(f.store.db)!
    f.store.db.exec('CREATE TEMP TABLE recovery_gate AS SELECT * FROM main.recovery_gate')
    f.store.db
      .prepare('UPDATE temp.recovery_gate SET value=?')
      .run(JSON.stringify({ ...gate, state: 'quarantine' }))
    assert.deepEqual(metadata(f.store.db), gate)
    f.store.assertRecoveryReady()
    const updated = { ...gate, minimumReader: gate.minimumReader + 1 }
    saveGate(f.store.db, updated)
    assert.deepEqual(metadata(f.store.db), updated)
    const temporary = f.store.db
      .prepare('SELECT value FROM temp.recovery_gate WHERE id=1')
      .get() as { value: string }
    assert.equal(JSON.parse(temporary.value).state, 'quarantine')
    assert.equal(JSON.parse(temporary.value).minimumReader, gate.minimumReader)
  } finally {
    f.close()
  }
})

for (const mode of ['revoke', 'begin'] as const)
  test(`durable ${mode} rejects a caller-owned authority transaction before main acknowledgement`, () => {
    const f = storeFixture()
    try {
      const gate = metadata(f.store.db)!
      const value = { kind: 'source' as const, sceneId: 's', messageId: `durable-${mode}` }
      f.authority.db.exec('BEGIN')
      if (mode === 'revoke') {
        assert.throws(
          () =>
            f.store.db
              .transaction(() => {
                revoke(f.store.db, value)
                f.store.db
                  .prepare('INSERT INTO config(key,value) VALUES(?,?)')
                  .run('ack-canary', 'true')
              })
              .immediate(),
          /recovery_.*transaction/,
        )
      } else
        assert.throws(() => beginDurableRevocation(f.store.db, value), /recovery_.*transaction/)
      assert.equal(
        f.authority.db.inTransaction,
        true,
        'the caller still owns its authority transaction',
      )
      assert.equal(f.store.db.inTransaction, false)
      assert.deepEqual(f.authority.head(), gate.head)
      assert.deepEqual(metadata(f.store.db), gate)
      assert.equal(
        f.store.db.prepare('SELECT 1 FROM config WHERE key=?').get('ack-canary'),
        undefined,
      )
      f.authority.db.exec('ROLLBACK')
      f.store.recall('s', `durable-${mode}`)
      assert.equal(f.authority.head().sequence, gate.head.sequence + 1)
      assert.deepEqual(metadata(f.store.db)!.head, f.authority.head())
    } finally {
      f.close()
    }
  })

test('offline maintenance effect capture does not append even with a caller-owned authority transaction', () => {
  const f = storeFixture()
  let authority: RecoveryAuthority | undefined
  let maintenance: Store | undefined
  try {
    const gate = metadata(f.store.db)!
    saveGate(f.store.db, { ...gate, state: 'quarantine' })
    f.store.close()
    authority = new RecoveryAuthority(gate.authority)
    maintenance = new Store(f.database, { authority })
    const value = { kind: 'source' as const, sceneId: 's', messageId: 'maintenance-only' }
    authority.db.exec('BEGIN')
    assert.deepEqual(
      captureRecoveryEffects(maintenance.db, () => revoke(maintenance!.db, value)),
      [value],
    )
    assert.equal(beginDurableRevocation(maintenance.db, value), undefined)
    assert.deepEqual(authority.head(), gate.head)
    assert.deepEqual(metadata(maintenance.db), { ...gate, state: 'quarantine' })
    assert.equal(authority.db.inTransaction, true)
  } finally {
    if (authority?.db.inTransaction) authority.db.exec('ROLLBACK')
    maintenance?.close()
    authority?.close()
    f.close()
  }
})

test('durable ticket commit rejects a caller-owned authority transaction without consuming the ticket', () => {
  const f = storeFixture()
  try {
    const before = metadata(f.store.db)!
    const ticket = beginDurableRevocation(f.store.db, {
      kind: 'source',
      sceneId: 's',
      messageId: 'durable-ticket',
    })!
    assert.equal(ticket.after.sequence, before.head.sequence + 1)
    let calls = 0
    f.authority.db.exec('BEGIN')
    assert.throws(
      () =>
        commitDurableRevocation(f.store.db, ticket, () => {
          calls++
          return true
        }),
      /recovery_.*transaction/,
    )
    assert.equal(calls, 0)
    assert.equal(f.authority.db.inTransaction, true)
    assert.equal(f.store.db.inTransaction, false)
    assert.deepEqual(metadata(f.store.db), before)
    assert.deepEqual(f.authority.head(), ticket.after)
    f.authority.db.exec('ROLLBACK')
    assert.equal(
      commitDurableRevocation(f.store.db, ticket, () => {
        calls++
        return true
      }),
      true,
    )
    assert.equal(calls, 1)
    assert.deepEqual(metadata(f.store.db)!.head, ticket.after)
  } finally {
    f.close()
  }
})

for (const name of ['data_version', 'schema_version'] as const)
  test(`ordinary main.pragma_${name} table cannot impersonate the literal bookkeeping PRAGMA`, () => {
    const f = fixture()
    const other = new RecoveryAuthority(f.path)
    try {
      const current = f.authority.db.pragma(`main.${name}`, { simple: true })
      f.authority.db.exec(`CREATE TABLE main.pragma_${name} AS SELECT ${current} AS ${name}`)
      assert.deepEqual(f.authority.head(), f.initial)
      f.authority.db.exec('CREATE TABLE main.literal_pragma_marker(value TEXT)')
      assert.equal(
        hashCalls(() => assert.deepEqual(f.authority.head(), f.initial)),
        f.initial.sequence,
      )
      other.db.prepare('UPDATE main.revocations SET hash=? WHERE sequence=1').run(zero)
      assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    } finally {
      other.close()
      f.close()
    }
  })

for (const name of ['data_version', 'schema_version'] as const)
  test(`FTS5 main.pragma_${name} virtual table cannot impersonate the literal bookkeeping PRAGMA`, () => {
    const f = fixture()
    const other = new RecoveryAuthority(f.path)
    try {
      f.authority.db.exec(`CREATE VIRTUAL TABLE main.pragma_${name} USING fts5(${name})`)
      f.authority.db.prepare(`INSERT INTO main.pragma_${name}(${name}) VALUES(?)`).run('999')
      const spoofed = f.authority.db
        .prepare(`SELECT ${name} n FROM main.pragma_${name}()`)
        .get() as { n: string }
      assert.equal(
        Number(spoofed.n),
        999,
        'function syntax alone can resolve a same-name virtual table',
      )
      assert.notEqual(f.authority.db.pragma(`main.${name}`, { simple: true }), 999)
      assert.deepEqual(f.authority.head(), f.initial)
      f.authority.db.exec('CREATE TABLE main.literal_pragma_marker(value TEXT)')
      assert.equal(
        hashCalls(() => assert.deepEqual(f.authority.head(), f.initial)),
        f.initial.sequence,
      )
      other.db.prepare('UPDATE main.revocations SET hash=? WHERE sequence=1').run(zero)
      assert.throws(() => f.authority.head(), /recovery_chain_invalid/)
    } finally {
      other.close()
      f.close()
    }
  })
