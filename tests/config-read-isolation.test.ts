import test from 'node:test'
import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Worker } from 'node:worker_threads'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import {
  ConfigReadConnection,
  configReadPaths,
  type PublicationInfo,
  type PublicationTicket,
  type PublicationVerifiedResult,
  type ConfigReadPath,
} from '../src/server/config-read.ts'
import { metadata, RecoveryAuthority, saveGate } from '../src/memory/recovery/ledger.ts'
import { PublicationGate } from '../src/server/publication-gate.ts'
import { WorkerRpc } from '../src/server/worker-rpc.ts'

let nextTicket = 0
function project(reader: ConfigReadConnection, path: ConfigReadPath) {
  const grant = reader.beginPublication(++nextTicket)
  return reader.verifyPublication({ ...grant, path })
}
const bodyText = (result: PublicationVerifiedResult) => new TextDecoder().decode(result.body)
async function publish(publisher: PublicationGate, result: PublicationVerifiedResult) {
  const response = new ServerResponse(new IncomingMessage(new Socket()))
  await publisher.publish(
    {
      result: {
        status: 200,
        headers: [['Content-Type', 'application/json']],
        body: result.body!,
        observedHead: result.observedHead,
      },
    },
    response,
    new AbortController().signal,
  )
  return response
}

async function fixture(
  run: (store: Store, database: string, root: string) => void | Promise<void>,
) {
  const root = mkdtempSync(join(tmpdir(), 'he-config-read-'))
  const database = join(root, 'data', 'live.sqlite')
  const store = new Store(database)
  try {
    await run(store, database, root)
  } finally {
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}
async function worker(database: string) {
  const rpc = new WorkerRpc(
    new Worker(new URL('./fixtures/config-read-worker.mjs', import.meta.url), {
      workerData: { database },
    }),
  )
  await rpc.ready
  const info = await rpc.call<PublicationInfo>('publication-info', null)
  const publisher = new PublicationGate(database, info, rpc)
  return {
    publisher,
    close: async () => {
      await publisher.close()
      await rpc.close()
    },
    async capture(path: ConfigReadPath): Promise<PublicationVerifiedResult> {
      const grant = await rpc.call<PublicationTicket>('publication-begin', null, { lease: true })
      try {
        return await rpc.call<PublicationVerifiedResult>('publication-verify', { ...grant, path })
      } finally {
        rpc.releaseResult(grant)
      }
    },
  }
}

test('strict config reader matches original defaults and initialized values without keys, writes or chmod', async () => {
  await fixture(async (store, database, root) => {
    const authorityFile = join(metadata(store.db)!.authority, 'authority.sqlite')
    chmodSync(authorityFile, 0o640)
    const reader = new ConfigReadConnection(database)
    try {
      for (const [path, expected] of [
        ['/api/settings', store.settings],
        ['/api/scenes', store.scenes()],
        ['/api/models', store.models],
      ] as const) {
        assert.deepEqual(JSON.parse(bodyText(project(reader, path))), expected)
      }
      store.set('settings', { ...store.settings, name: 'Current synthetic name' })
      store.saveScene({
        id: 'synthetic',
        name: 'Scene',
        kind: 'group',
        adapterId: 'offline',
        accountId: 'test',
        platformSceneId: 'synthetic',
        participationEnabled: false,
        learningEnabled: false,
        proactiveEnabled: false,
      })
      store.saveModels({
        providers: [
          {
            id: 'p',
            name: 'Provider',
            protocol: 'openai-chat',
            baseUrl: 'https://fixture.invalid',
            credentialEnv: '',
          },
        ],
        models: [],
        routes: [],
      })
      // Fixture ciphertext is intentionally unusable. A GET needs existence, never decryption.
      store.db
        .prepare('INSERT INTO secrets VALUES(?,?,?)')
        .run('model', 'p', 'SYNTHETIC_NOT_CIPHERTEXT')
      for (const [path, expected] of [
        ['/api/settings', store.settings],
        ['/api/scenes', store.scenes()],
        ['/api/models', store.models],
      ] as const) {
        const result = project(reader, path)
        assert.deepEqual(JSON.parse(bodyText(result)), expected)
        assert.doesNotMatch(bodyText(result), /SYNTHETIC_NOT_CIPHERTEXT/)
      }
      const connection = (reader as unknown as { db: Database.Database }).db
      assert.equal(connection.readonly, true)
      assert.equal(connection.inTransaction, false)
      assert.equal(connection.pragma('query_only', { simple: true }), 1)
      assert.equal(
        connection.pragma('busy_timeout', { simple: true }),
        store.db.pragma('busy_timeout', { simple: true }),
      )
      const authority = Reflect.get(reader, 'authority') as RecoveryAuthority
      assert.equal(authority.db.readonly, true)
      assert.equal(authority.db.inTransaction, false)
      assert.equal(authority.db.pragma('query_only', { simple: true }), 1)
      assert.equal(
        authority.db.pragma('busy_timeout', { simple: true }),
        store.db.pragma('busy_timeout', { simple: true }),
      )
      assert.equal((connection.prepare('SELECT total_changes() AS n').get() as { n: number }).n, 0)
      assert.equal(
        (authority.db.prepare('SELECT total_changes() AS n').get() as { n: number }).n,
        0,
      )
      assert.throws(() => connection.exec("INSERT INTO config VALUES('forbidden','1')"), /readonly/)
      assert.equal(statSync(authorityFile).mode & 0o777, 0o640)
      assert.equal(existsSync(join(root, 'data', '.secrets.key')), false)
    } finally {
      reader.close()
    }
  })
})

test('reader refuses missing, uninitialized and legacy stores without enrolling or changing files', () => {
  const root = mkdtempSync(join(tmpdir(), 'he-read-no-init-'))
  try {
    const missing = join(root, 'missing.sqlite')
    assert.throws(() => new ConfigReadConnection(missing))
    assert.equal(existsSync(missing), false)
    const empty = join(root, 'empty.sqlite')
    const emptyDb = new Database(empty)
    emptyDb.pragma('journal_mode = WAL')
    emptyDb.close()
    const emptyBytes = readFileSync(empty)
    assert.throws(() => new ConfigReadConnection(empty), /recovery_legacy_enrollment_required/)
    assert.deepEqual(readFileSync(empty), emptyBytes)
    const legacy = join(root, 'legacy.sqlite'),
      db = new Database(legacy)
    db.pragma('journal_mode = WAL')
    db.exec('CREATE TABLE retained(value TEXT)')
    db.close()
    const bytes = readFileSync(legacy)
    assert.throws(() => new ConfigReadConnection(legacy), /recovery_legacy_enrollment_required/)
    assert.deepEqual(readFileSync(legacy), bytes)
    assert.equal(existsSync(join(root, '.secrets.key')), false)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('actual IPC results are denied after external authority advances before publication', async () => {
  await fixture(async (store, database) => {
    const reader = await worker(database),
      publisher = reader.publisher
    const authority = new RecoveryAuthority(metadata(store.db)!.authority)
    try {
      const results: PublicationVerifiedResult[] = []
      for (const path of configReadPaths) results.push(await reader.capture(path))
      const head = authority.append(
        { kind: 'source', sceneId: 's', messageId: 'recalled' },
        authority.head(),
      )
      for (const result of results)
        await assert.rejects(publish(publisher, result), /worker_request_failed/)
      // Even after the writer reconciles, the older result must not become authorized again.
      saveGate(store.db, { ...metadata(store.db)!, head })
      for (const result of results)
        await assert.rejects(publish(publisher, result), /configuration_read_result_stale/)
      for (const path of configReadPaths) {
        const fresh = await reader.capture(path)
        assert.equal((await publish(publisher, fresh)).statusCode, 200)
      }
    } finally {
      await reader.close()
      authority.close()
    }
  })
})

test('parse failures close both snapshots and leave no read lock across an await', async () => {
  await fixture(async (store, database) => {
    const reader = new ConfigReadConnection(database)
    const authority = new RecoveryAuthority(metadata(store.db)!.authority)
    try {
      store.db.prepare("INSERT OR REPLACE INTO config VALUES('settings','not-json')").run()
      assert.throws(() => project(reader, '/api/settings'))
      assert.equal((Reflect.get(reader, 'db') as Database.Database).inTransaction, false)
      assert.equal((Reflect.get(reader, 'authority') as RecoveryAuthority).db.inTransaction, false)
      await Promise.resolve()
      store.db.prepare("DELETE FROM config WHERE key='settings'").run()
      assert.deepEqual(JSON.parse(bodyText(project(reader, '/api/settings'))), store.settings)
      assert.equal((reader as unknown as { db: Database.Database }).db.inTransaction, false)
      authority.db.pragma('busy_timeout = 0')
      authority.append(
        { kind: 'source', sceneId: 's', messageId: 'no-held-reader' },
        authority.head(),
      )
    } finally {
      reader.close()
      authority.close()
    }
  })
})
