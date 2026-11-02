import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { baseCompile } from '@intlify/message-compiler'
import { createI18n } from 'vue-i18n'
import { createSSRApp, h } from 'vue'
import { renderToString } from '@vue/server-renderer'

const locales = ['en', 'zh-CN', 'zh-TW'] as const
const messages = Object.fromEntries(
  locales.map((locale) => [
    locale,
    JSON.parse(readFileSync(new URL(`../i18n/locales/${locale}.json`, import.meta.url), 'utf8')),
  ]),
)
function strings(value: unknown, path = ''): Array<[string, string]> {
  if (typeof value === 'string') return [[path, value]]
  if (!value || typeof value !== 'object') return []
  return Object.entries(value).flatMap(([key, child]) =>
    strings(child, path ? `${path}.${key}` : key),
  )
}

test('every UI translation compiles with the installed Intlify message compiler', () => {
  for (const locale of locales) {
    const entries = strings(messages[locale])
    assert.ok(entries.length > 1000)
    for (const [key, source] of entries) {
      const errors: string[] = []
      baseCompile(source, { onError: (error) => errors.push(`${error.code}: ${error.message}`) })
      assert.deepEqual(errors, [], `${locale}.${key}`)
    }
  }
})

test('settings direct-message help renders its literal mention marker in both Chinese locales', async () => {
  for (const locale of ['zh-CN', 'zh-TW'] as const) {
    const i18n = createI18n({ legacy: false, locale, messages })
    const text = i18n.global.t('stDirectCooldownHelp')
    assert.match(text, locale === 'zh-CN' ? /@机器人/ : /@機器人/)
    assert.doesNotMatch(text, /\{'@'\}/)
    const app = createSSRApp({ render: () => h('p', i18n.global.t('stDirectCooldownHelp')) })
    app.use(i18n)
    assert.equal(await renderToString(app), `<p>${text}</p>`)
    i18n.dispose()
  }
})
