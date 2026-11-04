import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { parse, compileTemplate } from '@vue/compiler-sfc'

const read = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8')
const settings = read('app/pages/models.vue')
const calls = read('app/pages/model-calls.vue')

// Source-wiring checks complement the rendered synthetic-fixture browser acceptance run.
test('streaming editor defaults omitted values on and preserves explicit false', () => {
  assert.match(settings, /:model-value="nativeStreaming && selectedModel\.streaming !== false"/)
  assert.match(settings, /@update:model-value="selectedModel\.streaming = \$event"/)
  assert.match(settings, /:disabled="!nativeStreaming"/)
  assert.match(settings, /\['openai-chat', 'openai-responses'\]\.includes/)
})

test('call log renders measured TTFT, content kind and transport without inventing zero', () => {
  assert.match(calls, /call\.firstTokenMs == null/)
  assert.match(calls, /number\(call\.firstTokenMs, 1\)/)
  assert.match(calls, /mcallsFirstTokenKind_.*call\.firstTokenKind/)
  assert.match(calls, /mcallsTransport_.*call\.transportMode/)
  assert.doesNotMatch(calls, /t\('mcallsUnavailable'\)/)
  assert.match(calls, /number\(call\.tokensPerSecond, 2\)/)
  assert.match(calls, /mcallsFailure_.*call\.failureReason/)
})

test('both changed Vue templates compile and all new labels exist in every locale', () => {
  for (const [filename, source] of [
    ['models.vue', settings],
    ['model-calls.vue', calls],
  ]) {
    const { descriptor, errors } = parse(source!, { filename })
    assert.deepEqual(errors, [])
    const compiled = compileTemplate({
      source: descriptor.template!.content,
      filename: filename!,
      id: filename!,
    })
    assert.deepEqual(compiled.errors, [])
  }
  for (const locale of ['en', 'zh-CN', 'zh-TW']) {
    const messages = JSON.parse(read(`i18n/locales/${locale}.json`))
    for (const key of [
      'mcStreaming',
      'mcStreamingEnabled',
      'mcStreamingHelp',
      'mcallsTransport',
      'mcallsFirstTokenKind',
      ...['stream', 'non_stream', 'custom', 'unknown'].map((mode) => `mcallsTransport_${mode}`),
      ...['text', 'reasoning', 'tool'].map((kind) => `mcallsFirstTokenKind_${kind}`),
    ]) {
      assert.equal(typeof messages[key], 'string', `${locale}.${key}`)
      assert.ok(messages[key].length > 0)
    }
  }
})
