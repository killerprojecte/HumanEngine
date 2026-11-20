import assert from 'node:assert/strict'
import { once } from 'node:events'
import { readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { Router } from '../../src/models.ts'
import { Store } from '../../src/store.ts'

const paths = [
  ['retry-success', ['503', '200']],
  ['exhaustion-with-fallback', ['503', '503', '200']],
  ['parent-abort', ['cancel']],
]
const views = []
for (const [name, wire] of paths) {
  const store = new Store(':memory:')
  const abort = new AbortController()
  const diagnostic = [], billing = []
  let index = 0
  const server = createServer((req, res) => {
    const status = wire[index++]
    req.resume()
    if (status === 'cancel') {
      abort.abort()
      res.end()
      return
    }
    res.setHeader('Content-Type', 'application/json')
    res.statusCode = Number(status)
    if (status === '503') res.setHeader('Retry-After', '0')
    res.end(status === '200' ? JSON.stringify({
      choices: [{ finish_reason: 'stop', message: { role: 'assistant', content: 'synthetic' } }],
      usage: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 },
    }) : '{}')
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const baseUrl = `http://127.0.0.1:${server.address().port}/v1`
  const config = {
    providers: [{ id: 'p', name: 'P', protocol: 'openai-chat', baseUrl, credentialEnv: '',
      retryCount: 1, timeoutMs: 10000 }],
    models: ['primary', 'fallback'].map((id) => ({ id, providerId: 'p', apiModelName: id,
      displayName: id, capabilities: ['text'], streaming: false })),
    routes: [{ role: 'planner', orderedModelIds: ['primary', 'fallback'] }],
  }
  try {
    const router = new Router(config, (e) => {
      billing.push([e.modelId, e.event])
      store.recordModel(e, config)
    }, undefined, undefined, undefined, undefined, undefined, undefined, (e) => {
      diagnostic.push([e.modelId, e.event])
    })
    const result = router.complete('planner', [{ role: 'user', content: 'synthetic' }], [], abort.signal)
    if (name === 'parent-abort') await assert.rejects(result)
    else await result
    assert.equal(index, wire.length)
    const rows = store.usage().map((row) => [row.modelId, row.calls, row.unknownUsageCalls])
    views.push({ name, wire, billing, diagnostic, rows })
  } finally {
    server.closeAllConnections()
    await new Promise((resolve) => server.close(resolve))
    store.close()
  }
}
assert.deepEqual(views.map((v) => v.billing), [
  [['primary', 'model.completed']],
  [['primary', 'model.failed'], ['fallback', 'model.completed']],
  [],
])
assert.deepEqual(views.map((v) => v.rows), [
  [['primary', 1, 0]],
  [['fallback', 1, 0], ['primary', 1, 1]],
  [],
])
const output = JSON.stringify({ node: process.version, views }, null, 2) + '\n'
const file = new URL('../internal/modelservice/testdata/usage-retry-source.json', import.meta.url)
if (process.argv.includes('--check')) assert.equal(readFileSync(file, 'utf8'), output)
else writeFileSync(file, output)
console.log('PASS actual Router retry accounting, fallback, and parent abort')
