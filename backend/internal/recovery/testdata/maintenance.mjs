// Actual source oracle. Synthetic, file-backed state only; no runtime edits.
import Database from 'better-sqlite3'
import { mkdirSync, symlinkSync } from 'node:fs'
import { join } from 'node:path'
import {
  RecoveryAuthority,
  maintenanceGate,
  metadata,
  authorityPath,
  sameHead,
  requireOperationalReader,
} from '../../../../src/memory/recovery/ledger.ts'
const root = process.argv[2]
mkdirSync(join(root, 'stage'))
const database = join(root, 'stage', 'main.sqlite')
const authority = new RecoveryAuthority(
  join(root, 'authority'),
  '12345678-1234-4123-8123-123456789012',
)
const db = new Database(database)
db.exec('CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)')
const gate = {
  head: authority.head(),
  authority: authority.path,
  minimumReader: 1,
  state: 'quarantine',
}
const cases = []
for (const name of [
  'valid',
  'ready',
  'futureReader',
  'wrongInstance',
  'wrongPath',
  'badPrefix',
  'ahead',
  'fraction',
  'numericSpelling',
  'missingSequence',
  'nullSequence',
]) {
  const g = structuredClone(gate)
  if (name === 'ready') g.state = 'ready'
  if (name === 'futureReader') g.minimumReader = 12
  if (name === 'wrongInstance') g.head.instanceId = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
  if (name === 'wrongPath') g.authority += '-other'
  if (name === 'badPrefix') g.head.hash = 'a'.repeat(64)
  if (name === 'ahead') g.head.sequence = 1
  if (name === 'fraction') g.minimumReader = 1.5
  if (name === 'missingSequence') delete g.head.sequence
  if (name === 'nullSequence') g.head.sequence = null
  let raw = JSON.stringify(g)
  if (name === 'numericSpelling')
    raw = raw
      .replace('"sequence":0', '"sequence":0e0')
      .replace('"minimumReader":1', '"minimumReader":1.0')
  db.prepare('INSERT OR REPLACE INTO recovery_gate VALUES(1,?)').run(raw)
  let sourceGate = '',
    combined = ''
  try {
    maintenanceGate(db, database, authority)
  } catch (e) {
    sourceGate = e.message
  }
  combined = sourceGate
  if (!combined) {
    // Exact opening checks from reconcileRecovery, before any domain replay.
    const initial = authority.head(),
      current = metadata(db)
    if (
      !current ||
      current.head.instanceId !== initial.instanceId ||
      current.head.sequence > initial.sequence
    )
      combined = 'recovery_instance_or_head_mismatch'
    else if (!sameHead(current.head, authority.prefix(current.head.sequence)))
      combined = 'recovery_prefix_invalid'
  }
  cases.push({ name, raw, sourceGate, combined })
}
const metadataCases = []
for (const state of ['ready', 'quarantine']) {
  for (const name of [
    'valid',
    'caseFold',
    'duplicateHead',
    'duplicateNullState',
    'caseDistinctExtension',
    'numericSpelling',
    'duplicateSequence',
    'duplicateHeadValid',
    'reader32Overflow',
    'readerWrapToOne',
    'readerSafeMaximum',
    'readerUnsafe',
  ]) {
    let raw = JSON.stringify({ ...gate, state })
    if (name === 'caseFold') raw = raw.replace('"minimumReader":', '"MINIMUMREADER":')
    if (name === 'duplicateHead') raw = raw.slice(0, -1) + ',"head":{"sequence":0}}'
    if (name === 'duplicateNullState') raw = raw.slice(0, -1) + ',"state":null}'
    if (name === 'caseDistinctExtension') raw = raw.slice(0, -1) + ',"State":"other"}'
    if (name === 'numericSpelling')
      raw = raw
        .replace('"sequence":0', '"sequence":0e0')
        .replace('"minimumReader":1', '"minimumReader":1.0')
    if (name === 'duplicateSequence') raw = raw.replace('"sequence":0', '"sequence":1,"sequence":0')
    if (name === 'duplicateHeadValid')
      raw = raw.slice(0, -1) + ',"head":' + JSON.stringify(gate.head) + '}'
    const readerCases = {
      reader32Overflow: '2147483648',
      readerWrapToOne: '4294967297',
      readerSafeMaximum: '9007199254740991',
      readerUnsafe: '9007199254740992',
    }
    if (name in readerCases)
      raw = raw.replace('"minimumReader":1', '"minimumReader":' + readerCases[name])
    db.prepare('INSERT OR REPLACE INTO recovery_gate VALUES(1,?)').run(raw)
    let error = ''
    try {
      metadata(db)
    } catch (e) {
      error = e.message
    }
    metadataCases.push({ name: state + '/' + name, raw, error, state })
  }
}
db.prepare('INSERT OR REPLACE INTO recovery_gate VALUES(1,?)').run(JSON.stringify(gate))
maintenanceGate(db, database, authority)
try {
  db.transaction(() => {
    requireOperationalReader(db)
    throw Error('rollback')
  }).immediate()
} catch {}
const rolledBackReader = metadata(db).minimumReader
db.transaction(() => requireOperationalReader(db)).immediate()
const committedReader = metadata(db).minimumReader
// Real source reader upgrade must preserve distinct UTF-16 extension keys,
// including lone surrogates, equivalent raw key spellings and nested values.
const extensionRaw =
  JSON.stringify(gate).slice(0, -1) +
  String.raw`,"\ud800":"first","\ud801":"second","😀":"old","\ud83d\ude00":"last","ext":{"\udc00":"\udfff","\udc01":2},"dup":1,"\u0064up":2,"large":90071992547409931234}`
db.prepare('INSERT OR REPLACE INTO recovery_gate VALUES(1,?)').run(extensionRaw)
maintenanceGate(db, database, authority)
db.transaction(() => requireOperationalReader(db)).immediate()
const extensionUpgraded = db.prepare('SELECT value FROM recovery_gate WHERE id=1').get().value
const paths = {}
for (const [name, path] of [
  ['contained', join(root, 'stage', 'authority')],
  ['lexicalDots', join(root, 'stage', '..authority')],
  ['independent', authority.path],
]) {
  try {
    authorityPath(database, path)
    paths[name] = ''
  } catch (e) {
    paths[name] = e.message
  }
}
symlinkSync(authority.path, join(root, 'linked'))
try {
  authorityPath(database, join(root, 'linked'))
  paths.symlink = ''
} catch (e) {
  paths.symlink = e.message
}
db.close()
authority.close()
console.log(
  JSON.stringify({
    database,
    authority: gate.authority,
    cases,
    metadataCases,
    extensionRaw,
    extensionUpgraded,
    paths,
    rolledBackReader,
    committedReader,
  }),
)
