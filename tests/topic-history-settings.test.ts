import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'

const headers = {
  Authorization: 'Bearer synthetic-topic-history-settings',
  'Content-Type': 'application/json',
}

test('topic routing history settings default, validate and preserve stored values on rejected writes', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { engine, token: 'synthetic-topic-history-settings' })
  const put = (value: unknown, authenticated = true) =>
    app.request('/api/settings', {
      method: 'PUT',
      headers: authenticated ? headers : { 'Content-Type': 'application/json' },
      body: JSON.stringify(value),
    })
  try {
    const initial = structuredClone(store.settings)
    assert.equal(initial.topicHistoryMaxMessages, 2048)
    assert.equal((await put({ ...initial, topicHistoryMaxMessages: 500 }, false)).status, 401)
    assert.deepEqual(store.settings, initial)
    for (const limit of [500, 20000]) {
      const response = await put({ ...store.settings, topicHistoryMaxMessages: limit })
      assert.equal(response.status, 200)
      assert.equal((await response.json()).topicHistoryMaxMessages, limit)
      assert.equal(store.settings.topicHistoryMaxMessages, limit)
    }
    for (const invalid of [499, 20001, 500.5, 0, '2048', null]) {
      const before = structuredClone(store.settings)
      assert.equal((await put({ ...before, topicHistoryMaxMessages: invalid })).status, 400)
      assert.deepEqual(store.settings, before)
    }
    const legacy: Record<string, unknown> = { ...initial }
    delete legacy.topicHistoryMaxMessages
    assert.equal((await put(legacy)).status, 200)
    assert.equal(store.settings.topicHistoryMaxMessages, 2048)
  } finally {
    await engine.dispose()
    store.close()
  }
})

test('topic routing resource controls have labels and help in every supported UI locale', () => {
  for (const locale of ['en', 'zh-CN', 'zh-TW']) {
    const messages = JSON.parse(
      readFileSync(new URL(`../i18n/locales/${locale}.json`, import.meta.url), 'utf8'),
    ) as Record<string, unknown>
    for (const key of [
      'stTopicHistoryAdvanced',
      'stTopicHistoryMaxMessages',
      'stTopicHistoryMaxMessagesHelp',
    ]) {
      assert.equal(typeof messages[key], 'string', `${locale}: ${key}`)
      assert((messages[key] as string).trim().length > 0, `${locale}: ${key}`)
    }
  }
})
