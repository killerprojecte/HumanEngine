// Run from repository root: node --experimental-transform-types backend/internal/recovery/testdata/generate-write-events-node.mjs [--check]
// Synthetic byte/hash oracle from the actual TypeScript ledger, not a second implementation.
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryAuthority } from '../../../../src/memory/recovery/ledger.ts'
import {
  configurationKeys,
  sceneFlagKeys,
} from '../../../../src/memory/recovery/configuration-contract.ts'
const args = process.argv.slice(2)
if (args.length > 1 || (args.length === 1 && args[0] !== '--check')) {
  throw new Error('Usage: generate-write-events-node.mjs [--check]')
}
const check = args[0] === '--check'
const hash = 'a'.repeat(64)
const uuid = '12345678-1234-4123-8123-123456789012'
const config = Object.fromEntries(configurationKeys.map((key) => [key, false]))
const flags = Object.fromEntries(sceneFlagKeys.map((key) => [key, false]))
const events = [
  { kind: 'source', sceneId: '<>&\u2028\u2029', messageId: '\\u2028' },
  { kind: 'fingerprint', value: hash },
  { kind: 'assertion', id: 'id', throughVersion: Number.MAX_SAFE_INTEGER, terminal: true },
  { kind: 'security_state', revision: uuid },
  {
    kind: 'source_edit_head',
    schemaVersion: 1,
    sceneId: 's',
    messageId: 'm',
    eventId: 'e',
    revision: 1,
    deletionEpoch: 0,
    identityHash: hash,
    headHash: hash,
  },
  {
    kind: 'assertion_eligibility',
    schemaVersion: 1,
    operationId: uuid,
    mode: 'transition',
    changes: [
      {
        id: 'a',
        incarnationHash: hash,
        state: { status: 'invalidated', lineageStatus: null },
        materialHash: null,
      },
    ],
  },
  { kind: 'configuration_flags', schemaVersion: 8, flags: config },
  { kind: 'scene_flags', schemaVersion: 2, sceneId: 's', identityHash: hash, flags },
  { kind: 'habit_delete', schemaVersion: 1, id: 'h', fingerprint: hash },
  {
    kind: 'meme_delete',
    schemaVersion: 1,
    id: hash,
    contentHash: hash,
    qualificationHash: hash,
    format: 'png',
  },
  {
    kind: 'meme_state',
    schemaVersion: 1,
    id: hash,
    contentHash: hash,
    qualificationHash: hash,
    state: {
      available: true,
      disabled: false,
      knownOnly: false,
      shared: true,
      privacyReviewed: true,
    },
  },
  { kind: 'experience_delete', schemaVersion: 1, sceneId: 's', id: 'e' },
  {
    kind: 'experience_state',
    schemaVersion: 1,
    sceneId: 's',
    id: 'e',
    qualificationHash: hash,
    version: 1,
    status: 'active',
    validationId: 'v',
    activationBasis: 'administrator_review',
    reason: 'experimental_mechanical_hint',
  },
]
const directory = mkdtempSync(join(tmpdir(), 'go-authority-write-oracle-'))
let authority
try {
  authority = new RecoveryAuthority(join(directory, 'authority'), uuid)
  const rows = []
  for (const input of events) {
    const before = authority.head()
    const after = authority.append(input, before)
    const payload = authority.db
      .prepare('SELECT payload FROM revocations WHERE sequence=?')
      .get(after.sequence).payload
    const repeated = authority.append(input, after)
    const forced = authority.append(input, repeated, true)
    rows.push({ input, payload, before, after, repeated, forced })
  }
  const sources = Object.fromEntries(
    [
      'ledger.ts',
      'domain-contracts.ts',
      'configuration-contract.ts',
      'source-event-contract.ts',
      'eligibility-contract.ts',
    ].map((file) => {
      const path = 'src/memory/recovery/' + file
      return [path, createHash('sha256').update(readFileSync(path)).digest('hex')]
    }),
  )
  const file = new URL('write-events-node.json', import.meta.url)
  const generated = JSON.stringify({ node: process.version, sources, rows }, null, 2) + '\n'
  if (check) {
    if (readFileSync(file, 'utf8') !== generated)
      throw new Error('Authority write fixtures are stale; regenerate explicitly')
    console.log(`Verified ${rows.length} authority write fixtures`)
  } else {
    writeFileSync(file, generated)
  }
} finally {
  authority?.close()
  rmSync(directory, { recursive: true, force: true })
}
