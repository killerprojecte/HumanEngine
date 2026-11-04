import assert from 'node:assert/strict'
import test from 'node:test'
import {
  readStreamingResponse,
  StreamResponseError,
  type StreamProgressKind,
} from '../src/models/stream-response.ts'

const event = (value: unknown) => `data: ${JSON.stringify(value)}\n\n`
const chat = (delta: unknown, finish_reason: string | null = null) =>
  event({ id: 'chat', choices: [{ index: 0, delta, finish_reason }] })
function stream(text: string, fragment = 7, keepOpen = false) {
  const bytes = new TextEncoder().encode(text)
  let offset = 0,
    cancelled = false
  const body = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (offset < bytes.length) {
        controller.enqueue(bytes.slice(offset, (offset += fragment)))
      } else if (!keepOpen) controller.close()
    },
    cancel() {
      cancelled = true
    },
  })
  return { response: new Response(body), body, cancelled: () => cancelled }
}
async function parse(text: string, protocol: 'openai-chat' | 'openai-responses' = 'openai-chat') {
  const progress: StreamProgressKind[] = [],
    fixture = stream(text)
  const value = await readStreamingResponse(fixture.response, protocol, (kind) =>
    progress.push(kind),
  )
  assert.equal(fixture.body.locked, false)
  return { value, progress }
}

test('Chat SSE reconstructs fragmented UTF8, CRLF/comments, reasoning, parallel tools and final usage', async () => {
  const input =
    ': ping\r\n\r\n' +
    chat({ role: 'assistant', content: null }) +
    chat({ content: '' }) +
    chat({ reasoning_content: '想' }) +
    chat({ content: '你好' }) +
    chat({
      tool_calls: [
        { index: 1, id: 'b', type: 'function', function: { name: 'two', arguments: '{"b":' } },
        { index: 0, id: 'a', type: 'function', function: { name: 'on', arguments: '{"a":' } },
      ],
    }) +
    chat(
      {
        tool_calls: [
          { index: 0, function: { name: 'e', arguments: '1}' } },
          { index: 1, function: { arguments: '2}' } },
        ],
      },
      'tool_calls',
    ) +
    event({ choices: [], usage: { prompt_tokens: 11, completion_tokens: 22, total_tokens: 33 } }) +
    'data: [DONE]\r\n\r\n'
  const { value, progress } = await parse(input)
  assert.deepEqual(progress, ['reasoning', 'text', 'tool', 'tool', 'tool', 'tool'])
  assert.deepEqual(value.usage, { prompt_tokens: 11, completion_tokens: 22, total_tokens: 33 })
  assert.deepEqual((value.choices as any[])[0].message, {
    role: 'assistant',
    content: '你好',
    reasoning_content: '想',
    tool_calls: [
      { id: 'a', type: 'function', function: { name: 'one', arguments: '{"a":1}' } },
      { id: 'b', type: 'function', function: { name: 'two', arguments: '{"b":2}' } },
    ],
  })
})

test('SSE multiline data and terminal cancel an otherwise open body', async () => {
  const fixture = stream(
    'data: {"choices": [\n' +
      'data: {"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n',
    1,
    true,
  )
  await readStreamingResponse(fixture.response, 'openai-chat', () => {})
  assert.equal(fixture.cancelled(), true)
  assert.equal(fixture.body.locked, false)
})

const item = {
  id: 'fc',
  type: 'function_call',
  call_id: 'call',
  name: 'run',
  arguments: '',
  status: 'in_progress',
}
const completedItem = { ...item, arguments: '{"x":1}', status: 'completed' }
const completed = (output: unknown[]) =>
  event({
    type: 'response.completed',
    response: {
      id: 'r',
      status: 'completed',
      output,
      usage: { input_tokens: 2, output_tokens: 3, total_tokens: 5 },
    },
  })
const toolStart =
  event({ type: 'response.created', response: { id: 'r', status: 'in_progress' } }) +
  event({ type: 'response.output_item.added', output_index: 0, item })
const toolDelta = (delta: string) =>
  event({ type: 'response.function_call_arguments.delta', output_index: 0, item_id: 'fc', delta })

test('Responses preserves canonical opaque reasoning and tool continuation with real terminal status', async () => {
  const reasoning = {
    id: 'rs',
    type: 'reasoning',
    summary: [{ type: 'summary_text', text: 'Thinking' }],
    encrypted_content: 'opaque-continuation',
  }
  const { value, progress } = await parse(
    toolStart +
      toolDelta('{"x":') +
      toolDelta('1}') +
      event({ type: 'response.output_item.done', output_index: 0, item: completedItem }) +
      completed([completedItem, reasoning]),
    'openai-responses',
  )
  assert.deepEqual(progress, ['tool', 'tool', 'reasoning'])
  assert.deepEqual(value.output, [completedItem, reasoning])
  assert.deepEqual(value.usage, { input_tokens: 2, output_tokens: 3, total_tokens: 5 })
})

test('Responses content, refusal, summary deltas are meaningful, metadata and empty deltas are not', async () => {
  const message = { id: 'm', type: 'message', role: 'assistant', content: [] }
  const events = [
    { type: 'response.output_item.added', output_index: 0, item: message },
    {
      type: 'response.content_part.added',
      output_index: 0,
      item_id: 'm',
      content_index: 0,
      part: { type: 'output_text', text: '', annotations: [] },
    },
    {
      type: 'response.output_text.delta',
      output_index: 0,
      item_id: 'm',
      content_index: 0,
      delta: '',
    },
    {
      type: 'response.output_text.delta',
      output_index: 0,
      item_id: 'm',
      content_index: 0,
      delta: 'hello',
    },
    {
      type: 'response.content_part.added',
      output_index: 0,
      item_id: 'm',
      content_index: 1,
      part: { type: 'refusal', refusal: '' },
    },
    {
      type: 'response.refusal.delta',
      output_index: 0,
      item_id: 'm',
      content_index: 1,
      delta: 'no',
    },
    {
      type: 'response.output_item.added',
      output_index: 1,
      item: { id: 'rs', type: 'reasoning', summary: [] },
    },
    {
      type: 'response.reasoning_summary_part.added',
      output_index: 1,
      item_id: 'rs',
      summary_index: 0,
      part: { type: 'summary_text', text: '' },
    },
    {
      type: 'response.reasoning_summary_text.delta',
      output_index: 1,
      item_id: 'rs',
      summary_index: 0,
      delta: 'think',
    },
  ]
  const output = [
    {
      ...message,
      content: [
        { type: 'output_text', text: 'hello', annotations: [] },
        { type: 'refusal', refusal: 'no' },
      ],
    },
    { id: 'rs', type: 'reasoning', summary: [{ type: 'summary_text', text: 'think' }] },
  ]
  const result = await parse(events.map(event).join('') + completed(output), 'openai-responses')
  assert.deepEqual(result.progress, ['text', 'text', 'reasoning'])
})

for (const [name, input, protocol] of [
  ['chat truncated before finish', chat({ content: 'partial' }), 'openai-chat'],
  ['chat DONE without finish', chat({ content: 'partial' }) + 'data: [DONE]\n\n', 'openai-chat'],
  ['chat finish without DONE', chat({ content: 'partial' }, 'stop'), 'openai-chat'],
  ['chat token cap', chat({ content: 'partial' }, 'length') + 'data: [DONE]\n\n', 'openai-chat'],
  [
    'chat partial tool JSON',
    chat(
      {
        tool_calls: [
          { index: 0, id: 'a', type: 'function', function: { name: 'tool', arguments: '{' } },
        ],
      },
      'tool_calls',
    ) + 'data: [DONE]\n\n',
    'openai-chat',
  ],
  ['chat raw provider error', event({ error: { message: 'SECRET' } }), 'openai-chat'],
  ['responses no completion', toolStart + toolDelta('{'), 'openai-responses'],
  [
    'responses wrong item identity',
    toolStart + toolDelta('{"x":1}') + completed([{ ...completedItem, id: 'fc-changed' }]),
    'openai-responses',
  ],
  ['responses loses item', toolStart + completed([]), 'openai-responses'],
  [
    'responses loses arguments',
    toolStart + toolDelta('{"x":') + completed([{ ...completedItem, arguments: '{}' }]),
    'openai-responses',
  ],
  [
    'responses partial JSON',
    toolStart + completed([{ ...completedItem, arguments: '{' }]),
    'openai-responses',
  ],
  [
    'responses failed',
    event({ type: 'response.failed', response: { error: { message: 'SECRET' } } }),
    'openai-responses',
  ],
  [
    'responses incomplete',
    event({ type: 'response.incomplete', response: { status: 'incomplete' } }),
    'openai-responses',
  ],
  ['malformed JSON', 'data: SECRET\n\n', 'openai-chat'],
] as const)
  test(`Rejects ${name} safely`, async () => {
    const fixture = stream(input, 99999)
    await assert.rejects(
      readStreamingResponse(fixture.response, protocol, () => {}),
      (error) => {
        assert.ok(error instanceof StreamResponseError)
        assert.match(
          error.message,
          /^(invalid_provider_stream|incomplete_model_response|provider_stream_error)$/,
        )
        assert.equal(error.message.includes('SECRET'), false)
        return true
      },
    )
    assert.equal(fixture.body.locked, false)
  })

test('Bounded total bytes include ignored comments, cancel oversized open body', async () => {
  const fixture = stream(':' + 'x'.repeat(8 * 1024 * 1024), 1024 * 1024, true)
  await assert.rejects(
    readStreamingResponse(fixture.response, 'openai-chat', () => {}),
    /provider_response_too_large/,
  )
  assert.equal(fixture.cancelled(), true)
  assert.equal(fixture.body.locked, false)
})

test('Malformed UTF8 and transport failures are sanitized and release the reader', async () => {
  for (const body of [
    new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new Uint8Array([0xff]))
        c.close()
      },
    }),
    new ReadableStream<Uint8Array>({
      pull(c) {
        c.error(new Error('SECRET'))
      },
    }),
  ]) {
    await assert.rejects(
      readStreamingResponse(new Response(body), 'openai-chat', () => {}),
      /^Error: provider_stream_error$/,
    )
    assert.equal(body.locked, false)
  }
})

test('Responses accepts filled encrypted reasoning placeholders, rejects dropped opaque continuation', async () => {
  const base = { id: 'reason', type: 'reasoning', summary: [], encrypted_content: null }
  const added = event({ type: 'response.output_item.added', output_index: 0, item: base })
  const finalItem = { ...base, encrypted_content: 'opaque' }
  const done = event({ type: 'response.output_item.done', output_index: 0, item: finalItem })
  assert.deepEqual(
    (await parse(added + done + completed([finalItem]), 'openai-responses')).value.output,
    [finalItem],
  )
  await assert.rejects(
    parse(added + done + completed([base]), 'openai-responses'),
    /invalid_provider_stream/,
  )
})

test('Responses done events must agree with deltas and final snapshots', async () => {
  const done = event({
    type: 'response.function_call_arguments.done',
    output_index: 0,
    item_id: 'fc',
    arguments: '{"x":1}',
  })
  await parse(
    toolStart + toolDelta('{"x":') + done + completed([completedItem]),
    'openai-responses',
  )
  await assert.rejects(
    parse(
      toolStart + toolDelta('{"wrong":') + done + completed([completedItem]),
      'openai-responses',
    ),
    /invalid_provider_stream/,
  )
  await assert.rejects(
    parse(
      toolStart + done + completed([{ ...completedItem, arguments: '{"x":2}' }]),
      'openai-responses',
    ),
    /invalid_provider_stream/,
  )
})

test('Response reader honors transport abort without retaining its unsafe cause', async () => {
  let source: ReadableStreamDefaultController<Uint8Array> | undefined
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      source = controller
    },
  })
  const result = readStreamingResponse(new Response(body), 'openai-chat', () => {})
  source!.error(new DOMException('SECRET transport', 'AbortError'))
  await assert.rejects(
    result,
    (error) =>
      error instanceof StreamResponseError &&
      error.message === 'provider_stream_error' &&
      error.cause === undefined,
  )
  assert.equal(body.locked, false)
})

test('Missing or already locked bodies fail with sanitized stable errors', async () => {
  await assert.rejects(
    readStreamingResponse(new Response(null), 'openai-chat', () => {}),
    /incomplete_model_response/,
  )
  const response = new Response('private')
  const owner = response.body!.getReader()
  await assert.rejects(
    readStreamingResponse(response, 'openai-chat', () => {}),
    (error) => error instanceof StreamResponseError && error.message === 'invalid_provider_stream',
  )
  owner.releaseLock()
})

test('Responses nonempty item snapshots count only newly observed text, reasoning and tool arguments', async () => {
  const message = { id: 'm', type: 'message', content: [{ type: 'output_text', text: 'hello' }] }
  const reasoning = {
    id: 'rs',
    type: 'reasoning',
    summary: [{ type: 'summary_text', text: 'think' }],
    encrypted_content: 'opaque',
  }
  const output = [message, reasoning, completedItem]
  const input =
    output
      .map((item, output_index) =>
        event({ type: 'response.output_item.added', output_index, item }),
      )
      .join('') +
    output
      .map((item, output_index) => event({ type: 'response.output_item.done', output_index, item }))
      .join('')
      .repeat(3) +
    completed(output)
  assert.deepEqual((await parse(input, 'openai-responses')).progress, ['text', 'reasoning', 'tool'])
  const onlyDone =
    event({ type: 'response.output_item.done', output_index: 0, item: completedItem }) +
    completed([completedItem])
  assert.deepEqual((await parse(onlyDone, 'openai-responses')).progress, ['tool'])
})

test('Responses nonempty part snapshots and deltas share progress without duplicate done refreshes', async () => {
  const output = [
    { id: 'm', type: 'message', content: [{ type: 'output_text', text: 'hello world!' }] },
  ]
  const base = { output_index: 0, item_id: 'm', content_index: 0 }
  const input =
    event({
      type: 'response.output_item.added',
      output_index: 0,
      item: { ...output[0], content: [] },
    }) +
    event({
      type: 'response.content_part.added',
      ...base,
      part: { type: 'output_text', text: 'hello' },
    }) +
    event({ type: 'response.output_text.delta', ...base, delta: ' world' }) +
    event({ type: 'response.output_text.done', ...base, text: 'hello world' }) +
    event({ type: 'response.content_part.done', ...base, part: output[0]!.content[0] }) +
    event({ type: 'response.content_part.done', ...base, part: output[0]!.content[0] }) +
    completed(output)
  assert.deepEqual((await parse(input, 'openai-responses')).progress, ['text', 'text', 'text'])
})

test('Responses canonical-only completion records its actual first meaningful event, never opaque metadata', async () => {
  const output = [
    { id: 'rs', type: 'reasoning', summary: [], encrypted_content: 'opaque' },
    completedItem,
  ]
  assert.deepEqual((await parse(completed(output), 'openai-responses')).progress, ['tool'])
  assert.deepEqual((await parse(completed([output[0]]), 'openai-responses')).progress, [])
})

test('Chat preserves whitespace but whitespace-only deltas do not signal meaningful progress', async () => {
  const input =
    chat({ content: ' \n', reasoning_content: '\t' }) +
    chat({
      content: 'hello',
      reasoning_content: 'think',
      tool_calls: [
        { index: 0, id: 'a', type: 'function', function: { name: 'run', arguments: ' ' } },
      ],
    }) +
    chat({
      content: '\n',
      reasoning_content: ' ',
      tool_calls: [{ index: 0, function: { arguments: '{' } }],
    }) +
    chat({ tool_calls: [{ index: 0, function: { arguments: '\n' } }] }) +
    chat({ tool_calls: [{ index: 0, function: { arguments: '}' } }] }, 'tool_calls') +
    'data: [DONE]\n\n'
  const { value, progress } = await parse(input)
  assert.deepEqual(progress, ['text', 'reasoning', 'tool', 'tool'])
  const message = (value.choices as Array<{ message: Record<string, unknown> }>)[0]!.message
  assert.equal(message.content, ' \nhello\n')
  assert.equal(message.reasoning_content, '\tthink ')
  assert.deepEqual(message.tool_calls, [
    { id: 'a', type: 'function', function: { name: 'run', arguments: ' {\n}' } },
  ])
})

test('Responses whitespace-only deltas and snapshot growth preserve text without refreshing progress', async () => {
  const message = { id: 'm', type: 'message', content: [{ type: 'output_text', text: ' ' }] }
  const reasoning = { id: 'rs', type: 'reasoning', summary: [{ type: 'summary_text', text: '\t' }] }
  const call = { ...item, arguments: '\n' }
  const output = [
    { ...message, content: [{ type: 'output_text', text: ' hello\n ' }] },
    { ...reasoning, summary: [{ type: 'summary_text', text: '\tthink\n' }] },
    { ...completedItem, arguments: '\n{} \n' },
  ]
  const input =
    [message, reasoning, call]
      .map((item, output_index) =>
        event({ type: 'response.output_item.added', output_index, item }),
      )
      .join('') +
    event({
      type: 'response.output_text.delta',
      output_index: 0,
      item_id: 'm',
      content_index: 0,
      delta: 'hello',
    }) +
    event({
      type: 'response.output_text.delta',
      output_index: 0,
      item_id: 'm',
      content_index: 0,
      delta: '\n',
    }) +
    event({
      type: 'response.reasoning_summary_text.delta',
      output_index: 1,
      item_id: 'rs',
      summary_index: 0,
      delta: 'think',
    }) +
    event({
      type: 'response.function_call_arguments.delta',
      output_index: 2,
      item_id: 'fc',
      delta: '{}',
    }) +
    event({
      type: 'response.function_call_arguments.delta',
      output_index: 2,
      item_id: 'fc',
      delta: ' ',
    }) +
    output
      .map((item, output_index) => event({ type: 'response.output_item.done', output_index, item }))
      .join('')
      .repeat(2) +
    completed(output)
  const result = await parse(input, 'openai-responses')
  assert.deepEqual(result.progress, ['text', 'reasoning', 'tool'])
  assert.deepEqual(result.value.output, output)
  assert.deepEqual((await parse(completed([message, reasoning]), 'openai-responses')).progress, [])
})
