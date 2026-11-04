import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

const defaults = {
  promptContextWindowEnabled: true,
  promptContextWindowMaxMessages: 48,
  promptContextWindowRetainMessages: 24,
  promptContextWindowMaxBytes: 131072,
}

test('context window settings default, validate and preserve values after rejected writes', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { engine, token: 'synthetic-context-settings' })
  const put = (value: unknown, authenticated = true) =>
    app.request('/api/settings', {
      method: 'PUT',
      headers: {
        ...(authenticated ? { Authorization: 'Bearer synthetic-context-settings' } : {}),
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(value),
    })
  try {
    const initial = structuredClone(store.settings)
    for (const [key, value] of Object.entries(defaults)) {
      assert.equal(initial[key as keyof typeof defaults], value)
    }
    assert.equal((await put(initial, false)).status, 401)
    assert.deepEqual(store.settings, initial)
    for (const update of [
      {
        promptContextWindowMaxMessages: 24,
        promptContextWindowRetainMessages: 1,
        promptContextWindowMaxBytes: 8192,
      },
      {
        promptContextWindowMaxMessages: 200,
        promptContextWindowRetainMessages: 100,
        promptContextWindowMaxBytes: 1048576,
      },
      { ...defaults, promptContextWindowEnabled: false },
    ]) {
      const next = { ...store.settings, ...update }
      const response = await put(next)
      assert.equal(response.status, 200)
      assert.deepEqual(await response.json(), next)
      assert.deepEqual(store.settings, next)
    }
    for (const [key, invalids] of Object.entries({
      promptContextWindowEnabled: ['true', 1, null],
      promptContextWindowMaxMessages: [23, 201, 48.5, '48', null],
      promptContextWindowRetainMessages: [0, 101, 24.5, '24', null, 49],
      promptContextWindowMaxBytes: [8191, 1048577, 131072.5, '131072', null],
    })) {
      for (const value of invalids) {
        const before = structuredClone(store.settings)
        assert.equal((await put({ ...before, [key]: value })).status, 400, `${key}: ${value}`)
        assert.deepEqual(store.settings, before)
      }
    }
    const legacy: Record<string, unknown> = { ...initial }
    for (const key of Object.keys(defaults)) delete legacy[key]
    assert.equal((await put(legacy)).status, 200)
    assert.deepEqual(store.settings, initial)
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('context window controls have translations in every supported UI locale', () => {
  for (const locale of ['en', 'zh-CN', 'zh-TW']) {
    const messages = JSON.parse(
      readFileSync(new URL(`../i18n/locales/${locale}.json`, import.meta.url), 'utf8'),
    ) as Record<string, unknown>
    for (const suffix of [
      'Title',
      'Enabled',
      'Help',
      'Messages',
      'MessagesHelp',
      'Retain',
      'RetainHelp',
      'Bytes',
      'BytesHelp',
      'Invalid',
    ]) {
      const key = `stPromptContextWindow${suffix}`
      assert.equal(typeof messages[key], 'string', `${locale}: ${key}`)
      assert((messages[key] as string).trim().length > 0, `${locale}: ${key}`)
    }
  }
})
