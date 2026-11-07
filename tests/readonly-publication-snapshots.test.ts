import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer } from 'node:http'
import { Worker } from 'node:worker_threads'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import {
  ConfigReadConnection,
  type PublicationTicket,
  type PublicationVerifiedResult,
  type PublicationVerifyRequest,
} from '../src/server/config-read.ts'
import { PublicationGate } from '../src/server/publication-gate.ts'
import type { Operation, WorkerRpc } from '../src/server/worker-rpc.ts'

async function fixture(
  run: (store: Store, actor: ConfigReadConnection, authority: RecoveryAuthority) => Promise<void>,
) {
  const root = mkdtempSync(join(tmpdir(), 'he-readonly-snapshot-'))
  const store = new Store(join(root, 'data', 'live.sqlite'))
  const actor = new ConfigReadConnection(store.db.name)
  try {
    await run(store, actor, Reflect.get(actor, 'authority') as RecoveryAuthority)
  } finally {
    actor.close()
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}
function idle(actor: ConfigReadConnection, authority: RecoveryAuthority) {
  assert.equal((Reflect.get(actor, 'db') as Database.Database).inTransaction, false)
  assert.equal(authority.db.inTransaction, false)
  assert.equal((Reflect.get(actor, 'reservation') as Database.Database).inTransaction, false)
}
function waitFlag(flags: Int32Array, index: number, expected: number) {
  // Failure guard for a deterministic schedule, not a product deadline or latency assertion.
  if (Atomics.load(flags, index) === 0)
    assert.notEqual(Atomics.wait(flags, index, 0, 10000), 'timed-out')
  assert.equal(Atomics.load(flags, index), expected)
}
function signal(flags: Int32Array, index: number) {
  Atomics.store(flags, index, 1)
  Atomics.notify(flags, index)
}

test('actor reserves authority before its main pin: concurrent real writer cannot split HTTP head and body', async () => {
  await fixture(async (store, actor, authority) => {
    store.set('settings', { ...store.settings, name: 'Before concurrent writer' })
    const initialHead = metadata(store.db)!.head
    const main = Reflect.get(actor, 'db') as Database.Database
    const flags = new Int32Array(new SharedArrayBuffer(5 * Int32Array.BYTES_PER_ELEMENT))
    const writer = new Worker(
      new URL('./fixtures/readonly-publication-writer.mjs', import.meta.url),
      {
        workerData: { database: store.db.name, flags: flags.buffer },
      },
    )
    let next = 0
    const verified: PublicationVerifiedResult[] = []
    const rpc = {
      async call(op: Operation, payload: unknown) {
        if (op === 'publication-begin') return actor.beginPublication(++next)
        if (op === 'publication-verify') {
          const result = actor.verifyPublication(payload as PublicationVerifyRequest)
          verified.push(result)
          return result
        }
        if (op === 'publication-abandon')
          return actor.abandonPublication((payload as PublicationTicket).ticket)
        throw new Error('unexpected_test_operation')
      },
      releaseResult(grant: PublicationTicket) {
        actor.releasePublicationTicket(grant.ticket)
      },
    }
    const host = new PublicationGate(
      store.db.name,
      actor.publicationInfo(),
      rpc as unknown as WorkerRpc,
    )
    const failures: unknown[] = []
    const server = createServer(async (_request, response) => {
      try {
        await host.publish(
          {
            path: '/api/settings',
            result: {
              status: 200,
              headers: [['Content-Type', 'application/json']],
              body: new Uint8Array(),
            },
          },
          response,
          new AbortController().signal,
        )
      } catch (error) {
        failures.push(error)
        response.writeHead(503)
        response.end()
      }
    })
    const prototype = Object.getPrototypeOf(main.prepare('SELECT 1'))
    const originalGet = prototype.get
    let attempted = false
    try {
      waitFlag(flags, 0, 1)
      prototype.get = function (...args: unknown[]) {
        const row = originalGet.apply(this, args)
        if (
          this.database === main &&
          this.source === 'SELECT value FROM main.recovery_gate WHERE id=1' &&
          !attempted
        ) {
          attempted = true
          assert.equal(authority.db.inTransaction, true)
          assert.equal((Reflect.get(actor, 'reservation') as Database.Database).inTransaction, true)
          assert.equal(main.inTransaction, true)
          signal(flags, 1)
          waitFlag(flags, 2, 2)
        }
        return row
      }
      await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
      const address = server.address()
      assert.ok(address && typeof address !== 'string')
      const url = `http://127.0.0.1:${address.port}`
      const before = await fetch(url)
      assert.equal(before.status, 200)
      assert.equal((await before.json()).name, 'Before concurrent writer')
      assert.equal(attempted, true)
      assert.deepEqual(verified[0]!.observedHead, initialHead)
      idle(actor, authority)
      signal(flags, 3)
      waitFlag(flags, 4, 1)
      const after = await fetch(url)
      assert.equal(after.status, 200)
      assert.equal((await after.json()).name, 'After concurrent writer')
      assert.deepEqual(verified[1]!.observedHead, metadata(store.db)!.head)
      assert.equal(verified[1]!.observedHead.sequence, initialHead.sequence + 1)
      assert.deepEqual(failures, [])
    } finally {
      prototype.get = originalGet
      await new Promise<void>((resolve) => server.close(() => resolve()))
      await host.close()
      await writer.terminate()
    }
  })
})

test('one foreign commit requires one actor scan; verify, rollback and abandoned leases reuse only that snapshot integrity', async () => {
  await fixture(async (store, actor, authority) => {
    const prototype = Object.getPrototypeOf(authority.db.prepare('SELECT 1'))
    const originalIterate = prototype.iterate
    let scans = 0
    prototype.iterate = function (...args: unknown[]) {
      if (
        this.database === authority.db &&
        this.source === 'SELECT * FROM main.revocations ORDER BY sequence'
      )
        scans++
      return originalIterate.apply(this, args)
    }
    try {
      store.recall('synthetic', 'first')
      actor.beginPublication(1)
      assert.equal(scans, 1)
      actor.verifyPublication({ ticket: 1, path: '/api/settings' })
      assert.equal(scans, 1)
      actor.beginPublication(2)
      actor.abandonPublication(2)
      actor.beginPublication(3)
      actor.verifyPublication({ ticket: 3, path: '/api/settings' })
      assert.equal(scans, 1)
      store.recall('synthetic', 'second')
      actor.beginPublication(4)
      assert.equal(scans, 2)
      actor.abandonPublication(4)
      actor.beginPublication(5)
      actor.verifyPublication({ ticket: 5, path: '/api/settings' })
      assert.equal(scans, 2)
      idle(actor, authority)
    } finally {
      prototype.iterate = originalIterate
    }
  })
})

test('failure pinning main releases the earlier authority snapshot and leaves no lease', async () => {
  await fixture(async (store, actor, authority) => {
    const main = Reflect.get(actor, 'db') as Database.Database
    const originalExec = main.exec.bind(main)
    main.exec = ((sql: string) => {
      if (sql === 'BEGIN') {
        assert.equal(authority.db.inTransaction, true)
        throw new Error('synthetic_main_pin_failure')
      }
      return originalExec(sql)
    }) as typeof main.exec
    try {
      assert.throws(() => actor.beginPublication(1), /synthetic_main_pin_failure/)
      idle(actor, authority)
    } finally {
      main.exec = originalExec
    }
    store.recall('synthetic', 'after-pin-failure')
    actor.beginPublication(2)
    actor.verifyPublication({ ticket: 2, path: '/api/settings' })
    idle(actor, authority)
  })
})

test('actor rollback failure closes all three handles after attempting every release', async () => {
  await fixture(async (store, actor, authority) => {
    const main = Reflect.get(actor, 'db') as Database.Database
    const mainExec = main.exec.bind(main)
    const authorityExec = authority.db.exec.bind(authority.db)
    let mainReleased = false
    actor.beginPublication(1)
    authority.db.exec = ((sql: string) => {
      if (sql === 'ROLLBACK') throw new Error('synthetic_authority_rollback_failure')
      return authorityExec(sql)
    }) as typeof authority.db.exec
    main.exec = ((sql: string) => {
      if (sql === 'ROLLBACK') mainReleased = true
      return mainExec(sql)
    }) as typeof main.exec
    assert.throws(() => actor.abandonPublication(1), /synthetic_authority_rollback_failure/)
    assert.equal(mainReleased, true)
    assert.equal(main.open, false)
    assert.equal(authority.db.open, false)
    assert.equal((Reflect.get(actor, 'reservation') as Database.Database).open, false)
    assert.throws(() => actor.beginPublication(2), /configuration_reader_closed/)
    store.recall('synthetic', 'after-poisoned-reader-close')
  })
})
