/** Actual source schemas/read projections, synthetic data only. Node 24.22.1. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { SettingsSchema, ModelConfigSchema, SceneSchema } from '../../src/schema.ts'
import {
  defaultSettingsInput,
  settingsReadValue,
  modelsReadValue,
} from '../../src/config-read-values.ts'

assert.equal(process.version, 'v24.22.1', 'Use the pinned Node oracle')
const schemas = { Settings: SettingsSchema, ModelConfig: ModelConfigSchema, Scene: SceneSchema }
const settings = defaultSettingsInput()
const models = {
  providers: [
    {
      id: 'provider',
      name: 'Provider',
      protocol: 'openai-chat',
      baseUrl: 'https://example.com/v1',
    },
    { id: 'absent', name: 'Absent', protocol: 'openai-responses', baseUrl: 'https://example.org' },
  ],
  models: [
    {
      id: 'model',
      displayName: 'Model',
      providerId: 'provider',
      apiModelName: 'model',
      capabilities: ['text', 'tools'],
    },
  ],
  routes: [{ role: 'thinker', orderedModelIds: ['model'], timeoutMs: 1000 }],
}
const scene = {
  id: 'scene',
  kind: 'group',
  adapterId: 'adapter',
  accountId: 'account',
  platformSceneId: 'platform',
  name: 'Scene',
  participationEnabled: false,
  learningEnabled: false,
  proactiveEnabled: false,
}
const clone = (value) => JSON.parse(JSON.stringify(value))
const fixtures = []
function add(schema, label, value, wire = false, raw) {
  const input = raw ?? JSON.stringify(value)
  let parsed
  try {
    parsed = schemas[schema].safeParse(JSON.parse(input))
  } catch {
    parsed = { success: false }
  }
  let readOutput
  if (wire && parsed.success) {
    const read =
      schema === 'Settings'
        ? settingsReadValue(JSON.parse(input))
        : schema === 'ModelConfig'
          ? modelsReadValue(JSON.parse(input), (id) => id === 'provider')
          : JSON.parse(input)
    readOutput = JSON.stringify(read)
  }
  fixtures.push({
    schema,
    label,
    input,
    valid: parsed.success,
    ...(parsed.success ? { output: JSON.stringify(parsed.data) } : {}),
    ...(wire ? { wire: true, ...(readOutput ? { readOutput } : {}) } : {}),
  })
}
function set(schema, label, base, path, value, wire = false) {
  const input = clone(base)
  let target = input
  for (const part of path.slice(0, -1)) target = target[part]
  target[path.at(-1)] = value
  add(schema, label, input, wire)
}
add('Settings', 'defaults', settings, true)
add('ModelConfig', 'defaults', models, true)
add('Scene', 'defaults', scene, true)
const strings = [
  ['high-first', '\ud800'],
  ['high-last', '\udbff'],
  ['low-first', '\udc00'],
  ['low-last', '\udfff'],
  ['replacement', '\ufffd'],
  ['pair', '\ud800\udc00'],
  ['emoji', '😀'],
  ['broken-pair', '\ud800x\udc00'],
  ['reverse-pair', '\udc00\ud800'],
  ['two-high', '\ud800\ud801'],
  ['mixed', '<>&\u2028\u2029\ud800\ufffd\udfff😀\u0000\n\t'],
]
for (const [label, value] of strings) {
  for (const field of ['name', 'persona'])
    set('Settings', `${field}-${label}`, settings, [field], value, true)
  for (const [path, field] of [
    [['providers', 0, 'name'], 'provider-name'],
    [['models', 0, 'displayName'], 'display-name'],
    [['models', 0, 'apiModelName'], 'api-model'],
  ])
    set('ModelConfig', `${field}-${label}`, models, path, value, true)
  for (const field of ['accountId', 'platformSceneId', 'name', 'avatarUrl'])
    set('Scene', `${field}-${label}`, scene, [field], value, true)
}
for (const [schema, base, path, max] of [
  ['Settings', settings, ['name'], 100],
  ['Settings', settings, ['persona'], 20000],
  ['ModelConfig', models, ['providers', 0, 'name'], 100],
  ['ModelConfig', models, ['models', 0, 'displayName'], 100],
  ['ModelConfig', models, ['models', 0, 'apiModelName'], 200],
]) {
  for (const [label, unit] of [
    ['ascii', 'a'],
    ['lone', '\ud800'],
    ['pair', '😀'],
    ['replacement', '\ufffd'],
  ]) {
    for (const count of [0, 1, max - 1, max, max + 1]) {
      set(
        schema,
        `${path.join('.')}-${label}-length-${count}`,
        base,
        path,
        unit.repeat(count),
        count >= max,
      )
    }
  }
  set(
    schema,
    `${path.join('.')}-mixed-boundary`,
    base,
    path,
    '😀'.repeat(max - 2) + '\ud800\udc00',
    true,
  )
}
// ASCII-only regex and enum fields must see original strings; no shadow replacement.
for (const suffix of [
  '\ud800',
  '\udc00',
  '\ufffd',
  '😀',
  '\n',
  '\r',
  '\r\n',
  '\u2028',
  '\u2029',
  '\u0000',
]) {
  for (const [path, prefix] of [
    [['providers', 0, 'id'], 'provider'],
    [['models', 0, 'id'], 'model'],
    [['models', 0, 'providerId'], 'provider'],
    [['routes', 0, 'orderedModelIds', 0], 'model'],
    [['providers', 0, 'protocol'], 'openai-chat'],
    [['providers', 0, 'credentialEnv'], 'KEY'],
    [['models', 0, 'capabilities', 0], 'text'],
    [['models', 0, 'promptCacheMode'], 'auto'],
    [['routes', 0, 'role'], 'thinker'],
  ])
    set(
      'ModelConfig',
      `${path.join('.')}-suffix-${JSON.stringify(suffix)}`,
      models,
      path,
      prefix + suffix,
    )
  set(
    'Settings',
    `budget-enum-${JSON.stringify(suffix)}`,
    settings,
    ['budgetMode'],
    'strict' + suffix,
  )
  set('Settings', `timezone-${JSON.stringify(suffix)}`, settings, ['timezone'], 'UTC' + suffix)
  set(
    'Settings',
    `inclination-${JSON.stringify(suffix)}`,
    settings,
    ['participationWindows'],
    [{ startHour: 1, endHour: 2, inclination: 'quiet' + suffix }],
  )
  for (const [field, prefix] of [
    ['id', 'scene'],
    ['adapterId', 'adapter'],
    ['kind', 'group'],
  ])
    set('Scene', `${field}-suffix-${JSON.stringify(suffix)}`, scene, [field], prefix + suffix)
}
// Exact property identity, including keys which a lossy decoder would collide.
for (const [schema, base] of [
  ['Settings', settings],
  ['ModelConfig', models],
  ['Scene', scene],
]) {
  for (const key of [
    '\ud800',
    '\ud801',
    '\udc00',
    '\ufffd',
    'name\ud800',
    '__proto__',
    'constructor',
    'toString',
    '0',
  ]) {
    const value = clone(base)
    Object.defineProperty(value, key, { value: 'property', enumerable: true, configurable: true })
    add(schema, `unknown-key-${JSON.stringify(key)}`, value)
  }
  add(
    schema,
    'distinct-unknown-keys',
    null,
    false,
    JSON.stringify(base).slice(0, -1) + ',"\\ud800":1,"\\ud801":2,"�":3}',
  )
}
for (const key of ['\ud800', '\ufffd', '__proto__', 'constructor']) {
  const value = clone(models)
  Object.defineProperty(value.providers[0], key, { value: 'unknown', enumerable: true })
  add('ModelConfig', `provider-own-key-${JSON.stringify(key)}`, value)
  Object.defineProperty(value.models[0], key, { value: 'unknown', enumerable: true })
  add('ModelConfig', `model-own-key-${JSON.stringify(key)}`, value)
}
// Duplicate known keys and escaped-vs-raw valid pairs have JSON.parse semantics.
add(
  'Settings',
  'duplicate-name-last-lone',
  null,
  true,
  JSON.stringify(settings).slice(0, -1) + ',"name":"\\ud800"}',
)
add(
  'Settings',
  'escaped-property-name',
  null,
  true,
  JSON.stringify(settings).replace('"name"', '"na\\u006de"'),
)
add(
  'Settings',
  'escaped-pair',
  null,
  true,
  JSON.stringify(settings).replace('"HumanEngine"', '"\\ud83d\\ude00"'),
)
for (const url of [
  'https://example.com/\ud800',
  'https://example.com/\udc00',
  'https://example.com/😀',
  '\u00a0HTTP://EXAMPLE.com:80/\ud800\t\udc00\ufeff',
  'https://example.com/\ud800\nend\r\udfff',
  'https://example.com/\ud800\ufffd',
  'https://\ud800.example/path',
  'https://\udc00.example/path',
  'https://\ufffd.example/path',
  'https://😀.example/path',
  'https://%ED%A0%80.example/path',
  'https://example.com/%ED%A0%80',
  'https://\ud800@example.com/',
  'https://example.com/?\ud800',
  'https://example.com/#\ud800',
  '\ud800https://example.com/',
  'https://example.com/\ud800\u2003',
  'https://example.com/?#',
  'http:example.com/\ud800',
  'https://example.com:\ud800/',
  'https://[::1]/\ud800',
  'https://\ud83d\t\ude00.example/path',
  'https://\ud83d\n\ude00.example/path',
  'https://example.com/\ud83d\t\ude00',
  'https://example.com/\ud800\r\udc00',
])
  set('ModelConfig', `url-${JSON.stringify(url)}`, models, ['providers', 0, 'baseUrl'], url, true)
for (const count of [99, 100, 101])
  set(
    'ModelConfig',
    `protocol-length-${count}`,
    models,
    ['providers', 0, 'protocol'],
    'a:' + 'b'.repeat(count - 2),
  )
for (const count of [159, 160, 161]) {
  const value = clone(models)
  value.providers[0].id = value.models[0].providerId = 'a'.repeat(count)
  add('ModelConfig', `id-length-${count}`, value)
}
for (const [label, mutate] of [
  ['duplicate-provider', (v) => v.providers.push(clone(v.providers[0]))],
  ['duplicate-model', (v) => v.models.push(clone(v.models[0]))],
  ['duplicate-role-alias', (v) => v.routes.push({ ...v.routes[0], role: 'replyer' })],
  ['duplicate-route-model', (v) => v.routes[0].orderedModelIds.push('model')],
  ['unknown-provider', (v) => (v.models[0].providerId = 'unknown')],
  ['unknown-route-model', (v) => (v.routes[0].orderedModelIds = ['unknown'])],
  ['duplicate-capability-allowed', (v) => (v.models[0].capabilities = ['text', 'text'])],
  ['same-free-strings-allowed', (v) => (v.providers[0].name = v.providers[1].name = '\ud800')],
  [
    'distinct-free-strings-allowed',
    (v) => {
      v.providers[0].name = '\ud800'
      v.providers[1].name = '\ud801'
    },
  ],
  [
    'credential-presence-overrides-input',
    (v) => {
      v.providers[0].credentialConfigured = false
      v.providers[1].credentialConfigured = true
    },
  ],
]) {
  const value = clone(models)
  mutate(value)
  add('ModelConfig', label, value, true)
}
// Preserve number rejection after moving off encoding/json, which rejects overflow.
for (const [schema, base, old, replacement] of [
  ['Settings', settings, '"cacheHours":24', '"cacheHours":1e999'],
  ['ModelConfig', models, '"timeoutMs":1000', '"timeoutMs":-1e999'],
  [
    'Scene',
    { ...scene, memoryReadRevision: 1 },
    '"memoryReadRevision":1',
    '"memoryReadRevision":1e999',
  ],
])
  add(schema, 'number-overflow', null, true, JSON.stringify(base).replace(old, replacement))
const target = new URL('../internal/contract/testdata/http-strings-node.json', import.meta.url)
const output = JSON.stringify({ node: process.version, fixtures }, null, 2) + '\n'
if (process.argv.includes('--check'))
  assert.equal(readFileSync(target, 'utf8'), output, 'Regenerate HTTP string fixtures')
else writeFileSync(target, output)
console.log(
  `HTTP/schema strings: ${fixtures.length} Node cases (${fixtures.filter((f) => f.wire && (f.schema !== 'Scene' || f.valid)).length} TCP cases)`,
)
