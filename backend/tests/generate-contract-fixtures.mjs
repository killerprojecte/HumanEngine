/** Regenerate synthetic schema fixtures using the legacy implementation as oracle. */
import { readFileSync, writeFileSync } from 'node:fs'
import { ProviderSchema, ModelConfigSchema } from '../../src/schema.ts'
const target = new URL('../internal/contract/testdata/', import.meta.url)
const urls = JSON.parse(readFileSync(new URL('provider-urls-node.json', target), 'utf8'))
for (const input of [
  '\u00a0https://example.com\ufeff',
  '\u0085https://example.com\u0085',
  '\u2003https://exa\nmple.com/v1\u2029',
  '\ufeffhttps://example.com/\tpath',
])
  if (!urls.some((f) => f.input === input)) urls.push({ input })
writeFileSync(
  new URL('provider-urls-node.json', target),
  JSON.stringify(
    urls.map(({ input }) => {
      try {
        const parsed = ProviderSchema.safeParse({
          id: 'fixture',
          name: 'fixture',
          protocol: 'openai-chat',
          baseUrl: input,
        })
        return {
          input,
          valid: parsed.success,
          ...(parsed.success ? { output: parsed.data.baseUrl } : {}),
        }
      } catch {
        return { input, valid: false }
      }
    }),
    null,
    2,
  ) + '\n',
)
const base = {
  providers: [
    { id: 'provider', name: 'Fixture', protocol: 'openai-chat', baseUrl: 'https://example.com/v1' },
  ],
  models: [
    {
      id: 'model',
      displayName: 'Fixture',
      providerId: 'provider',
      apiModelName: 'fixture',
      capabilities: ['text', 'tools'],
    },
  ],
  routes: [{ role: 'thinker', orderedModelIds: ['model'], timeoutMs: 1000 }],
}
const cases = [
  { ...base, providers: [{ ...base.providers[0], name: '😀'.repeat(101) }] },
  { ...base, providers: [{ ...base.providers[0], name: '😀'.repeat(50) }] },
  { ...base, providers: [{ ...base.providers[0], name: '😀'.repeat(51) }] },
  base,
  { ...base, providers: [...base.providers, ...base.providers] },
  { ...base, models: [...base.models, ...base.models] },
  { ...base, routes: [...base.routes, { ...base.routes[0], role: 'replyer' }] },
  { ...base, models: [{ ...base.models[0], providerId: 'absent' }] },
  { ...base, routes: [{ ...base.routes[0], orderedModelIds: ['absent'] }] },
  { ...base, routes: [{ ...base.routes[0], orderedModelIds: ['model', 'model'] }] },
  { ...base, unexpected: true },
  {
    ...base,
    providers: [
      { ...base.providers[0], credentialConfigured: true, imageUploadLimits: { maxWidth: 256 } },
    ],
  },
  { ...base, models: [{ ...base.models[0], capabilities: [] }] },
  { ...base, providers: [{ ...base.providers[0], retryCount: null }] },
]
writeFileSync(
  new URL('models-node.json', target),
  JSON.stringify(
    cases.map((input) => {
      const parsed = ModelConfigSchema.safeParse(input)
      return { input, valid: parsed.success, ...(parsed.success ? { output: parsed.data } : {}) }
    }),
    null,
    2,
  ) + '\n',
)
