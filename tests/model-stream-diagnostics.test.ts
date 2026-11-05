import assert from 'node:assert/strict'
import test from 'node:test'
import { readStreamingResponse, StreamResponseError } from '../src/models/stream-response.ts'
import { sanitizeError } from '../src/diagnostics/errors.ts'
import { ModelError } from '../src/models.ts'
const secret = 'PRIVATE_STREAM_CANARY bearer-secret conversation-text'
const event = (value: unknown) => `data: ${JSON.stringify(value)}\n\n`
const cases = [
  [
    'chat upstream error',
    'openai-chat',
    event({ error: { message: secret } }),
    'provider_stream_upstream_error',
  ],
  [
    'responses upstream error',
    'openai-responses',
    event({ type: 'response.failed', response: { error: { message: secret } } }),
    'provider_stream_upstream_error',
  ],
  [
    'responses incomplete',
    'openai-responses',
    event({ type: 'response.incomplete', response: { incomplete_details: { reason: secret } } }),
    'provider_stream_upstream_error',
  ],
  [
    'responses on chat route',
    'openai-chat',
    event({ type: 'response.created', response: { id: secret } }),
    'provider_stream_protocol_mismatch',
  ],
  [
    'chat on responses route',
    'openai-responses',
    event({ choices: [], id: secret }),
    'provider_stream_protocol_mismatch',
  ],
  [
    'SSE type mismatch',
    'openai-responses',
    'event: message\n' + event({ type: 'response.created', response: { id: secret } }),
    'provider_stream_event_mismatch',
  ],
  ['invalid JSON', 'openai-chat', `data: ${secret}\n\n`, 'provider_stream_invalid_json'],
  ['invalid structure', 'openai-chat', event({ id: secret }), 'invalid_provider_stream'],
  [
    'early EOF',
    'openai-chat',
    event({ choices: [{ index: 0, delta: {}, finish_reason: null }] }),
    'incomplete_model_response',
  ],
] as const
for (const [name, protocol, input, expected] of cases)
  test(`safe stream diagnostics classify ${name} without exposing provider content`, async () => {
    let progress = 0
    await assert.rejects(
      readStreamingResponse(new Response(input), protocol, () => progress++),
      (error) => {
        assert.ok(error instanceof StreamResponseError)
        assert.equal(error.code, expected)
        const wrapped = new ModelError('provider_stream_invalid', undefined, false, {
          cause: error,
        })
        const diagnostic = sanitizeError(wrapped)
        assert.equal(diagnostic.causes[0]?.errorCode, expected)
        assert.equal(JSON.stringify(diagnostic).includes(secret), false)
        assert.equal(wrapped.retryable, false)
        return true
      },
    )
    assert.equal(progress, 0)
  })

test('decoder and transport errors remain distinct and omit their original unsafe cause', async () => {
  for (const [response, expected] of [
    [new Response(new Uint8Array([0xff])), 'provider_stream_invalid_utf8'],
    [
      new Response(
        new ReadableStream({
          pull() {
            throw new Error(secret)
          },
        }),
      ),
      'provider_stream_transport_error',
    ],
  ] as const) {
    await assert.rejects(
      readStreamingResponse(response, 'openai-chat', () => {}),
      (error) => {
        assert.ok(error instanceof StreamResponseError)
        const diagnostic = sanitizeError(error)
        assert.equal(diagnostic.errorCode, expected)
        assert.equal(JSON.stringify(diagnostic).includes(secret), false)
        assert.equal(error.cause, undefined)
        return true
      },
    )
  }
})
