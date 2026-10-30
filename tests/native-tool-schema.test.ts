import test from 'node:test'
import assert from 'node:assert/strict'
import { z } from '@hono/zod-openapi'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import { nativeToolSchema } from '../src/models/native-tool-schema.ts'

const command = z.union([
  z.object({ kind: z.literal('lookup'), query: z.string().min(1) }).strict(),
  z.object({ kind: z.literal('select'), ids: z.array(z.string().min(1)).min(1).max(2) }).strict(),
])
const inputSchema = z.toJSONSchema(command)
function engine(protocol: string) {
  const store = new Store(':memory:')
  store.log = (level, module, code) => ({
    id: 'synthetic-log',
    at: '2026-10-30T00:00:00Z',
    level,
    module,
    code,
  })
  store.set('models', {
    providers: [
      {
        id: 'synthetic',
        name: 'Synthetic',
        protocol,
        baseUrl: 'https://synthetic.invalid/v1',
        credentialEnv: '',
        timeoutMs: 1000,
        retryCount: 0,
      },
    ],
    models: [
      {
        id: 'synthetic',
        displayName: 'Synthetic',
        providerId: 'synthetic',
        apiModelName: 'synthetic-model',
        capabilities: ['text', 'tools'],
      },
    ],
    routes: [{ role: 'planner', orderedModelIds: ['synthetic'], timeoutMs: 1000 }],
  })
  return { store, engine: new Engine(store) }
}

test('Engine native Chat and Responses wire preserve every object-union constraint with redundant root type', async () => {
  const original = globalThis.fetch
  try {
    for (const protocol of ['openai-chat', 'openai-responses']) {
      const f = engine(protocol),
        before = structuredClone(inputSchema)
      let calls = 0
      globalThis.fetch = async (_url, init) => {
        calls++
        const body = JSON.parse(String(init?.body))
        const parameters =
          protocol === 'openai-chat' ? body.tools[0].function.parameters : body.tools[0].parameters
        assert.equal(parameters.type, 'object')
        assert.deepEqual(parameters, { ...before, type: 'object' })
        return new Response(
          JSON.stringify(
            protocol === 'openai-chat'
              ? {
                  choices: [
                    {
                      message: {
                        content: '',
                        tool_calls: [
                          {
                            id: 'tool1',
                            type: 'function',
                            function: {
                              name: 'fixture__command',
                              arguments: JSON.stringify({ kind: 'lookup', query: 'synthetic' }),
                            },
                          },
                        ],
                      },
                      finish_reason: 'tool_calls',
                    },
                  ],
                  usage: { prompt_tokens: 2, completion_tokens: 2 },
                }
              : {
                  status: 'completed',
                  output: [
                    {
                      type: 'function_call',
                      status: 'completed',
                      call_id: 'tool1',
                      name: 'fixture__command',
                      arguments: JSON.stringify({ kind: 'lookup', query: 'synthetic' }),
                    },
                  ],
                  usage: { input_tokens: 2, output_tokens: 2 },
                },
          ),
          { headers: { 'content-type': 'application/json' } },
        )
      }
      try {
        const out = await f.engine
          .router()
          .complete(
            'planner',
            [{ role: 'user', content: 'Synthetic schema contract only' }],
            [{ name: 'fixture.command', description: 'Synthetic command', inputSchema }],
          )
        assert.equal(calls, 1)
        assert.equal(out.toolCalls[0]!.name, 'fixture.command')
        assert.ok(command.safeParse(out.toolCalls[0]!.arguments).success)
        assert.deepEqual(inputSchema, before)
        for (const invalid of [
          null,
          [],
          { kind: 'lookup', query: '' },
          { kind: 'lookup', query: 'x', extra: true },
          { kind: 'select', ids: [] },
          { kind: 'select', ids: ['a', 'b', 'c'] },
        ])
          assert.equal(command.safeParse(invalid).success, false)
      } finally {
        await f.engine.dispose()
        f.store.close()
      }
    }
  } finally {
    globalThis.fetch = original
  }
})

test('native adaptation is narrowly provable, nonmutating and leaves unsupported schemas unchanged', () => {
  const branch = {
    type: 'object',
    required: ['x'],
    properties: { x: { type: 'string' } },
    additionalProperties: false,
  }
  for (const key of ['oneOf', 'anyOf']) {
    const schema = {
      description: 'Synthetic',
      [key]: [branch, { type: 'object', allOf: [{ required: ['y'] }] }],
      unevaluatedProperties: false,
    }
    const before = structuredClone(schema)
    const mapped = nativeToolSchema(schema)
    assert.deepEqual(mapped, { ...schema, type: 'object' })
    assert.deepEqual(schema, before)
  }
  for (const schema of [
    {},
    { type: undefined, anyOf: [branch] },
    { type: null, anyOf: [branch] },
    { type: [], anyOf: [branch] },
    { type: 'object', anyOf: [branch] },
    { type: 'string', anyOf: [branch] },
    { anyOf: [] },
    { anyOf: [branch, { type: 'string' }] },
    { oneOf: [branch, { $ref: '#/$defs/unknown' }] },
    { anyOf: [{ properties: { x: { type: 'string' } } }] },
    { anyOf: [branch], oneOf: [{}] },
    { oneOf: [true, branch] },
    { anyOf: [{ type: ['object', 'null'] }] },
  ])
    assert.equal(nativeToolSchema(schema), schema)
})

test('non-native protocol receives untouched original schema rather than OpenAI adaptation', async () => {
  const f = engine('fixture:anthropic'),
    controller = new AbortController()
  let seen = false
  f.engine.protocols.register(
    'fixture:anthropic',
    'Synthetic non-native protocol',
    {
      async complete(_context, _messages, tools) {
        seen = true
        assert.deepEqual(tools[0]!.inputSchema, inputSchema)
        assert.equal(tools[0]!.inputSchema.type, undefined)
        return {
          text: 'ok',
          toolCalls: [],
          usage: { inputTokens: 1, outputTokens: 1, totalTokens: 2 },
        }
      },
    },
    controller.signal,
  )
  try {
    await f.engine
      .router()
      .complete(
        'planner',
        [{ role: 'user', content: 'Synthetic' }],
        [{ name: 'fixture.command', description: 'Synthetic', inputSchema }],
      )
    assert.ok(seen)
  } finally {
    controller.abort()
    await f.engine.dispose()
    f.store.close()
  }
})

test('proof uses wire-visible own enumerable types, not inherited, hidden or custom JSON values', () => {
  const own = { type: 'object' }
  const hidden = Object.defineProperty({}, 'type', { value: 'object', enumerable: false })
  const accessor = Object.defineProperty({}, 'type', { get: () => 'object', enumerable: true })
  const custom = { type: 'object', toJSON: () => ({ type: 'string' }) }
  for (const branch of [
    Object.create({ type: 'object' }),
    Object.assign(Object.create({ foreign: true }), { type: 'object' }),
    hidden,
    accessor,
    custom,
  ]) {
    const schema = { anyOf: [own, branch] }
    assert.equal(nativeToolSchema(schema), schema)
  }
  let getterReads = 0
  const accessorBranches = Object.defineProperty([own], '0', {
    get: () => {
      getterReads++
      return own
    },
    enumerable: true,
  })
  const accessorUnion = { anyOf: accessorBranches }
  assert.equal(nativeToolSchema(accessorUnion), accessorUnion)
  assert.equal(getterReads, 0)
  const sparse = { anyOf: [own, , own] }
  assert.equal(nativeToolSchema(sparse), sparse)
  const prototypeRoot = Object.assign(Object.create({ foreign: true }), { anyOf: [own] })
  assert.equal(nativeToolSchema(prototypeRoot), prototypeRoot)
  const getterRoot = Object.defineProperty({ anyOf: [own] }, 'description', {
    get: () => 'unsafe',
    enumerable: true,
  })
  assert.equal(nativeToolSchema(getterRoot), getterRoot)
  const customRoot = { anyOf: [own], toJSON: () => ({ type: 'string' }) }
  assert.equal(nativeToolSchema(customRoot), customRoot)
})
