import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import { ModelError, Router, type RouterConfig } from '../src/models.ts'

test('Router preserves sanitized transient retry eligibility only when every attempted route has an explicit eligible HTTP failure', async () => {
  for (const statuses of [[503], [429, 504], [503, 401], [502, 200], [500]]) {
    let requests = 0
    const server = createServer(async (req, res) => {
      for await (const _chunk of req) {
        /* consume request */
      }
      res.statusCode = statuses[requests++]!
      res.setHeader('content-type', 'application/json')
      res.end(JSON.stringify({ error: 'private-provider-error-sentinel' }))
    })
    server.listen(0, '127.0.0.1')
    await once(server, 'listening')
    const address = server.address()
    assert.ok(address && typeof address !== 'string')
    const config: RouterConfig = {
      providers: [
        {
          id: 'fixture',
          name: 'Fixture',
          protocol: 'openai-chat',
          baseUrl: `http://127.0.0.1:${address.port}/v1`,
          credentialEnv: '',
          retryCount: 0,
          timeoutMs: 1000,
        },
      ],
      models: statuses.map((_status, index) => ({
        id: `model${index}`,
        displayName: 'Fixture',
        providerId: 'fixture',
        apiModelName: 'fixture',
        capabilities: ['text'],
      })),
      routes: [
        {
          role: 'learning',
          orderedModelIds: statuses.map((_status, index) => `model${index}`),
          timeoutMs: 1000,
        },
      ],
    }
    try {
      await assert.rejects(
        new Router(config).complete('learning', [{ role: 'user', content: 'Synthetic fixture' }]),
        (error: unknown) => {
          assert.ok(error instanceof ModelError)
          const transient = statuses.every((status) => status === 429 || status >= 500)
          assert.equal(error.retryable, transient)
          assert.equal(error.status, transient ? statuses.at(-1) : undefined)
          assert.equal(error.code, 'all_models_failed')
          assert.equal(error.message.includes('private-provider'), false)
          return true
        },
      )
      assert.equal(requests, statuses.length)
    } finally {
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    }
  }
})
