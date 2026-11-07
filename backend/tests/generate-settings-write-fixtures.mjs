/** Actual Store.set('settings', value) oracle. Synthetic SQLite only. */
import { createHash } from 'node:crypto'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import Database from 'better-sqlite3'
import { Store } from '../../src/store.ts'
import { metadata } from '../../src/memory/recovery/ledger.ts'
import { ensureSecurityState } from '../../src/memory/recovery/security-state.ts'
if (process.version !== 'v24.22.1') throw Error('Pinned Node 24.22.1 required')
const sources = {
  'src/store.ts': 'a181a5f0c2b5362873662f1f33e79055a284322b2cc7fa28cabbaf6bc6b0ac71',
  'src/budget/settings.ts': '63998711c972aea0fb449d777b8a63c64bc48d9c8215117684e41194e2c6261d',
  'src/memory/recovery/configuration-contract.ts':
    'eb62278c9b7eda4b17e4812a0f6d3495501c27e39428b343395d94fb755117ce',
  'src/memory/recovery/security-state.ts':
    'ca341983f12f39f96f72f51544cc8d7c05d8c40e03b8fc8d2e2ef1d10c3016d8',
  'src/memory/recovery/ledger.ts':
    '4c1a5f6cb400f5bbe636962e3bc332802f58998b291d6a5a05eb21685a6c1192',
}
for (const [path, expected] of Object.entries(sources)) {
  if (
    createHash('sha256')
      .update(readFileSync(new URL('../../' + path, import.meta.url)))
      .digest('hex') !== expected
  )
    throw Error('Settings oracle source changed: ' + path)
}
if (
  JSON.parse(readFileSync(new URL('../../node_modules/zod/package.json', import.meta.url)))
    .version !== '4.6.7'
)
  throw Error('Pinned Zod 4.6.7 required')
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
const initialSecurity = '12345678-1234-4123-8123-123456789012'
const defaults = {
  budgetExperimentEnabled: false,
  budgetMode: 'strict',
  budgetDailyUsd: 1,
  budgetMaxUnresolvedUsd: 0.25,
  budgetPerTurnUsd: 0.1,
  budgetPerTurnTokens: 32768,
  budgetPerTurnCalls: 8,
  budgetMaxOutputTokens: 2048,
  budgetRealtimeRatio: 0.2,
  budgetMaintenanceRatio: 0.1,
}
const cases = []
const add = (name, previous, input, options = {}) =>
  cases.push({ name, previous, input, ...options })
add('create_empty', null, {})
add('null_value', {}, null)
add(
  'same_value',
  { debugModelIO: true, unknown: 'preserve' },
  { debugModelIO: true, unknown: 'preserve' },
)
add('explicit_budget_defaults', {}, defaults)
add(
  'budget_order_not_security_change',
  defaults,
  Object.fromEntries(Object.entries(defaults).reverse()),
)
add(
  'unprojected_change',
  {},
  { apiKey: 'synthetic-not-a-credential', unknown: '\ud800', '\udfff': '<>&\u2028\u2029' },
)
add('raw_order_preserved', {}, JSON.parse('{"b":1,"2":2,"1":1,"a":3,"b":4}'))
add('unknown_nonfinite_serialization', {}, { x: 1e400 })
for (const key of [
  'memoryExperienceFeedbackEnabled',
  'memorySemanticEnabled',
  'memorySemanticAutoAdmissionEnabled',
  'memorySemanticAnswerEnabled',
  'memoryKnowledgeEnabled',
  'memoryKnowledgeAutoAdmissionEnabled',
  'memoryKnowledgeAnswerEnabled',
  'topicAdmissionEnabled',
  'memoryWorkingSetEnabled',
  'memoryEpisodesEnabled',
  'memoryCommitmentsEnabled',
  'memoryCommitmentsAnswerEnabled',
  'memoryHistoricalReadEnabled',
  'sourceEventRevisionsEnabled',
  'cacheEnabled',
  'debugModelIO',
  'memoryProjectionEnabled',
  'memoryGraphEnabled',
  'memoryGraphAutoAdmissionEnabled',
  'memoryGraphAnswerEnabled',
  'memoryGraphShadowEnabled',
  'memoryExperienceEnabled',
  'memoryExperienceAutoActivateEnabled',
  'memoryExperienceHintsEnabled',
]) {
  add('enable_' + key, { [key]: false }, { [key]: true })
  add('disable_' + key, { [key]: true }, { [key]: false })
}
add(
  'all_policy_changes',
  {},
  {
    memoryExperienceFeedbackEnabled: true,
    memoryKnowledgeEnabled: true,
    memorySemanticEnabled: true,
  },
)
for (const [prefix, revision] of [
  ['memoryExperienceFeedback', 'memory-experience-feedback-revision'],
  ['memoryKnowledge', 'memory-knowledge-policy-revision'],
  ['memorySemantic', 'memory-semantic-policy-revision'],
]) {
  for (const value of [null, false, '1', {}, -1, 0.5, 9007199254740991, 9007199254740992]) {
    add(
      'invalid_' + prefix + '_' + JSON.stringify(value),
      {},
      { [prefix + 'Enabled']: true },
      { revisions: { [revision]: JSON.stringify(value) } },
    )
  }
  add(
    'max_increment_' + prefix,
    {},
    { [prefix + 'Enabled']: true },
    { revisions: { [revision]: '9007199254740990' } },
  )
  add('unchanged_invalid_revision_' + prefix, {}, {}, { revisions: { [revision]: 'null' } })
  add('null_false_' + prefix, { [prefix + 'Enabled']: null }, { [prefix + 'Enabled']: false })
  add('nonboolean_' + prefix, { [prefix + 'Enabled']: false }, { [prefix + 'Enabled']: 'true' })
}
add(
  'knowledge_order_matters',
  { memoryKnowledgeEnabled: { a: 1, b: 2 } },
  { memoryKnowledgeEnabled: { b: 2, a: 1 } },
)
add(
  'semantic_lone_surrogate',
  { memorySemanticEnabled: '\ud800' },
  { memorySemanticEnabled: '\ud801' },
)
add(
  'policy_second_revision_invalid_rolls_back_first',
  {},
  { memoryExperienceFeedbackEnabled: true, memoryKnowledgeEnabled: true },
  { revisions: { 'memory-knowledge-policy-revision': '-1' } },
)
for (const [key, value] of Object.entries({
  ...defaults,
  budgetExperimentEnabled: true,
  budgetMode: 'best_effort',
  budgetDailyUsd: 2,
  budgetMaxUnresolvedUsd: 0.3,
  budgetPerTurnUsd: 0.15,
  budgetPerTurnTokens: 1000,
  budgetPerTurnCalls: 4,
  budgetMaxOutputTokens: 1000,
  budgetRealtimeRatio: 0.3,
  budgetMaintenanceRatio: 0.2,
}))
  add('security_' + key, {}, { [key]: value })
add(
  'combined_flags_security',
  {},
  { cacheEnabled: false, memoryKnowledgeEnabled: true, budgetDailyUsd: 2 },
)
for (const value of [
  1,
  'settings',
  true,
  [],
  [1],
  { budgetDailyUsd: null },
  { budgetMode: 'x' },
  { budgetPerTurnCalls: 0.5 },
  { budgetPerTurnTokens: 9007199254740992 },
  { budgetPerTurnTokens: -9007199254740992 },
  { budgetDailyUsd: 0, budgetRealtimeRatio: 0.6, budgetMaintenanceRatio: 0.6 },
  { budgetDailyUsd: 'a', budgetPerTurnCalls: 0.5, budgetRealtimeRatio: 1 },
  { budgetExperimentEnabled: null },
  { budgetDailyUsd: 1e400 },
])
  add('invalid_budget_' + cases.length, {}, value)
for (const [key, minimum, maximum] of [
  ['budgetDailyUsd', 0.000001, 100000],
  ['budgetMaxUnresolvedUsd', 0.000001, 100000],
  ['budgetPerTurnUsd', 0.000001, 100000],
  ['budgetPerTurnTokens', 1, 10000000],
  ['budgetPerTurnCalls', 1, 1000],
  ['budgetMaxOutputTokens', 1, 1000000],
  ['budgetRealtimeRatio', 0, 0.5],
  ['budgetMaintenanceRatio', 0, 0.5],
]) {
  for (const value of [minimum, maximum, minimum - 1, maximum + 1])
    add('budget_boundary_' + key + '_' + value, {}, { [key]: value })
}
for (const rawInput of [
  '1e400',
  '-1e400',
  '{"budgetExperimentEnabled":1e400}',
  '{"budgetDailyUsd":-1e400}',
  '{"budgetPerTurnCalls":1e400}',
  '{"memorySemanticEnabled":1e400}',
  '{"budgetMode":[]}',
  '{"budgetMode":{}}',
])
  add('raw_json_' + rawInput, {}, JSON.parse(rawInput), { rawInput })
add('invalid_budget_after_flag_intent', {}, { memoryKnowledgeEnabled: true, budgetDailyUsd: 0 })
add(
  'invalid_previous_budget_after_flag_intent',
  { budgetMode: 'invalid' },
  { memoryKnowledgeEnabled: true },
)
add('invalid_previous_before_invalid_next', { budgetMode: 'invalid' }, { budgetDailyUsd: null })
add(
  'main_update_failure_after_both_intents',
  {},
  { memoryKnowledgeEnabled: true, budgetDailyUsd: 2 },
  { failSettingsWrite: true },
)
add(
  'source_creates_absent_security_table',
  {},
  { budgetDailyUsd: 2 },
  { absentSecuritySchema: true },
)
const root = mkdtempSync(join(tmpdir(), 'he-settings-write-'))
process.env.HUMANENGINE_LOG_DIR = join(root, 'logs')
delete process.env.HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY
try {
  for (const [index, entry] of cases.entries()) {
    const store = new Store(join(root, String(index), 'data', 'main.sqlite'))
    const ledger = new Database(join(metadata(store.db).authority, 'authority.sqlite'), {
      readonly: true,
    })
    try {
      // The candidate supports a previously established canonical schema. The
      // last case explicitly records the TS lazy-create behavior still missing.
      if (!entry.absentSecuritySchema) {
        ensureSecurityState(store.db)
        store.db.prepare('INSERT INTO recovery_security_state VALUES(1,?)').run(initialSecurity)
      }
      if (entry.previous !== null)
        store.db
          .prepare('INSERT INTO config(key,value) VALUES(?,?)')
          .run('settings', JSON.stringify(entry.previous))
      for (const [key, raw] of Object.entries(entry.revisions ?? {}))
        store.db.prepare('INSERT INTO config(key,value) VALUES(?,?)').run(key, raw)
      if (entry.failSettingsWrite)
        store.db.exec(
          "CREATE TRIGGER fixture_fail_settings BEFORE INSERT ON config WHEN NEW.key='settings' BEGIN SELECT RAISE(ABORT,'fixture_settings_write_failed'); END",
        )
      // rawInput retains valid JSON.parse overflow values that JSON.stringify
      // would otherwise normalize before the Go implementation receives them.
      entry.rawInput ??= JSON.stringify(entry.input)
      if (entry.name === 'unknown_nonfinite_serialization') {
        entry.rawInput = '{"x":1e400}'
      } else if (entry.input?.budgetDailyUsd === Infinity) {
        entry.rawInput = '{"budgetDailyUsd":1e400}'
      }
      try {
        store.set('settings', entry.input)
      } catch (error) {
        entry.error = error.message
      }
      entry.output = store.db.prepare('SELECT key,value FROM config ORDER BY key').all()
      entry.events = ledger
        .prepare('SELECT payload FROM revocations ORDER BY sequence')
        .all()
        .map(({ payload }) => {
          const event = JSON.parse(payload)
          if (event.kind !== 'security_state') return payload
          if (!uuid.test(event.revision)) throw Error('Invalid security UUID')
          return JSON.stringify({ ...event, revision: '<uuid-v4>' })
        })
      const security = store.db
        .prepare("SELECT name FROM sqlite_master WHERE name='recovery_security_state'")
        .get()
      const revision =
        security && store.db.prepare('SELECT revision FROM recovery_security_state').get()?.revision
      entry.security = revision && revision !== initialSecurity ? '<uuid-v4>' : (revision ?? null)
      const gate = metadata(store.db)
      entry.ackSequence = gate.head.sequence
      entry.minimumReader = gate.minimumReader
    } finally {
      ledger.close()
      await store.closeLogs()
      store.close()
    }
  }
  const output = JSON.stringify({ sources, cases }, null, 2) + '\n'
  const target = new URL('../internal/settingsstore/testdata/write.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (readFileSync(target, 'utf8') !== output) throw Error('Settings write fixtures changed')
  } else {
    mkdirSync(new URL('.', target), { recursive: true })
    writeFileSync(target, output)
  }
  console.log(`${cases.length} settings write oracle cases`)
} finally {
  rmSync(root, { recursive: true, force: true })
}
