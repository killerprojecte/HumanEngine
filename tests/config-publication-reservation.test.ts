import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata, type RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { ConfigReadConnection } from '../src/server/config-read.ts'

async function fixture(run: (store: Store, authorityFile: string) => void) {
  const root = mkdtempSync(join(tmpdir(), 'he-publication-reservation-'))
  const store = new Store(join(root, 'data', 'live.sqlite'))
  try {
    run(store, join(metadata(store.db)!.authority, 'authority.sqlite'))
  } finally {
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}
function connections(reader: ConfigReadConnection) {
  return {
    main: Reflect.get(reader, 'db') as Database.Database,
    authority: (Reflect.get(reader, 'authority') as RecoveryAuthority).db,
    reservation: Reflect.get(reader, 'reservation') as Database.Database,
  }
}
function fingerprint(file: string) {
  return {
    hash: createHash('sha256').update(readFileSync(file)).digest('hex'),
    mtime: statSync(file, { bigint: true }).mtimeNs,
    mode: statSync(file).mode,
    journal: existsSync(`${file}-journal`),
  }
}

test('native capability proof runs once without row triggers, changes, file writes or journal creation', async () => {
  await fixture((store, file) => {
    const writer = new Database(file, { fileMustExist: true, timeout: 0 })
    writer.exec(
      'CREATE TABLE proof_effects(n); INSERT INTO proof_effects VALUES(0); ' +
        'CREATE TRIGGER proof_watch AFTER UPDATE ON identity BEGIN UPDATE proof_effects SET n=n+1; END',
    )
    const before = fingerprint(file),
      prepare = Database.prototype.prepare
    let proofs = 0,
      reader: ConfigReadConnection | undefined
    Database.prototype.prepare = function (
      this: Database.Database,
      ...args: Parameters<typeof prepare>
    ) {
      if (this.name === file && args[0] === 'UPDATE main.identity SET id=id WHERE 0') proofs++
      return prepare.apply(this, args)
    } as typeof prepare
    try {
      reader = new ConfigReadConnection(store.db.name)
      const handles = connections(reader)
      assert.equal(handles.reservation.readonly, false)
      assert.equal(handles.reservation.pragma('busy_timeout', { simple: true }), 5000)
      assert.equal(handles.authority.readonly, true)
      assert.deepEqual(fingerprint(file), before)
      for (let ticket = 1; ticket <= 25; ticket++) {
        reader.beginPublication(ticket)
        assert.equal(handles.reservation.inTransaction, true)
        assert.throws(() => writer.exec('BEGIN IMMEDIATE'), { code: 'SQLITE_BUSY' })
        reader.verifyPublication({ ticket, path: '/api/settings' })
        for (const db of Object.values(handles)) assert.equal(db.inTransaction, false)
      }
      assert.equal(proofs, 1)
      assert.deepEqual(handles.reservation.prepare('SELECT total_changes() AS n').get(), { n: 0 })
      assert.deepEqual(writer.prepare('SELECT n FROM proof_effects').get(), { n: 0 })
      assert.deepEqual(fingerprint(file), before)
      writer.exec('BEGIN IMMEDIATE; ROLLBACK')
    } finally {
      Database.prototype.prepare = prepare
      reader?.close()
      writer.close()
    }
  })
})

for (const name of ['main', 'authority', 'reservation'] as const) {
  test(`${name} rollback failure attempts all three releases and permanently closes the actor`, async () => {
    await fixture((store, file) => {
      const reader = new ConfigReadConnection(store.db.name),
        handles = connections(reader),
        rolledBack: string[] = []
      reader.beginPublication(1)
      for (const [key, db] of Object.entries(handles)) {
        const exec = db.exec.bind(db)
        db.exec = ((sql: string) => {
          if (sql === 'ROLLBACK') {
            rolledBack.push(key)
            if (key === name) throw new Error(`synthetic_${name}_rollback_failure`)
          }
          return exec(sql)
        }) as typeof db.exec
      }
      try {
        assert.throws(() => reader.abandonPublication(1), /rollback_failure/)
        assert.deepEqual(rolledBack, ['authority', 'main', 'reservation'])
        for (const db of Object.values(handles)) assert.equal(db.open, false)
        assert.throws(() => reader.beginPublication(2), /configuration_reader_closed/)
        const writer = new Database(file, { fileMustExist: true, timeout: 0 })
        try {
          writer.exec('BEGIN EXCLUSIVE; ROLLBACK')
        } finally {
          writer.close()
        }
      } finally {
        reader.close()
      }
    })
  })
}

for (const name of ['main', 'authority', 'reservation'] as const) {
  test(`${name} acquisition failure releases every previously acquired handle`, async () => {
    await fixture((store) => {
      const reader = new ConfigReadConnection(store.db.name),
        handles = connections(reader),
        db = handles[name],
        exec = db.exec.bind(db)
      let attempted = false
      db.exec = ((sql: string) => {
        if (sql.startsWith('BEGIN') && !attempted) {
          attempted = true
          throw new Error(`synthetic_${name}_begin_failure`)
        }
        return exec(sql)
      }) as typeof db.exec
      try {
        assert.throws(() => reader.beginPublication(1), /begin_failure/)
        assert.equal(attempted, true)
        for (const connection of Object.values(handles))
          assert.equal(connection.inTransaction, false)
        reader.beginPublication(2)
        reader.verifyPublication({ ticket: 2, path: '/api/settings' })
        for (const connection of Object.values(handles))
          assert.equal(connection.inTransaction, false)
      } finally {
        reader.close()
      }
    })
  })
}

test('constructor capability-proof rollback failure closes all three opened handles', async () => {
  await fixture((store, file) => {
    const exec = Database.prototype.exec,
      pragma = Database.prototype.pragma,
      opened = new Set<Database.Database>()
    Database.prototype.pragma = function (...args: Parameters<typeof pragma>) {
      opened.add(this)
      return pragma.apply(this, args)
    }
    Database.prototype.exec = function (sql: string) {
      if (this.name === file && !this.readonly && sql === 'ROLLBACK')
        throw new Error('synthetic_proof_rollback_failure')
      return exec.call(this, sql)
    }
    try {
      assert.throws(() => new ConfigReadConnection(store.db.name), /proof_rollback_failure/)
      assert.equal(opened.size, 3)
      for (const db of opened) assert.equal(db.open, false)
    } finally {
      Database.prototype.pragma = pragma
      Database.prototype.exec = exec
    }
    const reader = new ConfigReadConnection(store.db.name)
    reader.beginPublication(1)
    reader.verifyPublication({ ticket: 1 })
    reader.close()
  })
})
