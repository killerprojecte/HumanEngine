/** Actual source SecretStore oracle. No files, real keys, user DBs or env inputs. */
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import Database from 'better-sqlite3'
import { SecretStore } from '../../src/secrets/store.ts'
if (process.version !== 'v24.22.1') throw Error('Pinned Node required')
const sources = {
  'src/secrets/store.ts': '489d4cda773e2c7ddc3062823f4e18f54bf818b66757da11015a918a2bb08393',
  'src/memory/recovery/security-state.ts':
    'ca341983f12f39f96f72f51544cc8d7c05d8c40e03b8fc8d2e2ef1d10c3016d8',
  'src/memory/recovery/ledger.ts':
    '4c1a5f6cb400f5bbe636962e3bc332802f58998b291d6a5a05eb21685a6c1192',
}
// Pin every directly relevant source, including readiness and security effects.
for (const [path, hash] of Object.entries(sources))
  if (
    createHash('sha256')
      .update(readFileSync(new URL('../../' + path, import.meta.url)))
      .digest('hex') !== hash
  )
    throw Error('Source changed: ' + path)
const set = (value) => ({ action: 'set', value })
const cases = []
const add = (name, ops, options = {}) => cases.push({ name, ops, ...options })
add('lifecycle', [
  set(' synthetic first '),
  { action: 'preserve' },
  null,
  set('second'),
  { action: 'clear' },
  { action: 'clear' },
])
add('same_value_rotates', [set('same'), set('same')])
add('preserve_missing', [null, { action: 'preserve' }, { action: 'clear' }])
for (const [label, value] of [
  ['empty', ''],
  ['space', ' \t\r\n'],
  ['bom', '\ufeff'],
  ['nbsp', '\u00a0'],
  ['nel', '\u0085'],
  ['180e', '\u180e'],
  ['zero_width', '\u200b'],
  ['astral_limit', '😀'.repeat(8192)],
  ['astral_over', '😀'.repeat(8192) + 'x'],
  ['ascii_limit', 'a'.repeat(16384)],
  ['ascii_over', 'a'.repeat(16385)],
  ['surrogate', '\ud800'],
  ['null', null],
  ['number', 42],
  ['boolean', true],
  ['array', []],
  ['object', {}],
])
  add(label, [set(value)])
for (let cp = 0; cp <= 0xffff; cp++)
  if (
    [9, 10, 11, 12, 13, 32, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff].includes(
      cp,
    ) ||
    (cp >= 0x2000 && cp <= 0x200a)
  )
    add('trim_' + cp, [set(String.fromCharCode(cp))])
add('unknown_action', [{ action: 'bad', value: 'never expose me' }])
add('rollback_insert', [set('never expose me')], { fail: 'insert' })
add('rollback_update', [set('old'), set('never expose me')], { fail: 'update', failAt: 1 })
add('rollback_clear', [set('old'), { action: 'clear' }], { fail: 'delete', failAt: 1 })
add('surrogate_namespace_collision', [set('first'), set('second')], {
  namespace: '\ud800',
  otherNamespace: '\ud801',
})
add('surrogate_name_collision', [set('first'), set('second')], {
  key: '\ud800',
  otherKey: '\ud801',
})
add('missing_security_schema', [set('value')], { absentSecurity: true })
const capture = (fn) => {
  try {
    return { value: fn() }
  } catch (e) {
    return { error: e.message }
  }
}
for (const item of cases) {
  const db = new Database(':memory:')
  const store = new SecretStore(db)
  const ns = item.namespace ?? 'synthetic',
    key = item.key ?? 'key'
  if (!item.absentSecurity)
    db.exec(
      "CREATE TABLE recovery_security_state (id INTEGER PRIMARY KEY CHECK(id=1), revision TEXT NOT NULL); INSERT INTO recovery_security_state VALUES(1,'12345678-1234-4123-8123-123456789012')",
    )
  item.steps = []
  let previous = '12345678-1234-4123-8123-123456789012'
  item.ops.forEach((op, index) => {
    if (item.fail && index === (item.failAt ?? 0))
      db.exec(
        `CREATE TRIGGER fail_secret BEFORE ${item.fail} ON secrets BEGIN SELECT RAISE(ABORT,'fixture_secret_write_failed'); END`,
      )
    const currentNS = index && item.otherNamespace ? item.otherNamespace : ns,
      currentKey = index && item.otherKey ? item.otherKey : key
    const result = capture(() => store.apply(currentNS, currentKey, op))
    const revision = db
      .prepare("SELECT name FROM sqlite_master WHERE name='recovery_security_state'")
      .get()
      ? db.prepare('SELECT revision FROM recovery_security_state').get()?.revision
      : undefined
    const reads = [
      [ns, key],
      [item.otherNamespace ?? ns, item.otherKey ?? key],
      ['\ufffd', key],
      [ns, '\ufffd'],
    ].map(([n, k]) => ({
      namespace: n,
      key: k,
      has: capture(() => store.has(n, k)),
      get: capture(() => store.get(n, k) ?? null),
    }))
    item.steps.push({
      error: result.error ?? '',
      rotated: revision !== previous,
      reads,
      redaction: capture(() => store.redactionValues()),
      count: db.prepare('SELECT count(*) n FROM secrets').get().n,
    })
    previous = revision
  })
  db.close()
}
const output = JSON.stringify({ sources, cases }, null, 2) + '\n'
const target = new URL('../internal/credentials/testdata/secret-store.json', import.meta.url)
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== output) throw Error('Oracle differs')
} else writeFileSync(target, output)
console.log(`${cases.length} actual SecretStore cases verified`)
