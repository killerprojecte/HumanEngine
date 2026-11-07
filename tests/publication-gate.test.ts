import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { spawn, type ChildProcess } from 'node:child_process'
import { setTimeout as delay } from 'node:timers/promises'
import Database from 'better-sqlite3'
import { Store } from '../src/store.ts'
import { metadata, saveGate } from '../src/memory/recovery/ledger.ts'
import {
  ConfigReadConnection,
  type PublicationTicket,
  type PublicationVerifiedResult,
  type PublicationVerifyRequest,
} from '../src/server/config-read.ts'
import { PublicationGate, type PublicationPlan } from '../src/server/publication-gate.ts'
import type { WorkerRpc, Operation } from '../src/server/worker-rpc.ts'

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
async function interruptible<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise
  if (signal.aborted) throw new Error('request_aborted')
  const interrupted = deferred<never>()
  const cancel = () => interrupted.reject(new Error('request_aborted'))
  signal.addEventListener('abort', cancel, { once: true })
  try {
    return await Promise.race([promise, interrupted.promise])
  } finally {
    signal.removeEventListener('abort', cancel)
  }
}
/** Restricted scheduling proxy; all SQLite verification and projection use the real actor. */
class ActorRpc {
  private next = 0
  private readonly leases = new WeakMap<object, number>()
  readonly calls: Operation[] = []
  releases = 0
  unavailable = false
  afterBegin?: (grant: PublicationTicket) => void | Promise<void>
  beforeVerify?: (request: PublicationVerifyRequest) => void | Promise<void>
  afterVerify?: (value: PublicationVerifiedResult) => void | Promise<void>
  afterAbandon?: () => void | Promise<void>
  constructor(readonly actor: ConfigReadConnection) {}
  async call<T>(
    op: Operation,
    payload: unknown,
    options: { signal?: AbortSignal } = {},
  ): Promise<T> {
    this.calls.push(op)
    if (this.unavailable) throw new Error('worker_unavailable')
    if (options.signal?.aborted) throw new Error('request_aborted')
    if (op === 'publication-begin') {
      const grant = this.actor.beginPublication(++this.next)
      this.leases.set(grant, grant.ticket)
      try {
        await interruptible(Promise.resolve(this.afterBegin?.(grant)), options.signal)
        return grant as T
      } catch (error) {
        this.actor.releasePublicationTicket(this.leases.get(grant)!)
        throw error
      }
    }
    if (op === 'publication-verify') {
      const request = payload as PublicationVerifyRequest
      await interruptible(Promise.resolve(this.beforeVerify?.(request)), options.signal)
      const result = this.actor.verifyPublication(request)
      await interruptible(Promise.resolve(this.afterVerify?.(result)), options.signal)
      return result as T
    }
    if (op === 'publication-abandon') {
      this.actor.abandonPublication((payload as PublicationTicket).ticket)
      await this.afterAbandon?.()
      return undefined as T
    }
    throw new Error('test_operation_unexpected')
  }
  releaseResult(grant: object) {
    ++this.releases
    const ticket = this.leases.get(grant)
    this.leases.delete(grant)
    if (!this.unavailable && ticket !== undefined) this.actor.releasePublicationTicket(ticket)
  }
}
function target(onEnd?: () => void) {
  const response = new ServerResponse(new IncomingMessage(new Socket()))
  let body: Uint8Array | undefined
  const end = response.end.bind(response)
  response.end = ((chunk: Uint8Array) => {
    body = chunk
    onEnd?.()
    return end(chunk)
  }) as typeof response.end
  return { response, body: () => body }
}
function configPlan(): PublicationPlan {
  return {
    path: '/api/settings',
    result: {
      status: 200,
      headers: [['Content-Type', 'application/json']],
      body: new Uint8Array(),
    },
  }
}
const connection = (gate: PublicationGate, name: 'main' | 'authority') =>
  Reflect.get(gate, name) as Database.Database
function idle(gate: PublicationGate) {
  assert.equal(connection(gate, 'main').inTransaction, false)
  assert.equal(connection(gate, 'authority').inTransaction, false)
}
async function fixture(
  run: (f: {
    store: Store
    actor: ConfigReadConnection
    rpc: ActorRpc
    gate: PublicationGate
    writer: Database.Database
    database: string
    authorityFile: string
  }) => Promise<void>,
) {
  const root = mkdtempSync(join(tmpdir(), 'he-publication-gate-'))
  const database = join(root, 'data', 'live.sqlite'),
    store = new Store(database)
  const actor = new ConfigReadConnection(database),
    rpc = new ActorRpc(actor)
  const authorityFile = join(metadata(store.db)!.authority, 'authority.sqlite')
  const writer = new Database(authorityFile, { timeout: 0 })
  let gate: PublicationGate | undefined
  try {
    gate = new PublicationGate(database, actor.publicationInfo(), rpc as unknown as WorkerRpc)
    await run({ store, actor, rpc, gate, writer, database, authorityFile })
  } finally {
    await gate?.close()
    if (writer.inTransaction) writer.exec('ROLLBACK')
    writer.close()
    actor.close()
    await store.closeLogs()
    store.close()
    rmSync(root, { recursive: true, force: true })
  }
}
const liveSignal = () => new AbortController().signal
function pendingWriter(writer: Database.Database) {
  writer.exec('BEGIN IMMEDIATE; UPDATE main.identity SET format=format')
  assert.throws(() => writer.exec('COMMIT'), { code: 'SQLITE_BUSY' })
  assert.equal(writer.inTransaction, true)
}

test('host opens real authority file readonly without native waits; actor retains original allowance', async () => {
  await fixture(async ({ actor, gate }) => {
    assert.equal(actor.publicationInfo().mainTimeout, 5000)
    assert.equal(actor.publicationInfo().authorityTimeout, 5000)
    for (const name of ['main', 'authority'] as const) {
      const db = connection(gate, name)
      assert.equal(db.readonly, true)
      assert.equal(db.pragma('busy_timeout', { simple: true }), 0)
      assert.equal(db.pragma('query_only', { simple: true }), 1)
      assert.equal((db.prepare('SELECT total_changes() n').get() as { n: number }).n, 0)
    }
    const output = target()
    await gate.publish(configPlan(), output.response, liveSignal())
    assert.equal(output.response.statusCode, 200)
    assert.ok(output.body()!.byteLength)
    idle(gate)
  })
})
test('projection is deferred to actor snapshot and enqueue retains host authority lock', async () => {
  await fixture(async ({ store, gate, writer, rpc }) => {
    const plan = configPlan()
    store.set('settings', { ...store.settings, name: 'new before publication' })
    let attempted = false
    const output = target(() => {
      attempted = true
      assert.equal(connection(gate, 'main').inTransaction, true)
      assert.equal(connection(gate, 'authority').inTransaction, true)
      pendingWriter(writer)
      writer.exec('ROLLBACK')
    })
    await gate.publish(plan, output.response, liveSignal())
    assert.equal(attempted, true)
    assert.equal(JSON.parse(new TextDecoder().decode(output.body())).name, 'new before publication')
    assert.equal(rpc.calls.filter((op) => op === 'publication-verify').length, 1)
    writer.exec('BEGIN IMMEDIATE; UPDATE main.identity SET format=format; COMMIT')
    idle(gate)
  })
})
test('same-head historical corruption is rejected after actor cache warmup', async () => {
  await fixture(async ({ store, gate, writer, rpc }) => {
    store.recall('synthetic', 'one')
    await gate.publish(configPlan(), target().response, liveSignal())
    const head = metadata(store.db)!.head
    writer
      .prepare('UPDATE main.revocations SET payload=? WHERE sequence=1')
      .run('{"kind":"source","sceneId":"synthetic","messageId":"tampered"}')
    const output = target()
    await assert.rejects(
      gate.publish(configPlan(), output.response, liveSignal()),
      /recovery_chain_invalid/,
    )
    assert.deepEqual(metadata(store.db)!.head, head)
    assert.equal(output.response.headersSent, false)
    assert.equal(rpc.calls.filter((op) => op === 'publication-verify').length, 1)
    idle(gate)
  })
})
test('host rejects authority WAL drift inside final pinned snapshot', async () => {
  await fixture(async ({ gate, writer }) => {
    assert.equal(writer.pragma('journal_mode=WAL', { simple: true }), 'wal')
    const output = target()
    await assert.rejects(
      gate.publish(configPlan(), output.response, liveSignal()),
      /journal_unsupported/,
    )
    assert.equal(output.response.headersSent, false)
    idle(gate)
  })
})
test('host rejects quarantine, capability and authority path rebinding', async () => {
  await fixture(async ({ store, gate }) => {
    const original = metadata(store.db)!
    for (const change of [
      { state: 'quarantine' },
      { minimumReader: 999999 },
      { authority: join(original.authority, 'different') },
    ]) {
      saveGate(store.db, { ...original, ...change })
      const output = target()
      await assert.rejects(
        gate.publish(configPlan(), output.response, liveSignal()),
        /recovery_gate_invalid|recovery_authority_not_bound/,
      )
      assert.equal(output.response.headersSent, false)
      idle(gate)
    }
    saveGate(store.db, original)
    await gate.publish(configPlan(), target().response, liveSignal())
  })
})
test('host acquisition BUSY releases snapshots and awaits actor abandonment before retry', async () => {
  await fixture(async ({ gate, writer, rpc }) => {
    let begun = 0
    const abandoned = deferred(),
      allowAck = deferred()
    rpc.afterBegin = () => {
      ++begun
      // The actor reservation prevents the former PENDING-writer handoff race.
      assert.throws(() => writer.exec('BEGIN IMMEDIATE'), { code: 'SQLITE_BUSY' })
      assert.equal(writer.inTransaction, false)
    }
    const main = connection(gate, 'main'),
      exec = main.exec.bind(main)
    main.exec = ((sql: string) => {
      // Preserve the host's exceptional lock-contention fallback coverage without
      // manufacturing a writer phase now excluded by the actor's reservation.
      if (sql === 'BEGIN' && begun === 1)
        throw Object.assign(new Error('synthetic host acquisition BUSY'), { code: 'SQLITE_BUSY' })
      return exec(sql)
    }) as typeof main.exec
    rpc.afterAbandon = async () => {
      idle(gate)
      writer.exec('BEGIN IMMEDIATE; COMMIT')
      abandoned.resolve()
      await allowAck.promise
    }
    const output = target(),
      publish = gate.publish(configPlan(), output.response, liveSignal())
    await abandoned.promise
    await delay(15)
    assert.equal(begun, 1, 'no retry before abandonment acknowledgment')
    assert.equal(output.response.headersSent, false)
    allowAck.resolve()
    await publish
    assert.equal(begun, 2)
    assert.equal(output.response.writableEnded, true)
    assert.equal(rpc.releases, 2)
    idle(gate)
  })
})
test('exceptional host acquisition BUSY retains the original five-second sleep allowance', async () => {
  await fixture(async ({ actor, gate, writer, rpc }) => {
    let attempts = 0,
      controlTicks = 0
    rpc.afterBegin = () => {
      ++attempts
      assert.throws(() => writer.exec('BEGIN IMMEDIATE'), { code: 'SQLITE_BUSY' })
    }
    const main = connection(gate, 'main'),
      exec = main.exec.bind(main)
    main.exec = ((sql: string) => {
      if (sql === 'BEGIN')
        throw Object.assign(new Error('synthetic host acquisition BUSY'), { code: 'SQLITE_BUSY' })
      return exec(sql)
    }) as typeof main.exec
    rpc.afterAbandon = () => {
      idle(gate)
      writer.exec('BEGIN IMMEDIATE; COMMIT')
    }
    const output = target(),
      timer = setInterval(() => {
        ++controlTicks
      }, 20),
      start = performance.now()
    try {
      await assert.rejects(
        gate.publish(configPlan(), output.response, liveSignal()),
        /publication_database_busy/,
      )
    } finally {
      clearInterval(timer)
    }
    assert.equal(actor.publicationInfo().authorityTimeout, 5000)
    assert.ok(performance.now() - start >= 4900, 'existing 5000ms allowance was shortened')
    assert.ok(attempts > 12)
    assert.ok(controlTicks > 0, 'HTTP event loop must yield during lock waiting')
    assert.equal(output.response.headersSent, false)
    assert.equal(writer.inTransaction, false)
    idle(gate)
  })
})
test('whole asynchronous handshakes are FIFO without previous 32-request admission cap', async () => {
  await fixture(async ({ gate, rpc }) => {
    const entered = deferred(),
      allow = deferred(),
      order: number[] = []
    let verifying = 0
    rpc.beforeVerify = async () => {
      if (++verifying === 1) {
        entered.resolve()
        await allow.promise
      }
    }
    const outputs = Array.from({ length: 40 }, (_, n) =>
      target(() => {
        order.push(n)
      }),
    )
    const jobs = outputs.map((output) => gate.publish(configPlan(), output.response, liveSignal()))
    await entered.promise
    assert.equal(rpc.calls.filter((op) => op === 'publication-begin').length, 1)
    assert.equal(connection(gate, 'authority').inTransaction, true)
    allow.resolve()
    await Promise.all(jobs)
    assert.deepEqual(
      order,
      outputs.map((_, n) => n),
    )
    assert.equal(rpc.releases, 40)
    idle(gate)
  })
})
test('cancellation awaiting verification releases snapshots and never publishes', async () => {
  await fixture(async ({ gate, rpc, writer }) => {
    const controller = new AbortController(),
      entered = deferred(),
      never = deferred()
    rpc.beforeVerify = () => {
      entered.resolve()
      return never.promise
    }
    const output = target(),
      rejected = assert.rejects(
        gate.publish(configPlan(), output.response, controller.signal),
        /request_aborted/,
      )
    await entered.promise
    controller.abort()
    await rejected
    assert.equal(output.response.headersSent, false)
    rpc.beforeVerify = undefined
    // Public cancellation rejects promptly; its internal slot still owns cleanup.
    await gate.publish(configPlan(), target().response, liveSignal())
    assert.ok(rpc.calls.includes('publication-abandon'))
    writer.exec('BEGIN IMMEDIATE; UPDATE main.identity SET format=format; COMMIT')
    idle(gate)
  })
})
test('queued cancellation rejects immediately without unlocking or overtaking the active job', async () => {
  await fixture(async ({ gate, rpc }) => {
    const entered = deferred(),
      allow = deferred(),
      canceled = new AbortController()
    let verifies = 0
    rpc.beforeVerify = async () => {
      if (++verifies === 1) {
        entered.resolve()
        await allow.promise
      }
    }
    const first = target(),
      second = target(),
      third = target()
    const firstJob = gate.publish(configPlan(), first.response, liveSignal())
    await entered.promise
    const canceledJob = assert.rejects(
      gate.publish(configPlan(), second.response, canceled.signal),
      /request_aborted/,
    )
    const thirdJob = gate.publish(configPlan(), third.response, liveSignal())
    canceled.abort()
    await canceledJob
    assert.equal(first.response.headersSent, false, 'predecessor remains blocked')
    assert.equal(connection(gate, 'authority').inTransaction, true)
    assert.equal(rpc.calls.includes('publication-abandon'), false)
    assert.equal(rpc.calls.filter((op) => op === 'publication-begin').length, 1)
    allow.resolve()
    await Promise.all([firstJob, thirdJob])
    assert.equal(second.response.headersSent, false)
    assert.equal(rpc.calls.filter((op) => op === 'publication-begin').length, 2)
    idle(gate)
  })
})
test('malformed grant cannot leak actor snapshots or authorize a later response', async () => {
  await fixture(async ({ gate, rpc }) => {
    rpc.afterBegin = (grant) => {
      grant.ticket = Number.NaN
    }
    const output = target()
    await assert.rejects(
      gate.publish(configPlan(), output.response, liveSignal()),
      /ticket_invalid/,
    )
    assert.equal(output.response.headersSent, false)
    idle(gate)
    rpc.afterBegin = undefined
    await gate.publish(configPlan(), target().response, liveSignal())
    idle(gate)
  })
})
test('shutdown aborts active and queued requests, closes handles and cannot restart', async () => {
  await fixture(async ({ gate, rpc }) => {
    const entered = deferred(),
      never = deferred()
    rpc.beforeVerify = () => {
      entered.resolve()
      return never.promise
    }
    const first = target(),
      second = target()
    const p = assert.rejects(
      gate.publish(configPlan(), first.response, liveSignal()),
      /request_aborted/,
    )
    const q = assert.rejects(
      gate.publish(configPlan(), second.response, liveSignal()),
      /request_aborted/,
    )
    await entered.promise
    await gate.close()
    await Promise.all([p, q])
    assert.equal(first.response.headersSent, false)
    assert.equal(second.response.headersSent, false)
    assert.equal(rpc.calls.filter((op) => op === 'publication-begin').length, 1)
    assert.equal(connection(gate, 'authority').open, false)
    assert.equal(connection(gate, 'main').open, false)
    await assert.rejects(
      gate.publish(configPlan(), target().response, liveSignal()),
      /publication_closed/,
    )
  })
})
test('malformed and aborted verified replies never reach HTTP', async () => {
  await fixture(async ({ gate, rpc }) => {
    for (const corrupt of [
      (reply: PublicationVerifiedResult) => {
        reply.observedHead = null as never
      },
      (reply: PublicationVerifiedResult) => {
        reply.body = 'invalid' as never
      },
      (reply: PublicationVerifiedResult) => {
        reply.body = new Uint8Array(new SharedArrayBuffer(4)) as never
      },
    ]) {
      rpc.afterVerify = corrupt
      const output = target()
      await assert.rejects(gate.publish(configPlan(), output.response, liveSignal()))
      assert.equal(output.response.headersSent, false)
      idle(gate)
    }
    const controller = new AbortController()
    rpc.afterVerify = () => controller.abort()
    const output = target()
    await assert.rejects(
      gate.publish(configPlan(), output.response, controller.signal),
      /request_aborted/,
    )
    assert.equal(output.response.headersSent, false)
    idle(gate)
  })
})
test('enqueue BUSY-shaped failure is never retried after outcome becomes uncertain', async () => {
  await fixture(async ({ gate, rpc }) => {
    const output = target(() => {
      throw Object.assign(new Error('synthetic enqueue failure'), { code: 'SQLITE_BUSY' })
    })
    await assert.rejects(
      gate.publish(configPlan(), output.response, liveSignal()),
      /synthetic enqueue failure/,
    )
    assert.equal(rpc.calls.filter((op) => op === 'publication-begin').length, 1)
    idle(gate)
  })
})
function childMessage(child: ChildProcess): Promise<Record<string, unknown>> {
  return new Promise((resolve, reject) => {
    child.once('message', (message) => resolve(message as Record<string, unknown>))
    child.once('error', reject)
  })
}
async function childCall(child: ChildProcess, message: string) {
  const response = childMessage(child)
  child.send!(message)
  return response
}
test('actor closes after verification; independent process cannot commit before host releases', async () => {
  await fixture(async ({ actor, gate, rpc, authorityFile }) => {
    const source = `import Database from 'better-sqlite3';
      const db=new Database(process.env.TEST_AUTHORITY_FILE,{timeout:0});
      process.on('message',m=>{
        if(m==='prepare'){db.exec('BEGIN IMMEDIATE;UPDATE identity SET format=format');process.send({prepared:true})}
        if(m==='commit'){try{db.exec('COMMIT');process.send({committed:true})}catch(e){process.send({error:e.code})}}
        if(m==='close'){if(db.inTransaction)db.exec('ROLLBACK');db.close();process.exit(0)}
      });process.send({ready:true});`
    const child = spawn(process.execPath, ['--input-type=module', '-e', source], {
      cwd: process.cwd(),
      env: { ...process.env, TEST_AUTHORITY_FILE: authorityFile },
      stdio: ['ignore', 'ignore', 'inherit', 'ipc'],
    })
    try {
      assert.equal((await childMessage(child)).ready, true)
      rpc.afterVerify = async () => {
        actor.close()
        rpc.unavailable = true
        assert.equal((await childCall(child, 'prepare')).prepared, true)
        assert.deepEqual(await childCall(child, 'commit'), { error: 'SQLITE_BUSY' })
      }
      const output = target()
      await gate.publish(configPlan(), output.response, liveSignal())
      assert.equal(output.response.writableEnded, true)
      assert.equal(rpc.calls.includes('publication-abandon'), false)
      assert.equal((await childCall(child, 'commit')).committed, true)
      idle(gate)
    } finally {
      const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()))
      child.send?.('close')
      await exited
    }
  })
})

test('host rollback failure still abandons actor and permanently poisons all queued publications', async () => {
  await fixture(async ({ gate, rpc, writer }) => {
    const entered = deferred(),
      allowFailure = deferred()
    rpc.beforeVerify = async () => {
      entered.resolve()
      await allowFailure.promise
      throw new Error('synthetic verification failure')
    }
    const first = target(),
      queued = target()
    const firstRejected = assert.rejects(gate.publish(configPlan(), first.response, liveSignal()))
    await entered.promise
    const queuedRejected = assert.rejects(
      gate.publish(configPlan(), queued.response, liveSignal()),
      /request_aborted/,
    )
    const authority = connection(gate, 'authority'),
      main = connection(gate, 'main')
    const authorityExec = authority.exec.bind(authority),
      mainExec = main.exec.bind(main)
    let failed = false,
      mainRollback = false
    authority.exec = ((sql: string) => {
      if (sql === 'ROLLBACK') {
        failed = true
        throw Object.assign(new Error('synthetic rollback I/O failure'), { code: 'SQLITE_IOERR' })
      }
      return authorityExec(sql)
    }) as typeof authority.exec
    main.exec = ((sql: string) => {
      if (sql === 'ROLLBACK') mainRollback = true
      return mainExec(sql)
    }) as typeof main.exec
    allowFailure.resolve()
    await Promise.all([firstRejected, queuedRejected])
    // Public abort rejects promptly; close joins the active slot's nested cleanup.
    await gate.close()
    assert.equal(failed, true)
    assert.equal(mainRollback, true)
    assert.equal(authority.open, false)
    assert.equal(main.open, false)
    assert.equal(rpc.calls.includes('publication-abandon'), true)
    assert.equal(rpc.releases, 1)
    assert.equal(rpc.calls.filter((op) => op === 'publication-begin').length, 1)
    assert.equal(first.response.headersSent, false)
    assert.equal(queued.response.headersSent, false)
    writer.exec('BEGIN IMMEDIATE; UPDATE main.identity SET format=format; COMMIT')
    await assert.rejects(
      gate.publish(configPlan(), target().response, liveSignal()),
      /publication_closed/,
    )
  })
})

test('actor death after readiness but before verification never publishes a response', async () => {
  await fixture(async ({ actor, gate, rpc, writer }) => {
    rpc.afterBegin = () => {
      actor.close()
      rpc.unavailable = true
    }
    const output = target()
    await assert.rejects(
      gate.publish(configPlan(), output.response, liveSignal()),
      /worker_unavailable/,
    )
    assert.equal(output.response.headersSent, false)
    assert.equal(rpc.releases, 1)
    idle(gate)
    writer.exec('BEGIN IMMEDIATE; UPDATE main.identity SET format=format; COMMIT')
  })
})
