import test from 'node:test'
import assert from 'node:assert/strict'
import { Worker } from 'node:worker_threads'
import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import Database from 'better-sqlite3'
import { WorkerRpc } from '../src/server/worker-rpc.ts'
import {
  createOwnerFailureHandler,
  createVerifierFailureHandler,
} from '../src/server/owner-supervision.ts'

const environment = (root: string) => ({
  ...process.env,
  NODE_ENV: 'test',
  HUMANENGINE_DB: join(root, 'data/live.sqlite'),
  HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY: join(root, 'authority'),
  HUMANENGINE_LOG_DIR: join(root, 'logs'),
  HUMANENGINE_ADMIN_TOKEN: 'synthetic-owner-supervision-token',
  HUMANENGINE_PLUGINS: '',
})

// Event ordering is controlled here; actual process exit is exercised below.
class OrderedWorker extends EventEmitter {
  postMessage(message: { id?: number; op?: string }) {
    if (message.op === 'close')
      queueMicrotask(() => this.emit('message', { id: message.id, result: null }))
  }
  async terminate() {
    this.emit('exit', 0)
    return 0
  }
}

test('owner fatal notification requires ready, ignores planned close, and runs once', async () => {
  const calls: number[] = []
  const owner = new OrderedWorker()
  const rpc = new WorkerRpc(owner as unknown as Worker, {
    onUnexpectedExit: createOwnerFailureHandler((code) => calls.push(code)),
  })
  owner.emit('message', { ready: true })
  await rpc.ready
  const pending = assert.rejects(rpc.call('fetch', {}), /unavailable/)
  owner.emit('error', new Error('SYNTHETIC_PRIVATE_ERROR_MUST_NOT_BE_LOGGED'))
  owner.emit('exit', 1)
  owner.emit('message', { ready: true })
  assert.deepEqual(calls, [1])
  await pending
  await assert.rejects(rpc.call('fetch', {}), /unavailable/)
  await rpc.close()
  assert.deepEqual(calls, [1])

  for (const beforeReady of [false, true]) {
    const planned = new OrderedWorker()
    const wrapper = new WorkerRpc(planned as unknown as Worker, {
      onUnexpectedExit: () => calls.push(99),
    })
    const ready = wrapper.ready
    if (!beforeReady) planned.emit('message', { ready: true })
    const close = wrapper.close()
    planned.emit('message', { ready: true })
    await close
    await ready
    assert.deepEqual(calls, [1])
  }

  const verifier = new OrderedWorker()
  const verifierRpc = new WorkerRpc(verifier as unknown as Worker, {
    onUnexpectedExit: createVerifierFailureHandler((code) => calls.push(code)),
  })
  verifier.emit('message', { ready: true })
  await verifierRpc.ready
  const invalid = assert.rejects(
    verifierRpc.call('publication-verify', {}),
    /worker_request_failed/,
  )
  verifier.emit('message', { id: 1, error: true })
  await invalid
  assert.deepEqual(calls, [1])
  verifier.emit('exit', 0)
  verifier.emit('error', new Error('SYNTHETIC_PRIVATE_VERIFIER_ERROR'))
  assert.deepEqual(calls, [1, 1])
  await verifierRpc.close()

  const startup = new OrderedWorker()
  const failed = new WorkerRpc(startup as unknown as Worker, {
    onUnexpectedExit: () => calls.push(99),
  })
  const rejection = assert.rejects(failed.ready, /engine_startup_failed/)
  startup.emit('message', { startupError: 'engine_startup_failed' })
  startup.emit('message', { ready: true })
  startup.emit('exit', 1)
  await rejection
  await failed.close()
  assert.deepEqual(calls, [1, 1])
})

test(
  'real owner captures background timer errors and unhandled rejection without raw error leakage',
  { timeout: 15000 },
  async () => {
    const root = mkdtempSync(join(tmpdir(), 'he-owner-errors-'))
    const ownerUrl = new URL('../src/server/owner-worker.ts', import.meta.url).href
    const runtimeUrl = new URL('../src/server/owner-runtime.ts', import.meta.url).href
    const worker = new Worker(
      `
    import { register } from 'tsx/esm/api'; register();
    const { parentPort } = await import('node:worker_threads');
    await import(${JSON.stringify(ownerUrl)});
    const { getEngine } = await import(${JSON.stringify(runtimeUrl)});
    const runtime = await getEngine();
    const scenes = runtime.store.scenes.bind(runtime.store);
    runtime.store.scenes = () => { throw new Error('recovery_authority_head_changed') };
    // Invoke the actual existing proactive callback with the same transient recovery failure
    // produced while a durable asynchronous media operation has not acknowledged its head.
    setTimeout(() => runtime.engine.proactiveTimer._onTimeout(), 0);
    setTimeout(() => {
      runtime.store.scenes = scenes;
      Promise.reject(new Error('SYNTHETIC_PRIVATE_REJECTION_TOKEN'));
      setTimeout(() => { throw new Error('SYNTHETIC_PRIVATE_TIMER_TOKEN') }, 0);
      setTimeout(() => parentPort.postMessage({ probeComplete: true }), 60);
    }, 20);
  `,
      { eval: true, env: environment(root), stderr: true },
    )
    let output = ''
    worker.stderr!.on('data', (chunk) => {
      output += chunk.toString()
    })
    let fatal = 0
    const rpc = new WorkerRpc(worker, {
      onUnexpectedExit: () => {
        fatal++
      },
    })
    const complete = new Promise<void>((resolve, reject) => {
      worker.on('message', (message) => {
        if (message.probeComplete) resolve()
      })
      worker.on('error', reject)
      worker.on('exit', () => reject(new Error('owner exited before captured error probe')))
    })
    try {
      await rpc.ready
      await complete
      const response = await rpc.call<{ status: number }>('fetch', {
        url: 'http://localhost/api/status',
        method: 'GET',
        headers: [['Authorization', 'Bearer synthetic-owner-supervision-token']],
        body: new Uint8Array(),
      })
      assert.equal(response.status, 200)
      assert.equal(fatal, 0)
      assert.match(output, /uncaughtException/)
      assert.match(output, /unhandledRejection/)
      assert(!output.includes('SYNTHETIC_PRIVATE_REJECTION_TOKEN'))
      assert(!output.includes('SYNTHETIC_PRIVATE_TIMER_TOKEN'))
    } finally {
      await rpc.close()
      rmSync(root, { recursive: true, force: true })
    }
  },
)

function child(root: string, mode: string) {
  const process = spawn(
    globalThis.process.execPath,
    [fileURLToPath(new URL('./fixtures/owner-supervision-host.mjs', import.meta.url)), mode],
    {
      cwd: globalThis.process.cwd(),
      env: environment(root),
      stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
    },
  )
  let output = ''
  process.stdout!.on('data', (chunk) => {
    output += chunk.toString()
  })
  process.stderr!.on('data', (chunk) => {
    output += chunk.toString()
  })
  const messages: unknown[] = []
  process.on('message', (message) => messages.push(message))
  const done = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>(
    (resolve, reject) => {
      process.once('error', reject)
      process.once('exit', (code, signal) => resolve({ code, signal }))
    },
  )
  return { process, messages, done, output: () => output }
}

test(
  'actual host exits nonzero on ready owner or verifier death, but not either startup rejection or normal close',
  { timeout: 20000 },
  async () => {
    for (const mode of [
      'host-exit',
      'verifier-exit',
      'startup-error',
      'verifier-startup-error',
      'planned-close',
    ]) {
      const root = mkdtempSync(join(tmpdir(), 'he-owner-supervisor-'))
      const running = child(root, mode)
      try {
        const result = await running.done
        const fatal = mode === 'host-exit' || mode === 'verifier-exit'
        assert.equal(result.code, fatal ? 1 : 0, running.output())
        assert.equal(result.signal, null)
        if (fatal) {
          assert.match(
            running.output(),
            mode === 'host-exit'
              ? /Engine owner exited unexpectedly/
              : /Publication verifier exited unexpectedly/,
          )
          assert.match(running.output(), /outcomes may be unknown.*No operations will be replayed/)
        } else {
          assert(!running.output().includes('exited unexpectedly'))
          assert.deepEqual(
            running.messages,
            mode.endsWith('startup-error') ? [{ status: 503, cached: true }] : [{ closed: true }],
          )
        }
      } finally {
        if (running.process.exitCode === null) running.process.kill('SIGKILL')
        await running.done
        rmSync(root, { recursive: true, force: true })
      }
    }
  },
)

test(
  'committed mutation followed by actual owner exit leaves unknown HTTP outcome and is never replayed',
  { timeout: 15000 },
  async () => {
    const root = mkdtempSync(join(tmpdir(), 'he-owner-committed-exit-'))
    const running = child(root, 'committed-exit')
    try {
      const port = await new Promise<number>((resolve, reject) => {
        running.process.once('message', (message) => resolve((message as { port: number }).port))
        running.process.once('exit', () => reject(new Error('host exited before listening')))
      })
      const outcome = await fetch(`http://127.0.0.1:${port}/`, {
        method: 'POST',
        signal: AbortSignal.timeout(5000),
      })
        .then(async (response) => ({ status: response.status, body: await response.text() }))
        .catch(() => ({ status: 0, body: 'connection closed: outcome unknown' }))
      assert([0, 503].includes(outcome.status), JSON.stringify(outcome))
      assert.match(outcome.body, /unknown/i)
      assert.equal((await running.done).code, 1)
      const db = new Database(environment(root).HUMANENGINE_DB, { readonly: true })
      try {
        const row = db
          .prepare("SELECT value FROM config WHERE key='synthetic-exit-write-count'")
          .get() as { value: string }
        assert.equal(JSON.parse(row.value), 1)
      } finally {
        db.close()
      }
      assert.match(running.output(), /No operations will be replayed/)
    } finally {
      if (running.process.exitCode === null) running.process.kill('SIGKILL')
      await running.done
      rmSync(root, { recursive: true, force: true })
    }
  },
)
