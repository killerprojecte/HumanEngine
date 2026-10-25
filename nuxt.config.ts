export default defineNuxtConfig({
  compatibilityDate: '2026-10-25',
  modules: ['@nuxt/ui', '@nuxtjs/i18n'],
  css: ['~/assets/css/main.css'],
  devtools: { enabled: false },
  ui: { fonts: false },
  nitro: { externals: { external: ['better-sqlite3', 'sqlite-vec'] } },
  i18n: { defaultLocale: 'en', strategy: 'no_prefix', locales: [
    { code: 'en', language: 'en', name: 'English', file: 'en.json' },
    { code: 'zh-CN', language: 'zh-CN', name: 'Simplified Chinese', file: 'zh-CN.json' },
    { code: 'zh-TW', language: 'zh-TW', name: 'Traditional Chinese', file: 'zh-TW.json' },
  ] },
  app: { head: { title: 'HumanEngine', meta: [{name:'viewport',content:'width=device-width, initial-scale=1'}] } },
})
