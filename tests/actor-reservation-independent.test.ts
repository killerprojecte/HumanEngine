import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync, chmodSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Worker } from 'node:worker_threads'
import { spawn, type ChildProcess } from 'node:child_process'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata } from '../src/memory/recovery/ledger.ts'
import { ConfigReadConnection, type PublicationInfo } from '../src/server/config-read.ts'
import { PublicationGate, type PublicationPlan } from '../src/server/publication-gate.ts'
import { WorkerRpc, type Operation } from '../src/server/worker-rpc.ts'

async function fixture(run: (store: Store, file: string) => Promise<void>) {
  const root = mkdtempSync(join(tmpdir(), 'he-reservation-review-'))
  const store = new Store(join(root, 'data', 'live.sqlite'))
  const file = join(metadata(store.db)!.authority, 'authority.sqlite')
  try {
    await run(store, file)
  } finally {
    chmodSync(file, 0o600)
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}
function output() {
  return new ServerResponse(new IncomingMessage(new Socket()))
}
function plan(): PublicationPlan {
  return { path: '/api/settings', result: { status: 200, headers: [], body: new Uint8Array() } }
}
async function actor(database: string, beforeImport = '') {
  const source = new URL('../src/server/config-worker.ts', import.meta.url)
  const worker = new Worker(
    `import {register} from 'tsx/esm/api';register();${beforeImport};await import(${JSON.stringify(source.href)})`,
    { eval: true, workerData: { database } },
  )
  const rpc = new WorkerRpc(worker)
  await rpc.ready
  return rpc
}
function childMessage(child: ChildProcess): Promise<Record<string, unknown>> {
  return new Promise((resolve, reject) => {
    child.once('message', (value) => resolve(value as Record<string, unknown>))
    child.once('error', reject)
  })
}
async function childCall(child: ChildProcess, message: string) {
  const result = childMessage(child)
  child.send!(message)
  return result
}

test('native readonly fallback fails the reservation proof before any lease can exist', async () => {
  await fixture(async (store, file) => {
    const closed: boolean[] = []
    const original = Database.prototype.close
    Database.prototype.close = function () {
      closed.push(this.readonly)
      return original.call(this)
    }
    chmodSync(file, 0o444)
    try {
      assert.throws(() => new ConfigReadConnection(store.db.name), { code: 'SQLITE_READONLY' })
      assert.equal(closed.length, 3, 'failed native proof closes reservation and both readers')
      assert.equal(closed.filter(Boolean).length, 2)
      assert.equal(closed.filter((readonly) => !readonly).length, 1)
    } finally {
      Database.prototype.close = original
      chmodSync(file, 0o600)
    }
  })
})

test('constructor proof rejects a view, virtual table or replaced identity schema before its no-row DML', async () => {
  for (const replacement of [
    'CREATE VIEW identity AS SELECT id,format FROM saved_identity; CREATE TRIGGER inert INSTEAD OF UPDATE ON identity BEGIN SELECT 1; END',
    'CREATE VIRTUAL TABLE identity USING fts5(id,format)',
    'CREATE TABLE identity (id TEXT PRIMARY KEY ON CONFLICT REPLACE, format INTEGER NOT NULL)',
  ]) {
    await fixture(async (store, file) => {
      const other = new Database(file, { timeout: 0 })
      const original = Database.prototype.exec
      let changed = false
      Database.prototype.exec = function (sql) {
        if (!changed && this.name === file && !this.readonly && sql === 'BEGIN IMMEDIATE') {
          changed = true
          original.call(other, 'ALTER TABLE main.identity RENAME TO saved_identity; ' + replacement)
        }
        return original.call(this, sql)
      }
      try {
        assert.throws(() => new ConfigReadConnection(store.db.name), /recovery_schema_unsupported/)
        assert(changed)
        other.exec('BEGIN IMMEDIATE; ROLLBACK')
      } finally {
        Database.prototype.exec = original
        other.close()
      }
    })
  }
})

test('actual actor death after reservation but before grant never publishes and releases all actor locks', async () => {
  await fixture(async (store, file) => {
    const config = new URL('../src/server/config-read.ts', import.meta.url)
    const rpc = await actor(
      store.db.name,
      `const {ConfigReadConnection}=await import(${JSON.stringify(config.href)}); const begin=ConfigReadConnection.prototype.beginPublication; ConfigReadConnection.prototype.beginPublication=function(ticket){begin.call(this,ticket);process.exit(23)}`,
    )
    const gate = new PublicationGate(
      store.db.name,
      await rpc.call<PublicationInfo>('publication-info', null),
      rpc,
    )
    const response = output()
    try {
      await assert.rejects(
        gate.publish(plan(), response, new AbortController().signal),
        /worker_unavailable/,
      )
      assert.equal(response.headersSent, false)
      const writer = new Database(file, { timeout: 0 })
      try {
        writer.exec('BEGIN IMMEDIATE; ROLLBACK')
      } finally {
        writer.close()
      }
    } finally {
      await gate.close()
      await rpc.close()
    }
  })
})

test('actual actor death after grant and before verification never turns a lease into permission', async () => {
  await fixture(async (store) => {
    const rpc = await actor(store.db.name)
    const info = await rpc.call<PublicationInfo>('publication-info', null)
    const proxy = {
      async call<T>(
        op: Operation,
        payload: unknown,
        options?: { signal?: AbortSignal; lease?: boolean },
      ) {
        const value = await rpc.call<T>(op, payload, options)
        if (op === 'publication-begin') await rpc.worker.terminate()
        return value
      },
      releaseResult: (value: object) => rpc.releaseResult(value),
    }
    const gate = new PublicationGate(store.db.name, info, proxy as unknown as WorkerRpc)
    const response = output()
    try {
      await assert.rejects(
        gate.publish(plan(), response, new AbortController().signal),
        /worker_unavailable/,
      )
      assert.equal(response.headersSent, false)
    } finally {
      await gate.close()
      await rpc.close()
    }
  })
})

test('actual actor death after verified reply leaves host shared lock blocking an independent process commit', async () => {
  await fixture(async (store, file) => {
    const rpc = await actor(store.db.name)
    const info = await rpc.call<PublicationInfo>('publication-info', null)
    const child = spawn(
      process.execPath,
      [
        '--input-type=module',
        '-e',
        `
      import Database from 'better-sqlite3';
      const db=new Database(process.env.TEST_AUTHORITY_FILE,{timeout:0});
      process.on('message',m=>{
        if(m==='reserve'){let error;try{db.exec('BEGIN IMMEDIATE')}catch(e){error=e.code}const inTransaction=db.inTransaction;if(inTransaction)db.exec('ROLLBACK');process.send({error,inTransaction,canRead:!!db.prepare('SELECT id FROM main.identity').get()})}
        if(m==='prepare'){db.exec('BEGIN IMMEDIATE;UPDATE main.identity SET format=format');process.send({prepared:true})}
        if(m==='commit'){try{db.exec('COMMIT');process.send({committed:true})}catch(e){process.send({error:e.code})}}
        if(m==='close'){if(db.inTransaction)db.exec('ROLLBACK');db.close();process.exit(0)}
      });process.send({ready:true});
    `,
      ],
      {
        cwd: process.cwd(),
        env: { ...process.env, TEST_AUTHORITY_FILE: file },
        stdio: ['ignore', 'ignore', 'inherit', 'ipc'],
      },
    )
    let gate: PublicationGate | undefined
    try {
      assert.equal((await childMessage(child)).ready, true)
      const proxy = {
        async call<T>(
          op: Operation,
          payload: unknown,
          options?: { signal?: AbortSignal; lease?: boolean },
        ) {
          const value = await rpc.call<T>(op, payload, options)
          if (op === 'publication-begin') {
            assert.deepEqual(await childCall(child, 'reserve'), {
              error: 'SQLITE_BUSY',
              inTransaction: false,
              canRead: true,
            })
          }
          if (op === 'publication-verify') {
            await rpc.worker.terminate()
            assert.equal((await childCall(child, 'prepare')).prepared, true)
            assert.deepEqual(await childCall(child, 'commit'), { error: 'SQLITE_BUSY' })
          }
          return value
        },
        releaseResult: (value: object) => rpc.releaseResult(value),
      }
      gate = new PublicationGate(store.db.name, info, proxy as unknown as WorkerRpc)
      const response = output()
      await gate.publish(plan(), response, new AbortController().signal)
      assert.equal(response.writableEnded, true)
      assert.equal((await childCall(child, 'commit')).committed, true)
    } finally {
      await gate?.close()
      await rpc.close()
      const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()))
      child.send('close')
      await exited
    }
  })
})
