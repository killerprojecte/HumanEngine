import test from 'node:test'
import assert from 'node:assert/strict'
import { Worker } from 'node:worker_threads'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { WorkerRpc } from '../src/server/worker-rpc.ts'

test(
  'owner disposal cancels a real plugin action before drain, keeping SQLite open through route cleanup',
  { timeout: 10_000 },
  async () => {
    const root = mkdtempSync(join(tmpdir(), 'he-disposal-order-'))
    const state = new Int32Array(new SharedArrayBuffer(6 * 4))
    const runtimeUrl = new URL('../src/server/owner-runtime.ts', import.meta.url).href
    const workerUrl = new URL('../src/server/owner-worker.ts', import.meta.url).href
    const worker = new Worker(
      `
    import { register } from 'tsx/esm/api'; register();
    const { workerData } = await import('node:worker_threads');
    const state = new Int32Array(workerData);
    const { getEngine } = await import(${JSON.stringify(runtimeUrl)});
    const runtime = await getEngine();
    const close = runtime.store.close.bind(runtime.store);
    runtime.store.close = () => { Atomics.store(state,4,state[3]===1?1:-1); close() };
    const dispose = runtime.engine.dispose.bind(runtime.engine);
    runtime.engine.dispose = async () => { Atomics.add(state,5,1); await dispose() };
    await runtime.engine.plugins.register({
      manifest: {id:'closeprobe',name:'Probe',version:'1',description:'Probe',configSchema:{type:'object'},capabilities:[]},
      setup(context) {
        context.signal.addEventListener('abort',()=>Atomics.store(state,1,1));
        context.onDispose(()=>{ Atomics.store(state,2,1) });
        context.registerAdminAction('wait',()=>new Promise(resolve=>{
          Atomics.store(state,0,1);
          context.signal.addEventListener('abort',()=>{
            setTimeout(()=>{
              try {
                runtime.store.db.prepare('SELECT 1').get();
                Atomics.store(state,3,runtime.store.db.open?1:-1);
              } catch { Atomics.store(state,3,-1) }
              resolve({canceled:true});
            },30);
          },{once:true});
        }));
      }
    });
    await runtime.engine.plugins.enable('closeprobe');
    await import(${JSON.stringify(workerUrl)});
  `,
      {
        eval: true,
        execArgv: [],
        workerData: state.buffer,
        env: {
          ...process.env,
          HUMANENGINE_DB: join(root, 'data', 'live.sqlite'),
          HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY: join(root, 'authority'),
          HUMANENGINE_LOG_DIR: join(root, 'logs'),
          HUMANENGINE_ADMIN_TOKEN: 'synthetic-disposal-token',
          HUMANENGINE_PLUGINS: '',
        },
      },
    )
    const rpc = new WorkerRpc(worker)
    try {
      await rpc.ready
      const action = rpc
        .call('fetch', {
          url: 'http://localhost/api/plugins/closeprobe/actions/wait',
          method: 'POST',
          headers: [
            ['Authorization', 'Bearer synthetic-disposal-token'],
            ['Content-Type', 'application/json'],
          ],
          body: new TextEncoder().encode('{}'),
        })
        .catch((error) => error)
      const deadline = performance.now() + 2000
      while (state[0] !== 1 && performance.now() < deadline)
        await new Promise((resolve) => setTimeout(resolve, 5))
      assert.equal(state[0], 1)
      const closing = rpc.close()
      await closing
      assert((await action) instanceof Error)
      assert.deepEqual([...state], [1, 1, 1, 1, 1, 1])
    } finally {
      await rpc.close()
      rmSync(root, { recursive: true, force: true })
    }
  },
)
