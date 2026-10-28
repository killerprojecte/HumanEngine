import { fixtureReplyPlan } from './helpers/reply-plan.ts'
import type { ChatMessage } from '../src/models.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

test('manual rules survive authenticated save and every outgoing simulation planner/thinker request', async () => {
  let calls = 0
  const captured: Array<Array<{ role: string; content: string }>> = []
  const server = createServer(async (req, res) => {
    const chunks: Buffer[] = []
    for await (const chunk of req) chunks.push(Buffer.from(chunk))
    const body = JSON.parse(Buffer.concat(chunks).toString()) as {
      messages: Array<{ role: string; content: string }>
      tools: Array<{ function: { name: string } }>
    }
    captured.push(body.messages)
    calls++
    const planner = body.tools.some((t) => t.function.name === 'participation__decide')
    const call = planner
      ? {
          id: 'decision',
          type: 'function',
          function: {
            name: 'participation__decide',
            arguments: JSON.stringify({ action: 'reply', reasonCode: 'addressed' }),
          },
        }
      : body.tools.some((t) => t.function.name === 'reply__plan')
        ? {
            id: 'reply-plan',
            type: 'function',
            function: {
              name: 'reply__plan',
              arguments: JSON.stringify(fixtureReplyPlan(body.messages as ChatMessage[])),
            },
          }
        : calls === 3
          ? {
              id: 'send',
              type: 'function',
              function: {
                name: 'messages__send',
                arguments: JSON.stringify({
                  operationId: 'once',
                  segments: [{ parts: [{ type: 'text', text: 'Fixture reply' }] }],
                }),
              },
            }
          : undefined
    res.setHeader('Content-Type', 'application/json')
    res.end(
      JSON.stringify({
        choices: [
          {
            finish_reason: call ? 'tool_calls' : 'stop',
            message: {
              role: 'assistant',
              content: call ? null : 'Not automatically delivered',
              ...(call ? { tool_calls: [call] } : {}),
            },
          },
        ],
        usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
      }),
    )
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address() as { port: number }
  process.env.HUMANENGINE_FIXTURE_KEY = 'fixture-only'
  const store = new Store(':memory:'),
    engine = new Engine(store)
  try {
    await engine.start()
    const app = createApp(store, { engine, token: 'admin' }),
      headers = { Authorization: 'Bearer admin', 'Content-Type': 'application/json' }
    store.set('models', {
      providers: [
        {
          id: 'p',
          name: 'Fixture',
          protocol: 'openai-chat',
          baseUrl: `http://127.0.0.1:${address.port}`,
          credentialEnv: 'HUMANENGINE_FIXTURE_KEY',
        },
      ],
      models: [
        {
          id: 'm',
          displayName: 'Fixture',
          providerId: 'p',
          apiModelName: 'fixture',
          capabilities: ['text', 'tools'],
        },
      ],
      routes: [
        { role: 'planner', orderedModelIds: ['m'], timeoutMs: 10000 },
        { role: 'thinker', orderedModelIds: ['m'], timeoutMs: 10000 },
      ],
    })
    const rules = [
      {
        id: 'language',
        scope: 'global',
        content: 'Respond in French.',
        nonOverridable: true,
        enabled: true,
      },
      {
        id: 'disabled',
        scope: 'global',
        content: 'DISABLED_RULE_SENTINEL',
        nonOverridable: true,
        enabled: false,
      },
      {
        id: 'isolated',
        scope: 'scene',
        sceneId: 'private-secret',
        content: 'SCENE_RULE_SENTINEL',
        nonOverridable: false,
        enabled: true,
      },
    ]
    assert.equal(
      (await app.request('/api/rules', { method: 'PUT', headers, body: JSON.stringify(rules) }))
        .status,
      200,
    )
    assert.deepEqual(await (await app.request('/api/rules', { headers })).json(), rules)
    engine.memory.create({
      scope: { kind: 'scene', sceneId: 'private-secret', personId: 'person' },
      kind: 'fact',
      content: 'Private secret',
    })
    const before = store.db.prepare('SELECT * FROM memories').all()
    const created = (await (
      await app.request('/api/simulations', { method: 'POST', headers })
    ).json()) as { id: string }
    const response = await app.request(`/api/simulations/${created.id}/messages`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ text: 'Hello' }),
    })
    assert.equal(response.status, 200)
    const messages = (await response.json()) as Array<{
      senderId: string
      parts: Array<{ text: string }>
    }>
    assert.equal(messages.length, 2)
    assert.equal(messages[1]?.parts[0]?.text, 'Fixture reply')
    assert.equal(calls, 3)
    for (const messages of captured) {
      const system = messages
        .filter((m) => m.role === 'system')
        .map((m) => m.content)
        .join('\n')
      assert.match(system, /Respond in French\./)
      assert.doesNotMatch(system, /DISABLED_RULE_SENTINEL|SCENE_RULE_SENTINEL/)
    }
    // This mock intentionally returns English: injection is not proof of model compliance.

    assert.deepEqual(store.db.prepare('SELECT * FROM memories').all(), before)
    assert.equal((await app.request('/api/plugins', { headers })).status, 200)
    await app.request(`/api/simulations/${created.id}`, { method: 'DELETE', headers })
    assert.deepEqual(engine.runtime.preview(created.id), [])
  } finally {
    await engine.dispose()
    store.close()
    delete process.env.HUMANENGINE_FIXTURE_KEY
    server.closeAllConnections()
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
})

test('manual rule scope uses stable scene IDs and excludes disabled or other-scene rules', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const scene = {
    id: 'stable',
    kind: 'private' as const,
    adapterId: 'local',
    accountId: 'bot',
    platformSceneId: 'external',
    name: 'Renamed display name',
    participationEnabled: true,
    learningEnabled: false,
    proactiveEnabled: false,
  }
  const context = {
    scene,
    mode: 'real' as const,
    turnId: 'test',
    signal: new AbortController().signal,
    dependencies: new Set<string>(),
    validations: [],
  }
  store.set('rules', [
    {
      id: 'global',
      scope: 'global',
      content: 'Respond in French.',
      nonOverridable: false,
      enabled: true,
    },
    {
      id: 'local',
      scope: 'scene',
      sceneId: 'stable',
      content: 'Use short sentences.',
      nonOverridable: true,
      enabled: true,
    },
    {
      id: 'other',
      scope: 'scene',
      sceneId: 'other',
      content: 'Other scene.',
      nonOverridable: false,
      enabled: true,
    },
    {
      id: 'disabled',
      scope: 'global',
      content: 'Disabled.',
      nonOverridable: false,
      enabled: false,
    },
  ])
  try {
    assert.deepEqual(
      engine
        .rules(context)
        .map((r) => r.id)
        .sort(),
      ['global', 'local'],
    )
    const renamedScene = { ...scene, name: 'Another title' }
    assert.deepEqual(
      engine
        .rules({ ...context, scene: renamedScene })
        .map((r) => r.id)
        .sort(),
      ['global', 'local'],
    )
    assert.deepEqual(
      engine.rules({ ...context, mode: 'simulation' }).map((r) => r.id),
      ['global'],
    )
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('duplicate rule IDs are rejected without overwriting saved rules', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  try {
    const app = createApp(store, { engine, token: 'mock-admin' })
    const existing = {
      id: 'existing',
      scope: 'global',
      content: 'Retain this rule.',
      enabled: true,
      nonOverridable: false,
    }
    store.set('rules', [existing])
    const rule = {
      id: 'same',
      scope: 'global',
      content: 'Prefer concise replies.',
      enabled: true,
      nonOverridable: false,
    }
    const response = await app.request('/api/rules', {
      method: 'PUT',
      headers: { Authorization: 'Bearer mock-admin', 'Content-Type': 'application/json' },
      body: JSON.stringify([rule, rule]),
    })
    assert.equal(response.status, 400)
    assert.deepEqual(await response.json(), { error: 'duplicate_rule_ids' })
    assert.deepEqual(store.get('rules', []), [existing])
  } finally {
    await engine.dispose()
    store.close()
  }
})
