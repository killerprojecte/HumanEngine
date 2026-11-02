import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { spawn } from 'node:child_process'
import { pathToFileURL } from 'node:url'
import { join } from 'node:path'
import { LogChannel, acquireLogChannel } from '../src/diagnostics/log-writer.ts'

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-log-writer-'))
  return {
    root,
    async close() {
      await rm(root, { recursive: true, force: true })
    },
  }
}

test('file worker preserves whole FIFO records across ordered rotations and durable close', async () => {
  const f = await fixture(),
    channel = new LogChannel({ directory: f.root, rotateBytes: 256 })
  try {
    for (let id = 0; id < 5; id++)
      assert(channel.enqueue(JSON.stringify({ id, text: 'x'.repeat(180) }) + '\n'))
    const result = await channel.flush(5000, true)
    assert.equal(result.complete, true)
    assert.equal(result.durable, true)
    assert.equal(result.flushedAcceptedRecords, 5)
    const lines = (
      await Promise.all(
        ['runtime.jsonl.2', 'runtime.jsonl.1', 'runtime.jsonl'].map((name) =>
          readFile(join(f.root, name), 'utf8'),
        ),
      )
    )
      .join('')
      .trim()
      .split('\n')
    assert.deepEqual(
      lines.map((line) => JSON.parse(line).id),
      [0, 1, 2, 3, 4],
    )
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('admission is nonblocking, byte/record bounded, with explicit whole-record rejection and priority reserve', async () => {
  const f = await fixture(),
    channel = new LogChannel({ directory: f.root, maxBytes: 1024, maxRecords: 8 })
  const wait = Atomics.wait
  try {
    Atomics.wait = (() => {
      throw new Error('admission_must_not_wait')
    }) as typeof Atomics.wait
    for (let id = 0; id < 7; id++) assert(channel.enqueue(JSON.stringify({ id }) + '\n'))
    assert.equal(channel.enqueue('{"id":7}\n'), false)
    assert.equal(channel.enqueue('{"id":8}\n', true), true)
    assert.equal(channel.enqueue('x'.repeat(1025), true), false)
    assert(channel.status.pendingBytes <= 1024)
    assert(channel.status.pendingRecords <= 8)
    assert.equal(channel.status.rejectedRecords, 2)
    assert.equal(channel.status.rejectedBytes, Buffer.byteLength('{"id":7}\n') + 1025)
  } finally {
    Atomics.wait = wait
  }
  try {
    const result = await channel.flush(5000, true)
    assert.equal(result.complete, false, 'prior rejection remains visible despite successful drain')
    assert.equal(result.status.writtenRecords, 8)
    assert.equal(result.status.pendingRecords, 0)
    assert.equal(
      (await readFile(join(f.root, 'runtime.jsonl'), 'utf8')).trim().split('\n').length,
      8,
    )
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('a second flush covers records accepted after an earlier fsync barrier', async () => {
  const f = await fixture(),
    channel = new LogChannel({ directory: f.root })
  try {
    channel.enqueue('{"id":1}\n')
    const first = channel.flush()
    channel.enqueue('{"id":2}\n')
    const second = channel.flush()
    const [a, b] = await Promise.all([first, second])
    assert.equal(a.throughAcceptedRecords, 1)
    assert.equal(b.throughAcceptedRecords, 2)
    assert(b.flushedAcceptedRecords >= 2)
    assert.equal(b.durable, true)
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('retiring canonical file owner never overlaps another writer and preserves rejection health', async () => {
  const f = await fixture()
  const a = acquireLogChannel(f.root)
  a.enqueue('{"id":1}\n')
  const closing = a.close()
  const b = acquireLogChannel(join(f.root, '.'))
  try {
    assert.equal(b.enqueue('{"id":2}\n'), false)
    await closing
    assert.equal(b.enqueue('{"id":3}\n'), true)
    const result = await b.close()
    assert.equal(a.status.acceptedRecords, 1, 'retired handle does not double-count its generation')
    assert.equal(result.status.acceptedRecords, 2)
    assert.equal(result.status.rejectedRecords, 1)
    assert.equal(result.durable, false)
    assert.equal(result.complete, false)
    assert.deepEqual(
      (await readFile(join(f.root, 'runtime.jsonl'), 'utf8'))
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line).id),
      [1, 3],
    )
  } finally {
    b.closeSync()
    await f.close()
  }
})

test('sink errors are observable and never retry uncertain partial records', async () => {
  const f = await fixture(),
    collision = join(f.root, 'not-a-directory')
  await writeFile(collision, 'fixture')
  const channel = new LogChannel({ directory: collision })
  try {
    assert(channel.enqueue('{"id":1}\n'))
    const result = await channel.flush(5000, true)
    assert.equal(result.complete, false)
    assert.equal(result.durable, false)
    assert.equal(result.status.failedRecords, 1)
    assert.equal(result.status.pendingRecords, 0)
    assert.equal(result.status.state, 'degraded')
    assert.equal(channel.enqueue('{"id":2}\n'), false)
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('startup preserves an interrupted JSON line rather than merging a new record into it', async () => {
  const f = await fixture()
  await writeFile(join(f.root, 'runtime.jsonl'), '{"incomplete":')
  const channel = new LogChannel({ directory: f.root })
  try {
    channel.enqueue('{"id":1}\n')
    assert.equal((await channel.flush(5000, true)).complete, true)
    assert.equal(await readFile(join(f.root, 'runtime.jsonl.1'), 'utf8'), '{"incomplete":')
    assert.equal(await readFile(join(f.root, 'runtime.jsonl'), 'utf8'), '{"id":1}\n')
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('released lease cannot write through an owner still held by another Store', async () => {
  const f = await fixture(),
    a = acquireLogChannel(f.root),
    b = acquireLogChannel(f.root)
  try {
    a.enqueue('{"id":1}\n')
    await a.close()
    assert.equal(a.enqueue('{"id":2}\n'), false)
    assert.equal(a.status.lastFailure, 'lease_released')
    assert.equal(a.status.rejectedRecords, 1)
    assert.equal(b.enqueue('{"id":3}\n'), true)
    assert.equal((await b.close()).complete, true)
    assert.deepEqual(
      (await readFile(join(f.root, 'runtime.jsonl'), 'utf8'))
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line).id),
      [1, 3],
    )
  } finally {
    b.closeSync()
    await f.close()
  }
})

test('slow stdout cannot block file durability or a shorter concurrent flush deadline', async () => {
  const f = await fixture()
  const moduleUrl = pathToFileURL(join(process.cwd(), 'src/diagnostics/log-writer.ts')).href
  const script = join(f.root, 'slow-stdout.mjs')
  await writeFile(
    script,
    `
    import { LogChannel } from ${JSON.stringify(moduleUrl)};
    const out = new LogChannel();
    const file = new LogChannel({directory: ${JSON.stringify(join(f.root, 'logs'))}});
    out.enqueue('x'.repeat(800000));
    file.enqueue('{"ok":true}\\n');
    const first = out.flush(2000);
    const started = Date.now();
    const short = await out.flush(20, true);
    const elapsed = Date.now() - started;
    const persisted = await file.flush(2000, true);
    await first;
    console.error(JSON.stringify({short, elapsed, persisted}));
  `,
  )
  const child = spawn(process.execPath, ['--import', 'tsx', script], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  // Deliberately never consume stdout: a slow console must not hold file delivery.
  let stderr = ''
  child.stderr.on('data', (chunk) => {
    stderr += chunk.toString()
  })
  const timeout = setTimeout(() => child.kill('SIGKILL'), 5000)
  try {
    const code = await new Promise<number | null>((resolve, reject) => {
      child.on('error', reject)
      child.on('exit', resolve)
    })
    assert.equal(code, 0, stderr)
    const result = JSON.parse(stderr.trim())
    assert.equal(result.short.complete, false)
    assert.equal(result.short.durable, false)
    assert(result.elapsed < 1000, 'own short deadline does not wait for prior 2s barrier')
    assert.equal(result.persisted.complete, true)
    assert.equal(result.persisted.durable, true)
    assert.equal(await readFile(join(f.root, 'logs/runtime.jsonl'), 'utf8'), '{"ok":true}\n')
  } finally {
    clearTimeout(timeout)
    child.stdout.destroy()
    child.stderr.destroy()
    await f.close()
  }
})

test('confirmed failed worker exit releases the last owner without changing retired health', async () => {
  const f = await fixture(),
    path = join(f.root, 'collision')
  await writeFile(path, 'not-directory')
  const a = acquireLogChannel(path)
  a.enqueue('{"id":1}\n')
  const failed = await a.close()
  assert.equal(failed.complete, false)
  await rm(path)
  const b = acquireLogChannel(path)
  try {
    assert.equal(b.enqueue('{"id":2}\n'), true)
    assert.equal((await b.close()).complete, true)
    assert.equal(a.status.failedRecords, 1)
    assert.equal(a.status.state, 'degraded')
    assert.equal(await readFile(join(path, 'runtime.jsonl'), 'utf8'), '{"id":2}\n')
  } finally {
    b.closeSync()
    await f.close()
  }
})

test('static worker ignores inherited module-loader flags and writes physical stdout', async () => {
  const moduleUrl = pathToFileURL(join(process.cwd(), 'src/diagnostics/log-writer.ts')).href
  const child = spawn(
    process.execPath,
    [
      '--import',
      'tsx',
      '--input-type=module',
      '-e',
      `
    import { LogChannel } from ${JSON.stringify(moduleUrl)};
    const channel = new LogChannel();
    channel.enqueue('physical-worker-fixture\\n');
    const result = await channel.flush(2000, true);
    if (!result.complete) throw new Error(JSON.stringify(result));
  `,
    ],
    { stdio: ['ignore', 'pipe', 'pipe'] },
  )
  let stdout = '',
    stderr = ''
  child.stdout.on('data', (chunk) => {
    stdout += chunk.toString()
  })
  child.stderr.on('data', (chunk) => {
    stderr += chunk.toString()
  })
  const timeout = setTimeout(() => child.kill('SIGKILL'), 5000)
  try {
    const code = await new Promise<number | null>((resolve, reject) => {
      child.on('error', reject)
      child.on('exit', resolve)
    })
    assert.equal(code, 0, stderr)
    assert.equal(stdout, 'physical-worker-fixture\n')
  } finally {
    clearTimeout(timeout)
  }
})

test('stdout partial write backpressure resumes only its unwritten suffix without duplicate bytes', async () => {
  const f = await fixture(),
    script = join(f.root, 'eventual-stdout.mjs')
  const moduleUrl = pathToFileURL(join(process.cwd(), 'src/diagnostics/log-writer.ts')).href
  await writeFile(
    script,
    `
    import {LogChannel} from ${JSON.stringify(moduleUrl)};
    const channel=new LogChannel();
    channel.enqueue('prefix:'+'🚀'.repeat(180000)+'\\n');
    let reported=false;
    const monitor=setInterval(()=>{if(channel.status.backpressured&&!reported){reported=true;console.error('BACKPRESSURED');}},5);
    const result=await channel.flush(3000,true);
    clearInterval(monitor);
    console.error(JSON.stringify(result));
    if(!result.complete)process.exitCode=1;
  `,
  )
  const child = spawn(process.execPath, ['--import', 'tsx', script], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const chunks: Buffer[] = []
  let stderr = '',
    reading = false
  const read = () => {
    if (reading) return
    reading = true
    child.stdout.on('data', (chunk) => {
      chunks.push(Buffer.from(chunk))
      child.stdout.pause()
      setTimeout(() => child.stdout.resume(), 2)
    })
  }
  child.stderr.on('data', (chunk) => {
    stderr += chunk.toString()
    if (stderr.includes('BACKPRESSURED')) read()
  })
  const fallback = setTimeout(read, 500),
    timeout = setTimeout(() => child.kill('SIGKILL'), 5000)
  try {
    const code = await new Promise<number | null>((resolve, reject) => {
      child.on('error', reject)
      child.on('close', resolve)
    })
    assert.equal(code, 0, stderr)
    assert.match(stderr, /BACKPRESSURED/)
    const result = JSON.parse(stderr.trim().split('\n').at(-1)!)
    assert.equal(result.complete, true)
    assert.equal(result.status.failedRecords, 0)
    assert.equal(result.status.rejectedRecords, 0)
    assert.equal(result.status.writtenRecords, 1)
    assert.equal(result.status.backpressured, false)
    assert.deepEqual(Buffer.concat(chunks), Buffer.from('prefix:' + '🚀'.repeat(180000) + '\n'))
  } finally {
    clearTimeout(fallback)
    clearTimeout(timeout)
    await f.close()
  }
})

test('same-stack records use one bounded batch and precede the durable flush barrier', async () => {
  const f = await fixture(),
    channel = new LogChannel({ directory: f.root })
  const worker = (channel as unknown as { worker: { postMessage: (...args: unknown[]) => void } })
    .worker
  const post = worker.postMessage.bind(worker),
    batches: number[] = []
  worker.postMessage = (...args) => {
    const message = args[0] as { kind: string; records?: Uint8Array[] }
    if (message.kind === 'records') batches.push(message.records!.length)
    return post(...args)
  }
  try {
    for (let id = 0; id < 3; id++) assert(channel.enqueue(JSON.stringify({ id }) + '\n'))
    assert.equal(channel.status.pendingRecords, 3)
    assert.equal((await channel.flush(2000, true)).durable, true)
    assert.deepEqual(batches, [3])
    assert.deepEqual(
      (await readFile(join(f.root, 'runtime.jsonl'), 'utf8'))
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line).id),
      [0, 1, 2],
    )
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('failed admitted batch handoff is unconfirmed, not falsely persisted or retried', async () => {
  const channel = new LogChannel()
  const worker = (channel as unknown as { worker: { postMessage: (...args: unknown[]) => void } })
    .worker
  worker.postMessage = () => {
    throw new Error('synthetic_handoff_failure')
  }
  assert(channel.enqueue('one\n'))
  assert(channel.enqueue('two\n'))
  const result = await channel.flush(2000, true)
  assert.equal(result.complete, false)
  assert.equal(result.durable, false)
  assert.equal(result.status.acceptedRecords, 2)
  assert.equal(result.status.writtenRecords, 0)
  assert.equal(result.status.rejectedRecords, 0)
  assert.equal(result.status.unconfirmedRecords, 2)
  assert.equal(result.status.pendingRecords, 0)
})

test('native exact allocation preserves Unicode bytes and accounts oversized UTF-8 before transfer', async () => {
  const f = await fixture(),
    channel = new LogChannel({ directory: f.root, maxBytes: 1024, maxRecords: 8 })
  const record = '漢字🚀\ud800\n'
  try {
    assert(channel.enqueue(record))
    assert.equal(channel.enqueue('🚀'.repeat(300)), false)
    assert.equal(channel.status.rejectedBytes, 1200)
    const result = await channel.flush(2000, true)
    assert.equal(result.status.writtenRecords, 1)
    assert.equal(result.complete, false, 'oversized rejection remains explicit')
    assert.deepEqual(await readFile(join(f.root, 'runtime.jsonl')), Buffer.from(record))
  } finally {
    channel.closeSync()
    await f.close()
  }
})

test('a lone blocked stdout deadline always resumes the caller with incomplete health', async () => {
  const f = await fixture(),
    script = join(f.root, 'deadline-only.mjs')
  const moduleUrl = pathToFileURL(join(process.cwd(), 'src/diagnostics/log-writer.ts')).href
  await writeFile(
    script,
    `
    import {LogChannel} from ${JSON.stringify(moduleUrl)};
    const channel=new LogChannel();channel.enqueue('x'.repeat(800000));
    await new Promise(resolve=>setTimeout(resolve,100));
    const result=await channel.flush(25,true);
    console.error(JSON.stringify({afterAwait:true,complete:result.complete,durable:result.durable,status:result.status}));
  `,
  )
  const child = spawn(process.execPath, ['--import', 'tsx', script], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let stderr = ''
  child.stderr.on('data', (chunk) => {
    stderr += chunk.toString()
  })
  const timeout = setTimeout(() => child.kill('SIGKILL'), 5000)
  try {
    const code = await new Promise<number | null>((resolve, reject) => {
      child.on('error', reject)
      child.on('exit', resolve)
    })
    assert.equal(code, 0, stderr)
    const result = JSON.parse(stderr.trim())
    assert.equal(result.afterAwait, true)
    assert.equal(result.complete, false)
    assert.equal(result.durable, false)
    assert.equal(result.status.lastFailure, 'flush_timeout')
  } finally {
    clearTimeout(timeout)
    child.stdout.destroy()
    child.stderr.destroy()
    await f.close()
  }
})
