import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import test from 'node:test'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { probe } from '../src/probes.ts'

test('Explicit probes exercise both single-instance protocols, validate capabilities, and record usage without config mutation', async () => {
  const oldKey = process.env.HUMANENGINE_PROBE_FIXTURE_KEY
  process.env.HUMANENGINE_PROBE_FIXTURE_KEY = 'fixture-only-secret'
  let mode = 'text',
    bad = false,
    requests = 0
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    const body = JSON.parse(Buffer.concat(chunks).toString())
    requests++
    assert.equal(body.model, 'selected-model')
    assert.equal(req.headers.authorization, 'Bearer fixture-only-secret')
    res.setHeader('content-type', 'application/json')
    if (bad && mode === 'text') {
      res.statusCode = 400
      res.end(JSON.stringify({ error: { message: 'private-provider-body' } }))
      return
    }
    if (mode === 'embedding') {
      res.end(
        JSON.stringify({
          data: [{ index: 0, embedding: bad ? [0, 0] : [0.5, 0.5] }],
          usage: { prompt_tokens: 3, total_tokens: 3 },
        }),
      )
      return
    }
    if (mode === 'vision') {
      const encoded = JSON.stringify(body).match(/data:image\/png;base64,([A-Za-z0-9+/=]+)/)?.[1]
      assert.ok(encoded)
      assert.deepEqual(
        [...(await sharp(Buffer.from(encoded, 'base64')).raw().toBuffer())],
        [255, 255, 255],
      )
    }
    const responses = req.url?.endsWith('/responses')
    const args = JSON.stringify({ value: bad ? 'wrong' : 'humanengine-probe' })
    res.end(
      JSON.stringify(
        responses
          ? {
              status: 'completed',
              output:
                mode === 'tools'
                  ? [{ type: 'function_call', call_id: 'c', name: 'probe__echo', arguments: args }]
                  : [
                      {
                        type: 'message',
                        role: 'assistant',
                        content: [{ type: 'output_text', text: 'OK' }],
                      },
                    ],
              usage: { input_tokens: 3, output_tokens: 2 },
            }
          : {
              choices: [
                {
                  finish_reason: mode === 'tools' ? 'tool_calls' : 'stop',
                  message:
                    mode === 'tools'
                      ? {
                          role: 'assistant',
                          content: null,
                          tool_calls: [
                            {
                              id: 'c',
                              type: 'function',
                              function: { name: 'probe__echo', arguments: args },
                            },
                          ],
                        }
                      : { role: 'assistant', content: 'OK' },
                },
              ],
              usage: { prompt_tokens: 3, completion_tokens: 2 },
            },
      ),
    )
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address !== 'string')
  const store = new Store(':memory:')
  try {
    for (const protocol of ['openai-chat', 'openai-responses'] as const) {
      store.set('models', {
        providers: [
          {
            id: 'p',
            name: 'Fixture',
            protocol,
            baseUrl: `http://127.0.0.1:${address.port}/v1`,
            credentialEnv: 'HUMANENGINE_PROBE_FIXTURE_KEY',
          },
        ],
        models: [
          {
            id: 'selected',
            displayName: 'Selected',
            providerId: 'p',
            apiModelName: 'selected-model',
            capabilities: ['text'],
            inputUsdPerMillion: 1,
            outputUsdPerMillion: 2,
          },
          {
            id: 'fallback',
            displayName: 'Must not run',
            providerId: 'p',
            apiModelName: 'fallback-model',
            capabilities: ['text'],
          },
        ],
        routes: [{ role: 'thinker', orderedModelIds: ['fallback', 'selected'], timeoutMs: 1000 }],
      })
      const before = JSON.stringify(store.models)
      for (const capability of ['text', 'tools', 'vision', 'embedding'] as const) {
        mode = capability
        bad = false
        const result = await probe(store, 'selected', capability)
        assert.equal(result.ok, true, JSON.stringify(result))
        assert.ok(result.durationMs >= 0)
      }
      mode = 'tools'
      bad = true
      assert.equal((await probe(store, 'selected', 'tools')).detail, 'tool_call_validation_failed')
      mode = 'embedding'
      assert.equal(
        (await probe(store, 'selected', 'embedding')).detail,
        'embedding_validation_failed',
      )
      mode = 'text'
      const count = requests
      const failed = await probe(store, 'selected', 'text')
      assert.equal(failed.ok, false)
      assert.equal(requests, count + 1)
      assert.ok(!JSON.stringify(failed).includes('private-provider-body'))
      assert.equal(JSON.stringify(store.models), before)
    }
    const count = requests
    assert.equal((await probe(store, 'missing', 'text')).ok, false)
    assert.equal(requests, count)
    const usage = store.db.prepare('SELECT SUM(calls) AS calls FROM model_usage').get() as {
      calls: number
    }
    assert.equal(usage.calls, 14)
    assert.ok(!JSON.stringify(store.logs).includes('private-provider-body'))
    assert.ok(!JSON.stringify(store.logs).includes('fixture-only-secret'))
  } finally {
    if (oldKey === undefined) delete process.env.HUMANENGINE_PROBE_FIXTURE_KEY
    else process.env.HUMANENGINE_PROBE_FIXTURE_KEY = oldKey
    store.close()
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})
