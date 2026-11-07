// Isolated process fixture. All failures and data are synthetic; never kills the test runner.
import { register } from 'tsx/esm/api'
register()
const { Worker } = await import('node:worker_threads')
const { createServer } = await import('node:http')
const { mkdirSync, writeFileSync } = await import('node:fs')
const { dirname } = await import('node:path')
const mode = process.argv[2]
if (
  [
    'host-exit',
    'verifier-exit',
    'planned-close',
    'startup-error',
    'verifier-startup-error',
  ].includes(mode)
) {
  if (mode === 'verifier-startup-error') {
    // Use the actual host construction/cached-failure path and actual worker entries.
    // Only the verifier's initial database path is made invalid, before readiness.
    const output = dirname(process.env.HUMANENGINE_DB) + '/isolated-runtime'
    mkdirSync(output + '/.output/server', { recursive: true })
    const loader = JSON.stringify(import.meta.resolve('tsx/esm/api'))
    for (const [name, source] of [
      ['engine-owner', 'owner-worker'],
      ['config-reader', 'config-worker'],
    ]) {
      const url = new URL('../../src/server/' + source + '.ts', import.meta.url).href
      writeFileSync(
        output + '/.output/server/' + name + '.mjs',
        `import { register } from ${loader}; register();
        import { workerData } from 'node:worker_threads';
        ${name === 'config-reader' ? "workerData.database += '.missing';" : ''}
        await import(${JSON.stringify(url)});`,
      )
    }
    process.chdir(output)
    process.env.NODE_ENV = 'production'
  }
  const runtimeModule = await import('../../server/utils/engine.ts')
  if (mode === 'startup-error' || mode === 'verifier-startup-error') {
    if (mode === 'startup-error') {
      mkdirSync(dirname(process.env.HUMANENGINE_DB), { recursive: true })
      writeFileSync(process.env.HUMANENGINE_DB, 'synthetic invalid database')
    }
    const first = runtimeModule.getEngine()
    await first.catch(() => {})
    const response = await runtimeModule.handleEngineRequest(
      new Request('http://localhost/api/status', {
        headers: { Authorization: `Bearer ${process.env.HUMANENGINE_ADMIN_TOKEN}` },
      }),
    )
    const same = runtimeModule.getEngine() === first
    await runtimeModule.closeEngine()
    process.send?.({ status: response.status, cached: same })
    process.exitCode = 0
  } else {
    const runtime = await runtimeModule.getEngine()
    if (mode === 'planned-close') {
      await runtimeModule.closeEngine()
      process.send?.({ closed: true })
      process.exitCode = 0
    } else {
      // A live listener proves exitCode alone cannot implement fail-stop supervision.
      createServer(() => {}).listen(0, '127.0.0.1', () => {
        void (mode === 'verifier-exit' ? runtime.reader : runtime.owner).worker.terminate()
      })
    }
  }
} else if (mode === 'committed-exit') {
  const { WorkerRpc } = await import('../../src/server/worker-rpc.ts')
  const { createOwnerFailureHandler } = await import('../../src/server/owner-supervision.ts')
  const ownerRuntime = new URL('../../src/server/owner-runtime.ts', import.meta.url).href
  const ownerWorker = new URL('../../src/server/owner-worker.ts', import.meta.url).href
  const worker = new Worker(
    `
    import { register } from 'tsx/esm/api'; register();
    const { getEngine } = await import(${JSON.stringify(ownerRuntime)});
    const runtime = await getEngine();
    runtime.app.post('/api/synthetic-committed-exit', () => {
      runtime.store.set('synthetic-exit-write-count', runtime.store.get('synthetic-exit-write-count', 0) + 1);
      process.exit(0);
    });
    await import(${JSON.stringify(ownerWorker)});
  `,
    { eval: true },
  )
  const rpc = new WorkerRpc(worker, { onUnexpectedExit: createOwnerFailureHandler() })
  await rpc.ready
  const server = createServer(async (_req, res) => {
    try {
      await rpc.call('fetch', {
        url: 'http://localhost/api/synthetic-committed-exit',
        method: 'POST',
        headers: [['Authorization', `Bearer ${process.env.HUMANENGINE_ADMIN_TOKEN}`]],
        body: new Uint8Array(),
      })
      res.end('unexpected success')
    } catch {
      res.writeHead(503)
      res.end('Outcome may be unknown. Do not automatically retry writes.')
    }
  })
  server.listen(0, '127.0.0.1', () => process.send?.({ port: server.address().port }))
} else {
  throw new Error('invalid synthetic supervision mode')
}
