import test from 'node:test'
import assert from 'node:assert/strict'
import { Worker } from 'node:worker_threads'
import { WorkerRpc, readBody } from '../src/server/worker-rpc.ts'

test('aborted RPC remains tracked until completion, admits further work and never retries', async () => {
  const worker = new Worker(
    `const {parentPort}=require('node:worker_threads');
    let count=0; parentPort.on('message',m=>{ if(m.cancel || m.release) return;
    if(m.op==='close' || m.op==='config') {parentPort.postMessage({id:m.id,result:count}); return}
    count++; setTimeout(()=>parentPort.postMessage({id:m.id,result:count}),150)
    }); parentPort.postMessage({ready:true})`,
    { eval: true },
  )
  const rpc = new WorkerRpc(worker)
  await rpc.ready
  try {
    const controllers = Array.from({ length: 64 }, () => new AbortController())
    const calls = controllers.map((controller) =>
      rpc.call('fetch', {}, { signal: controller.signal }).catch((error) => error),
    )
    controllers.forEach((controller) => controller.abort())
    assert((await Promise.all(calls)).every((result) => result instanceof Error))
    assert.equal((Reflect.get(rpc, 'pending') as Map<number, unknown>).size, 64)
    assert.equal(await rpc.call('config', null), 64)
    await new Promise((resolve) => setTimeout(resolve, 200))
    assert.equal((Reflect.get(rpc, 'pending') as Map<number, unknown>).size, 0)
    assert.equal(await rpc.call('config', null), 64)
    const abort = AbortSignal.abort()
    await assert.rejects(rpc.call('fetch', {}, { signal: abort }), /aborted/)
  } finally {
    await rpc.close()
  }
})

test('worker death rejects outstanding operations without replacement', async () => {
  const worker = new Worker(`require('node:worker_threads').parentPort.postMessage({ready:true})`, {
    eval: true,
  })
  const rpc = new WorkerRpc(worker)
  await rpc.ready
  await assert.rejects(rpc.call('fetch', {}), /unavailable/)
  await assert.rejects(rpc.call('fetch', {}), /unavailable/)
  await rpc.close()
})

test('body cancellation interrupts a stalled stream and releases its reader', async () => {
  let canceled = false
  const stream = new ReadableStream<Uint8Array>({
    cancel() {
      canceled = true
    },
  })
  const controller = new AbortController()
  const pending = readBody(stream, { signal: controller.signal })
  controller.abort()
  await assert.rejects(pending, /aborted/)
  assert(canceled)
  assert.equal(stream.locked, false)
})

test('delivered result cleanup is exact without an outstanding-request limit', async () => {
  const worker = new Worker(
    `const {parentPort}=require('node:worker_threads');
    parentPort.on('message',m=>{ if(m.release || m.cancel) return;
      parentPort.postMessage({id:m.id,result:{ok:true}})
    }); parentPort.postMessage({ready:true})`,
    { eval: true },
  )
  const rpc = new WorkerRpc(worker)
  await rpc.ready
  try {
    const results = await Promise.all(
      Array.from({ length: 64 }, () => rpc.call<object>('fetch', {}, { lease: true })),
    )
    assert.deepEqual(await rpc.call('fetch', {}), { ok: true })
    rpc.releaseResult(results[0]!)
    rpc.releaseResult(results[0]!)
    const next = await rpc.call<object>('fetch', {}, { lease: true })
    assert.deepEqual(await rpc.call('fetch', {}), { ok: true })
    rpc.releaseResult(next)
    results.forEach((result) => rpc.releaseResult(result))
  } finally {
    await rpc.close()
  }
})

test('dedicated close control drains canceled work and concurrent close is idempotent', async () => {
  const shared = new Int32Array(new SharedArrayBuffer(4 * 4))
  const worker = new Worker(
    `const {parentPort,workerData}=require('node:worker_threads');
    const state = new Int32Array(workerData); let active=0;
    parentPort.on('message', async m=>{
      if(m.release || m.cancel) return;
      if(m.op==='close') {
        Atomics.add(state,0,1);
        Atomics.store(state,1,active);
        while(active) await new Promise(r=>setTimeout(r,5));
        Atomics.store(state,2,1);
        parentPort.postMessage({id:m.id,result:null}); return;
      }
      active++; Atomics.add(state,3,1);
      setTimeout(()=>{active--;parentPort.postMessage({id:m.id,result:{ok:true}})},150);
    }); parentPort.postMessage({ready:true})`,
    { eval: true, workerData: shared.buffer },
  )
  const rpc = new WorkerRpc(worker)
  await rpc.ready
  const controllers = Array.from({ length: 64 }, () => new AbortController())
  const calls = controllers.map((controller) =>
    rpc.call('fetch', {}, { signal: controller.signal }).catch((error) => error),
  )
  controllers.forEach((controller) => controller.abort())
  assert((await Promise.all(calls)).every((value) => value instanceof Error))
  const first = rpc.close()
  assert.equal(rpc.close(), first)
  await assert.rejects(rpc.call('fetch', {}), /closing/)
  await first
  assert.equal(shared[0], 1)
  assert.equal(shared[1], 64)
  assert.equal(shared[2], 1)
  assert.equal(shared[3], 64)
})

test('close control is delivered during pending startup without waiting for ready', async () => {
  const shared = new Int32Array(new SharedArrayBuffer(4))
  const worker = new Worker(
    `const {parentPort,workerData}=require('node:worker_threads');
    const state=new Int32Array(workerData);
    parentPort.on('message',m=>{if(m.op==='close'){
      Atomics.add(state,0,1);parentPort.postMessage({id:m.id,result:null});
    }});`,
    { eval: true, workerData: shared.buffer },
  )
  const rpc = new WorkerRpc(worker)
  const ready = assert.rejects(rpc.ready, /unavailable/)
  const first = rpc.close()
  assert.equal(rpc.close(), first)
  await first
  await ready
  assert.equal(shared[0], 1)
})

test('body reads and IPC transfer accept payloads above the rejected byte ceilings', async () => {
  const chunk = new Uint8Array(1024 * 1024).fill(7)
  let remaining = 129
  const body = await readBody(
    new ReadableStream<Uint8Array>({
      pull(controller) {
        if (remaining--) controller.enqueue(chunk)
        else controller.close()
      },
    }),
  )
  assert.equal(body.byteLength, 129 * 1024 * 1024)
  const worker = new Worker(
    `const {parentPort}=require('node:worker_threads');
    parentPort.on('message',m=>{
      if(m.release || m.cancel)return;
      if(m.op==='close'){parentPort.postMessage({id:m.id,result:null});return}
      const body=m.payload.body;
      parentPort.postMessage({id:m.id,result:body},[body.buffer]);
    });parentPort.postMessage({ready:true})`,
    { eval: true },
  )
  const rpc = new WorkerRpc(worker)
  await rpc.ready
  try {
    const reply = await rpc.call<Uint8Array>('fetch', { body })
    assert.equal(body.byteLength, 0, 'owned input transferred rather than cloned')
    assert.equal(reply.byteLength, 129 * 1024 * 1024)
    assert.equal(reply[0], 7)
    assert.equal(reply[reply.length - 1], 7)
  } finally {
    await rpc.close()
  }
})

test(
  'shutdown does not force termination at the rejected twenty-second grace',
  { timeout: 30000 },
  async () => {
    const worker = new Worker(
      `const {parentPort}=require('node:worker_threads');
    let closeId;
    parentPort.on('message',m=>{
      if(m.op==='close')closeId=m.id;
      if(m.complete)parentPort.postMessage({id:closeId,result:null});
    });parentPort.postMessage({ready:true})`,
      { eval: true },
    )
    const rpc = new WorkerRpc(worker)
    await rpc.ready
    let exited = false,
      completed = false
    worker.on('exit', () => {
      exited = true
    })
    const closing = rpc.close().then(() => {
      completed = true
    })
    try {
      await new Promise((resolve) => setTimeout(resolve, 21000))
      assert.equal(exited, false)
      assert.equal(completed, false)
      worker.postMessage({ complete: true })
      await closing
      assert(completed)
    } finally {
      worker.postMessage({ complete: true })
      await rpc.close()
    }
  },
)
