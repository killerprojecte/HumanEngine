/** Actual authority append validation, including JavaScript string identities. */
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryAuthority } from '../../src/memory/recovery/ledger.ts'
if (process.version !== 'v24.22.1') throw Error('Pinned Node required')
if (process.argv.slice(2).some((arg) => arg !== '--check')) throw Error('Only --check supported')
const hash = 'a'.repeat(64),
  uuid = '12345678-1234-4123-8123-123456789012'
const source = (id) => ({ kind: 'source', sceneId: id, messageId: 'm' })
const changes = (ids) => ({
  kind: 'assertion_eligibility',
  schemaVersion: 1,
  operationId: uuid,
  mode: 'transition',
  changes: ids.map((id) => ({
    id,
    incarnationHash: hash,
    state: { status: 'invalidated', lineageStatus: null },
    materialHash: null,
  })),
})
const flags = {
  participationEnabled: true,
  learningEnabled: false,
  proactiveEnabled: false,
  memoryReadEnabled: true,
  personalizationEnabled: false,
}
const events = [
  source('\ud800'),
  source('\udfff'),
  source('\ufffd'),
  source('\ud800\udc00'),
  source('\udfff\ud800'),
  source('\ud800'.repeat(2000)),
  source('\ud800'.repeat(2001)),
  source('😀'.repeat(1000)),
  source('😀'.repeat(1001)),
  changes(['\ud800', '\udfff', '\ufffd']),
  changes(['\ud800', '\ud800']),
  changes(['😀', '\ud83d\ude00']),
  { kind: 'scene_flags', schemaVersion: 2, sceneId: '\udfff', identityHash: hash, flags },
  { kind: 'habit_delete', schemaVersion: 1, id: '\ud800'.repeat(2000), fingerprint: hash },
  { kind: 'habit_delete', schemaVersion: 1, id: '😀'.repeat(2000), fingerprint: hash },
  { kind: 'habit_delete', schemaVersion: 1, id: '😀'.repeat(2001), fingerprint: hash },
]
const root = mkdtempSync(join(tmpdir(), 'he-lossless-events-'))
let authority
try {
  authority = new RecoveryAuthority(join(root, 'authority'), uuid)
  const cases = events.map((input) => {
    const raw = JSON.stringify(input),
      before = authority.head()
    let accepted = false,
      payload = null,
      after = null
    try {
      const head = authority.append(input, authority.head())
      after = head
      payload = authority.db
        .prepare('SELECT payload FROM revocations WHERE sequence=?')
        .get(head.sequence).payload
      accepted = true
    } catch {}
    return { raw, accepted, payload, before, after }
  })
  const sources = Object.fromEntries(
    [
      'ledger.ts',
      'eligibility-contract.ts',
      'configuration-contract.ts',
      'domain-contracts.ts',
    ].map((name) => [
      name,
      createHash('sha256')
        .update(readFileSync(new URL('../../src/memory/recovery/' + name, import.meta.url)))
        .digest('hex'),
    ]),
  )
  const output = JSON.stringify({ node: process.version, sources, cases }, null, 2) + '\n',
    target = new URL('../internal/recovery/testdata/lossless-events-node.json', import.meta.url)
  if (process.argv.includes('--check')) {
    if (readFileSync(target, 'utf8') !== output) throw Error('Lossless event oracle changed')
  } else writeFileSync(target, output)
  console.log(`${cases.length} lossless authority cases`)
} finally {
  authority?.close()
  rmSync(root, { recursive: true, force: true })
}
