import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Worker } from 'node:worker_threads'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata, saveGate } from '../src/memory/recovery/ledger.ts'
import {
  type PublicationInfo,
  type PublicationTicket,
  type PublicationVerifiedResult,
} from '../src/server/config-read.ts'

async function actor(database: string, bootstrap = '', workerData: object = {}) {
  const source = new URL('../src/server/config-worker.ts', import.meta.url)
  const worker = new Worker(
    `import { register } from 'tsx/esm/api'; register(); ${bootstrap}; await import(${JSON.stringify(source.href)})`,
    { eval: true, workerData: { database, ...workerData } },
  )
  await new Promise<void>((resolve, reject) => {
    worker.once('error', reject)
    worker.once('message', (message) =>
      message.ready ? resolve() : reject(new Error('reader_startup_failed')),
    )
  })
  let next = 0
  const pending = new Map<
    number,
    { resolve: (value: any) => void; reject: (error: Error) => void }
  >()
  worker.on('message', (message) => {
    const request = pending.get(message.id)
    if (!request) return
    pending.delete(message.id)
    message.error
      ? request.reject(new Error('reader_request_failed'))
      : request.resolve(message.result)
  })
  worker.on('error', (error) => {
    for (const request of pending.values()) request.reject(error)
    pending.clear()
  })
  function start<T>(op: string, payload: unknown = null) {
    const id = ++next
    const result = new Promise<T>((resolve, reject) => pending.set(id, { resolve, reject }))
    worker.postMessage({ id, op, payload })
    return { id, result }
  }
  return {
    worker,
    start,
    call: <T>(op: string, payload: unknown = null) => start<T>(op, payload).result,
    notify: (message: object) => worker.postMessage(message),
    close: async () => {
      if (worker.threadId === -1) return
      await start('close').result
      await worker.terminate()
    },
  }
}

async function fixture(
  run: (
    store: Store,
    reader: Awaited<ReturnType<typeof actor>>,
    authority: Database.Database,
    database: string,
  ) => Promise<void>,
) {
  const root = mkdtempSync(join(tmpdir(), 'he-publication-actor-'))
  const database = join(root, 'data', 'live.sqlite')
  const store = new Store(database)
  const reader = await actor(database)
  const authority = new Database(join(metadata(store.db)!.authority, 'authority.sqlite'), {
    timeout: 0,
  })
  try {
    await run(store, reader, authority, database)
  } finally {
    await reader.close()
    authority.close()
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}

function assertAuthorityPinned(authority: Database.Database) {
  assert.throws(() => authority.exec('BEGIN IMMEDIATE'), { code: 'SQLITE_BUSY' })
  assert.equal(authority.inTransaction, false)
}
function assertAuthorityReleased(authority: Database.Database) {
  authority.transaction(() => authority.exec('UPDATE identity SET format=format')).immediate()
}

const decode = (result: PublicationVerifiedResult) =>
  JSON.parse(new TextDecoder().decode(result.body))

test('publication actor reports opened connection settings and encodes fixed projections before releasing', async () => {
  await fixture(async (store, reader, authority) => {
    const info = await reader.call<PublicationInfo>('publication-info')
    assert.equal(info.authorityPath, metadata(store.db)!.authority)
    assert.equal(info.mainTimeout, store.db.pragma('busy_timeout', { simple: true }))
    assert.equal(info.authorityTimeout, 5000)
    assert.equal(authority.pragma('journal_mode', { simple: true }), 'delete')
    for (const [path, expected] of [
      ['/api/settings', store.settings],
      ['/api/scenes', store.scenes()],
      ['/api/models', store.models],
    ] as const) {
      const lease = await reader.call<PublicationTicket>('publication-begin')
      const verified = await reader.call<PublicationVerifiedResult>('publication-verify', {
        ...lease,
        path,
      })
      assert.deepEqual(decode(verified), expected)
      assert.deepEqual(verified.observedHead, metadata(store.db)!.head)
      assertAuthorityReleased(authority)
    }
    const lease = await reader.call<PublicationTicket>('publication-begin')
    const verified = await reader.call<PublicationVerifiedResult>('publication-verify', lease)
    assert.deepEqual(verified, { observedHead: metadata(store.db)!.head })
    assertAuthorityReleased(authority)
    await assert.rejects(reader.call('config', '/api/settings'))
  })
})

test('publication actor pins both snapshots and rejects overlapping or mismatched tickets without releasing', async () => {
  await fixture(async (store, reader, authority) => {
    const before = store.settings
    const lease = await reader.call<PublicationTicket>('publication-begin')
    assertAuthorityPinned(authority)
    store.set('settings', { ...before, name: 'Written after main snapshot' })
    await assert.rejects(reader.call('publication-begin'))
    await assert.rejects(reader.call('publication-verify', { ticket: lease.ticket + 1000 }))
    await assert.rejects(reader.call('publication-abandon', { ticket: lease.ticket + 1000 }))
    reader.notify({ release: lease.ticket + 1000 })
    reader.notify({ cancel: lease.ticket + 1000 })
    await reader.call('publication-info')
    assertAuthorityPinned(authority)
    const verified = await reader.call<PublicationVerifiedResult>('publication-verify', {
      ...lease,
      path: '/api/settings',
    })
    assert.deepEqual(decode(verified), before)
    assertAuthorityReleased(authority)
    await assert.rejects(reader.call('publication-verify', lease))
    const fresh = await reader.call<PublicationTicket>('publication-begin')
    assert.equal(
      decode(
        await reader.call<PublicationVerifiedResult>('publication-verify', {
          ...fresh,
          path: '/api/settings',
        }),
      ).name,
      'Written after main snapshot',
    )
  })
})

test('publication release, cancel, abandon and close release only their own lease and are idempotent', async () => {
  await fixture(async (_store, reader, authority) => {
    const first = await reader.call<PublicationTicket>('publication-begin')
    assert.equal(await reader.call('publication-abandon', first), null)
    assert.equal(await reader.call('publication-abandon', first), null)
    assertAuthorityReleased(authority)
    const second = await reader.call<PublicationTicket>('publication-begin')
    reader.notify({ release: first.ticket })
    reader.notify({ cancel: first.ticket })
    await reader.call('publication-info')
    assertAuthorityPinned(authority)
    reader.notify({ release: second.ticket })
    reader.notify({ release: second.ticket })
    await reader.call('publication-info')
    assertAuthorityReleased(authority)
    const third = await reader.call<PublicationTicket>('publication-begin')
    reader.notify({ cancel: third.ticket })
    reader.notify({ cancel: third.ticket })
    await reader.call('publication-info')
    assertAuthorityReleased(authority)
    await assert.rejects(reader.call('publication-verify', third))
    await reader.call('publication-begin')
    await reader.call('close')
    assertAuthorityReleased(authority)
  })
})

test('publication verification rejects bad projections and live gate changes, releasing every matching lease', async () => {
  await fixture(async (store, reader, authority) => {
    const gate = metadata(store.db)!
    const settings = store.settings
    for (const change of [
      () => saveGate(store.db, { ...gate, state: 'quarantine' }),
      () => saveGate(store.db, { ...gate, minimumReader: 99999 }),
      () => saveGate(store.db, { ...gate, authority: join(gate.authority, 'other') }),
      () => store.db.prepare("INSERT OR REPLACE INTO config VALUES('settings','not-json')").run(),
    ]) {
      change()
      const lease = await reader.call<PublicationTicket>('publication-begin')
      await assert.rejects(reader.call('publication-verify', { ...lease, path: '/api/settings' }))
      assertAuthorityReleased(authority)
      saveGate(store.db, gate)
      store.db
        .prepare("INSERT OR REPLACE INTO config VALUES('settings', ?)")
        .run(JSON.stringify(settings))
    }
    const lease = await reader.call<PublicationTicket>('publication-begin')
    await assert.rejects(reader.call('publication-verify', { ...lease, path: '/api/unknown' }))
    assertAuthorityReleased(authority)
    const fresh = await reader.call<PublicationTicket>('publication-begin')
    assert.deepEqual(
      decode(
        await reader.call<PublicationVerifiedResult>('publication-verify', {
          ...fresh,
          path: '/api/settings',
        }),
      ),
      settings,
    )
  })
})

test('publication warmup detects supported external same-head chain tampering before any lease escapes', async () => {
  await fixture(async (store, reader, authority) => {
    store.recall('s', 'original')
    const lease = await reader.call<PublicationTicket>('publication-begin')
    await reader.call('publication-verify', lease)
    authority
      .prepare('UPDATE revocations SET payload=? WHERE sequence=1')
      .run('{"kind":"source","sceneId":"s","messageId":"tampered"}')
    await assert.rejects(reader.call('publication-begin'))
    assertAuthorityReleased(authority)
  })
})

test('cancel queued during the native reader wait releases the eventual begin lease', async () => {
  await fixture(async (_store, reader, authority) => {
    authority.exec('BEGIN EXCLUSIVE')
    const begin = reader.start<PublicationTicket>('publication-begin')
    const canceled = begin.result
    reader.notify({ cancel: begin.id })
    // The native busy wait belongs to the actor, not this event loop.
    await new Promise((resolve) => setTimeout(resolve, 100))
    authority.exec('ROLLBACK')
    const lease = await canceled
    assert.equal(lease.ticket, begin.id)
    await reader.call('publication-info')
    assertAuthorityReleased(authority)
    await assert.rejects(reader.call('publication-verify', lease))
  })
})

test(
  'reservation excludes a waiting writer while verify uses existing snapshots and releases before replying',
  { timeout: 15000 },
  async () => {
    await fixture(async (_store, reader, authority) => {
      const lease = await reader.call<PublicationTicket>('publication-begin')
      const writer = new Worker(
        `import Database from 'better-sqlite3';
       import { parentPort, workerData } from 'node:worker_threads';
       const db = new Database(workerData.file);
       try {
         parentPort.postMessage('reserving');
         db.exec('BEGIN IMMEDIATE; UPDATE identity SET format=format');
         db.exec('COMMIT');
         parentPort.postMessage('committed');
       } finally { db.close(); }`,
        { eval: true, workerData: { file: authority.name } },
      )
      const committing = new Promise<void>((resolve, reject) => {
        writer.once('error', reject)
        writer.once('message', (message) => {
          assert.equal(message, 'reserving')
          resolve()
        })
      })
      const committed = new Promise<void>((resolve, reject) => {
        writer.once('error', reject)
        writer.on('message', (message) => {
          if (message === 'committed') resolve()
        })
      })
      try {
        await committing
        // The reservation blocks new writers before PENDING, while fixed reads
        // still succeed. Verification must release it before the writer can commit.
        assertAuthorityPinned(authority)
        assert(authority.prepare('SELECT id FROM identity').get())
        const started = performance.now()
        const result = await reader.call<PublicationVerifiedResult>('publication-verify', {
          ...lease,
          path: '/api/settings',
        })
        assert(result.body instanceof Uint8Array)
        assert(
          performance.now() - started < 1500,
          'verify acquired a new lock and waited on the writer',
        )
        await committed
        assertAuthorityReleased(authority)
      } finally {
        await reader.call('publication-abandon', lease)
        await writer.terminate()
      }
    })
  },
)

test('a failed begin-grant send releases both pinned snapshots before reporting failure', async () => {
  await fixture(async (_store, _reader, authority, database) => {
    const failing = await actor(
      database,
      `
      const { parentPort } = await import('node:worker_threads');
      const original = parentPort.postMessage.bind(parentPort);
      let failed = false;
      parentPort.postMessage = (message, ...rest) => {
        if (!failed && message.result?.ticket) {
          failed = true;
          throw new Error('synthetic_begin_send_failure');
        }
        return original(message, ...rest);
      };
    `,
    )
    try {
      await assert.rejects(failing.call('publication-begin'))
      assertAuthorityReleased(authority)
      const lease = await failing.call<PublicationTicket>('publication-begin')
      await failing.call('publication-verify', lease)
      assertAuthorityReleased(authority)
    } finally {
      await failing.close()
    }
  })
})

test('a closed parent port explicitly rolls back and closes all three actor handles', async () => {
  await fixture(async (_store, _reader, authority, database) => {
    const state = new Int32Array(new SharedArrayBuffer(4))
    const disconnected = await actor(
      database,
      `
      const { parentPort, workerData } = await import('node:worker_threads');
      const { default: Database } = await import('better-sqlite3');
      const original = Database.prototype.close;
      Database.prototype.close = function () {
        original.call(this);
        Atomics.add(new Int32Array(workerData.state), 0, 1);
        return this;
      };
      parentPort.on('message', message => {
        if (message.fixtureClosePort) parentPort.close();
      });
    `,
      { state: state.buffer },
    )
    try {
      await disconnected.call('publication-begin')
      assertAuthorityPinned(authority)
      const exited = new Promise<void>((resolve, reject) => {
        disconnected.worker.once('error', reject)
        disconnected.worker.once('exit', (code) => {
          assert.equal(code, 0)
          resolve()
        })
      })
      disconnected.notify({ fixtureClosePort: true })
      await exited
      assert.equal(state[0], 3, 'all three SQLite close methods were invoked before actor exit')
      assertAuthorityReleased(authority)
    } finally {
      await disconnected.worker.terminate()
    }
  })
})

test('begin rejects an authority journal changed to WAL since reader startup', async () => {
  await fixture(async (_store, reader, authority) => {
    assert.equal(authority.pragma('journal_mode = WAL', { simple: true }), 'wal')
    await assert.rejects(reader.call('publication-begin'))
    assertAuthorityReleased(authority)
  })
})

test('rollback failure attempts all three actor rollbacks and closes before preventing another publication', async () => {
  await fixture(async (_store, _reader, authority, database) => {
    for (const [failAt, operation] of [
      ['authority', 'publication-verify'],
      ['main', 'publication-abandon'],
      ['reservation', 'publication-verify'],
      ['all', 'cancel'],
    ] as const) {
      const state = new Int32Array(new SharedArrayBuffer(7 * 4))
      const failing = await actor(
        database,
        `
        const { workerData } = await import('node:worker_threads');
        const { default: Database } = await import('better-sqlite3');
        const state = new Int32Array(workerData.state);
        const exec = Database.prototype.exec;
        const close = Database.prototype.close;
        Database.prototype.exec = function (sql) {
          if (sql === 'ROLLBACK' && Atomics.load(state, 0)) {
            const kind = this.name.endsWith('/authority.sqlite')
              ? (this.readonly ? 'authority' : 'reservation') : 'main';
            Atomics.add(state, kind === 'authority' ? 1 : kind === 'main' ? 2 : 3, 1);
            if (workerData.failAt === 'all' || workerData.failAt === kind)
              throw new Error('synthetic_rollback_failure');
          }
          return exec.call(this, sql);
        };
        Database.prototype.close = function () {
          const index = this.name.endsWith('/authority.sqlite') ? (this.readonly ? 4 : 6) : 5;
          const result = close.call(this);
          Atomics.add(state, index, 1);
          return result;
        };
      `,
        { state: state.buffer, failAt },
      )
      const main = new Database(database, { timeout: 0 })
      try {
        const lease = await failing.call<PublicationTicket>('publication-begin')
        assertAuthorityPinned(authority)
        Atomics.store(state, 0, 1)
        if (operation === 'cancel') {
          failing.notify({ cancel: lease.ticket })
          await assert.rejects(failing.call('publication-info'))
        } else {
          await assert.rejects(failing.call(operation, lease))
        }
        assert.deepEqual(
          Array.from(state),
          [1, 1, 1, 1, 1, 1, 1],
          `${failAt}: all three rollbacks and closes ran`,
        )
        assertAuthorityReleased(authority)
        const checkpoint = main.pragma('wal_checkpoint(TRUNCATE)') as { busy: number }[]
        assert.equal(checkpoint[0]!.busy, 0, `${failAt}: no main read snapshot remains`)
        await assert.rejects(failing.call('publication-begin'))
        await assert.rejects(failing.call('publication-verify', lease))
        assert.equal(await failing.call('publication-abandon', lease), null)
        await failing.close()
        assert.deepEqual(
          Array.from(state),
          [1, 1, 1, 1, 1, 1, 1],
          'close skips handles already closed by poison',
        )
      } finally {
        main.close()
        try {
          await failing.close()
        } finally {
          await failing.worker.terminate()
        }
      }
    }
  })
})
