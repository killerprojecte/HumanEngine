import assert from 'node:assert/strict'
import test from 'node:test'
import { once } from 'node:events'
import { createServer } from 'node:http'
import { Router, type ChatMessage, type RouterConfig } from '../src/models.ts'
import { supportsOpenAIPromptBreakpoints } from '../src/models/prompt-cache.ts'

const model = {
  id: 'm',
  providerId: 'p',
  displayName: 'fixture',
  apiModelName: 'gpt-6.1-terra',
  capabilities: ['text', 'tools'] as Array<'text' | 'tools'>,
}
const provider = {
  id: 'p',
  name: 'fixture',
  protocol: 'openai-chat',
  baseUrl: 'https://api.openai.com/v1',
  credentialEnv: '',
  retryCount: 0,
  timeoutMs: 10000,
}
test('cache capability is conservative and explicit gateway opt-in is scoped to native protocols', () => {
  assert.equal(supportsOpenAIPromptBreakpoints(provider, model), true)
  assert.equal(
    supportsOpenAIPromptBreakpoints(provider, { ...model, apiModelName: 'unknown' }),
    false,
  )
  assert.equal(
    supportsOpenAIPromptBreakpoints(provider, { ...model, promptCacheMode: 'off' }),
    false,
  )
  const gateway = { ...provider, baseUrl: 'https://example.com/v1' }
  assert.equal(supportsOpenAIPromptBreakpoints(gateway, model), false)
  assert.equal(
    supportsOpenAIPromptBreakpoints(gateway, { ...model, promptCacheMode: 'openai' }),
    true,
  )
  assert.equal(
    supportsOpenAIPromptBreakpoints(
      { ...gateway, protocol: 'custom:model' },
      { ...model, promptCacheMode: 'openai' },
    ),
    false,
  )
})
for (const protocol of ['openai-chat', 'openai-responses'] as const) {
  test(`${protocol} marks stable text blocks only with capability and Planner scope`, async () => {
    const bodies: any[] = []
    const server = createServer(async (req, res) => {
      const chunks: Buffer[] = []
      for await (const chunk of req) chunks.push(Buffer.from(chunk))
      bodies.push(JSON.parse(Buffer.concat(chunks).toString()))
      res.setHeader('content-type', 'application/json')
      res.end(
        JSON.stringify(
          protocol === 'openai-chat'
            ? { choices: [{ finish_reason: 'stop', message: { content: 'Done' } }] }
            : {
                status: 'completed',
                output: [
                  {
                    type: 'message',
                    role: 'assistant',
                    content: [{ type: 'output_text', text: 'Done' }],
                  },
                ],
              },
        ),
      )
    })
    server.listen(0, '127.0.0.1')
    await once(server, 'listening')
    const address = server.address()
    assert(address && typeof address !== 'string')
    const config = (mode?: 'auto' | 'off' | 'openai'): RouterConfig => ({
      providers: [{ ...provider, protocol, baseUrl: `http://127.0.0.1:${address.port}/v1` }],
      models: [{ ...model, ...(mode ? { promptCacheMode: mode } : {}) }],
      routes: ['planner', 'replyer'].map((role) => ({
        role: role as 'planner' | 'replyer',
        orderedModelIds: ['m'],
        timeoutMs: 10000,
      })),
    })
    const messages: ChatMessage[] = [
      { role: 'system', content: 'Current trusted rules' },
      {
        role: 'user',
        contextLayout: 'evidence',
        contextCacheBoundaries: [0, 1],
        content: JSON.stringify({
          timezone: 'UTC',
          evidence: [
            { id: 'a', value: 'history one' },
            { id: 'b', value: 'history two' },
            { id: 'context', value: 'fresh hints' },
          ],
          now: 'fresh clock',
          decision: 'current decision',
        }),
      },
    ]
    try {
      const before = structuredClone(messages)
      await new Router(config('openai')).complete('planner', messages)
      const body = bodies.at(-1)
      assert.deepEqual(body.prompt_cache_options, { mode: 'implicit' })
      const wire = body[protocol === 'openai-chat' ? 'messages' : 'input']
      const marked = wire.filter(
        (m: any) =>
          Array.isArray(m.content) && m.content.some((c: any) => c.prompt_cache_breakpoint),
      )
      assert.equal(marked.length, 3)
      for (const m of marked) {
        assert.equal(m.content[0].type, protocol === 'openai-chat' ? 'text' : 'input_text')
        assert.deepEqual(m.content[0].prompt_cache_breakpoint, { mode: 'explicit' })
      }
      assert.equal(typeof wire.at(-1).content, 'string')
      assert.equal(JSON.parse(wire.at(-1).content).now, 'fresh clock')
      assert.equal(
        typeof wire.at(-2).content,
        'string',
        'fresh hints must not become a stable boundary',
      )
      assert.deepEqual(messages, before)
      for (const [mode, role] of [
        ['auto', 'planner'],
        ['off', 'planner'],
        ['openai', 'replyer'],
      ] as const) {
        await new Router(config(mode)).complete(role, messages)
        const ignored = bodies.at(-1)
        assert.equal(ignored.prompt_cache_options, undefined)
        assert.equal(JSON.stringify(ignored).includes('prompt_cache_breakpoint'), false)
        assert.equal(JSON.stringify(ignored).includes('contextCacheBoundaries'), false)
      }
    } finally {
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    }
  })
}
