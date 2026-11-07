/** Synthetic read-validation oracle. Run with pinned Node 24.22.1 and --import tsx. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryAuthority } from '../../src/memory/recovery/ledger.ts'
import { parseExternalCostRecoveryEvent } from '../../src/memory/recovery/external-cost-contract.ts'
import { parseExperienceTrialRecoveryEvent } from '../../src/memory/recovery/experience-trial-contract.ts'
import { parseSemanticRecoveryEvent } from '../../src/memory/recovery/semantic-contract.ts'
import { parseIdentityRecoveryEvent } from '../../src/memory/recovery/identity-contract.ts'
import { parseWorkingSetPinRecoveryEvent } from '../../src/memory/recovery/working-set-contract.ts'
import { parseCommitmentRecoveryEvent } from '../../src/memory/recovery/commitment-contract.ts'

assert.equal(process.version, 'v24.22.1', 'Use the pinned Node oracle')
assert.ok(
  process.argv.slice(2).every((arg) => arg === '--check'),
  'Only --check is supported',
)
const H = 'a'.repeat(64),
  I = '12345678-1234-4123-8123-123456789012',
  MAX = Number.MAX_SAFE_INTEGER
const clone = (value) => JSON.parse(JSON.stringify(value))
const parsers = {
  external_cost_receipt: parseExternalCostRecoveryEvent,
  experience_trial: parseExperienceTrialRecoveryEvent,
  semantic_mutation: parseSemanticRecoveryEvent,
  identity_mutation: parseIdentityRecoveryEvent,
  working_set_pin: parseWorkingSetPinRecoveryEvent,
  commitment_head: parseCommitmentRecoveryEvent,
  commitment_delete: parseCommitmentRecoveryEvent,
}
const receipt = {
  kind: 'external_cost_receipt',
  receipt: {
    schemaVersion: 1,
    revision: 1,
    receipt: {
      id: 'r',
      category: 'tool',
      currency: 'USD',
      billingDate: '2024-02-29',
      periodStart: '2024-02-01',
      periodEnd: '2024-02-29',
      sourceId: 'source',
      status: 'known',
      amountNanoUsd: 0,
    },
    correctionId: null,
    recordedAt: '2024-02-29T01:02:03.456Z',
  },
}
const trial = {
  kind: 'experience_trial',
  schemaVersion: 1,
  operation: 'start',
  policyId: 'p',
  sceneId: 's',
  expectedPolicyVersion: 1,
  nextPolicyVersion: 1,
  expectedReceiptVersion: null,
  nextReceiptVersion: 1,
  beforeHash: null,
  afterHash: H,
  nextPhase: 'trial',
}
const semantic = {
  kind: 'semantic_mutation',
  schemaVersion: 1,
  operation: 'admit',
  id: 'i',
  sceneId: 's',
  previousVersion: null,
  nextVersion: 1,
  beforeHash: null,
  afterHash: H,
  nextState: { status: 'candidate', admission: 'qualified_candidate' },
}
const endpoint = { adapterId: 'adapter', accountId: 'account', personId: 'person' }
const source = { sceneId: 's', messageId: 'm' }
const identity = {
  kind: 'identity_mutation',
  schemaVersion: 1,
  operation: 'alias_put',
  operationId: 'o',
  expectedRevision: 0,
  nextRevision: 1,
  beforeHash: H,
  qualificationHash: H,
  targets: { endpoints: [endpoint], aliasIds: ['alias'], source },
}
const pin = {
  kind: 'working_set_pin',
  schemaVersion: 1,
  policy: {
    schemaVersion: 1,
    sceneId: 's',
    selection: {
      ref: { kind: 'assertion', id: 'i', version: 1, deletionEpoch: 0 },
      dependencyKey: H,
    },
    policyRevision: 1,
    pinned: true,
    terminal: false,
  },
}
const head = {
  id: 'i',
  sceneId: 's',
  latestVersion: 2,
  activeVersion: 1,
  everActivatedVersion: 2,
  everTerminal: { version: 1, state: 'done' },
  deletionEpoch: 0,
  mutationVersion: 3,
  deleted: false,
}
const commitment = {
  kind: 'commitment_head',
  schemaVersion: 2,
  sceneId: 's',
  id: 'i',
  deletionEpoch: 0,
  termsVersion: 2,
  mutationVersion: 3,
  qualificationHash: H,
  temporalVersion: 1,
  privacyMarker: { recordedAt: '2024-02-29T01:02:03+23:59', head, reason: 'purge' },
}
const deletion = {
  kind: 'commitment_delete',
  schemaVersion: 1,
  sceneId: 's',
  id: 'i',
  deletionEpoch: 0,
}
const inputs = [],
  seen = new Set()
function add(label, value, raw = JSON.stringify(value)) {
  if (seen.has(raw)) return
  seen.add(raw)
  inputs.push({ label, raw })
}
function change(
  base,
  path,
  value,
  label = `${base.kind}:${path.join('.')}:${JSON.stringify(value)}`,
  remove = false,
) {
  const input = clone(base)
  let target = input
  for (const key of path.slice(0, -1)) target = target[key]
  if (remove) delete target[path.at(-1)]
  else
    Object.defineProperty(target, path.at(-1), {
      value,
      writable: true,
      enumerable: true,
      configurable: true,
    })
  add(label, input)
}
const bases = [receipt, trial, semantic, identity, pin, commitment, deletion]
// At every schema object boundary: omission, null, wrong primitive/collection,
// unknown keys, safe integer edges, and source-specific string identities.
function mutations(base, object = base, path = []) {
  for (const extra of ['extra', '__proto__', '\ud800', '\ufffd'])
    change(
      base,
      [...path, extra],
      true,
      `${base.kind}:${path.join('.')}:extra-${JSON.stringify(extra)}`,
    )
  for (const [key, value] of Object.entries(object)) {
    const at = [...path, key]
    change(base, at, null, `${base.kind}:${at.join('.')}:omitted`, true)
    const replacements = [null, false, true, 0, -1, 1.5, MAX, MAX + 1, '', 'unknown', {}, []]
    for (const replacement of replacements) change(base, at, replacement)
    if (value && typeof value === 'object' && !Array.isArray(value)) mutations(base, value, at)
    if (Array.isArray(value) && value[0] && typeof value[0] === 'object')
      mutations(base, value[0], [...at, 0])
  }
}
for (const base of bases) {
  add(`${base.kind}:base`, base)
  mutations(base)
  add(
    `${base.kind}:raw-reordered`,
    base,
    JSON.stringify(Object.fromEntries(Object.entries(base).reverse())),
  )
  add(`${base.kind}:raw-escaped`, base, JSON.stringify(base).replaceAll('"s"', '"\\u0073"'))
}
// Branches and exact transition relations. Null/missing are intentionally distinct.
for (const operation of ['start', 'collect', 'promote', 'suspend', 'expire', 'erase']) {
  const base = {
    ...trial,
    operation,
    expectedReceiptVersion: operation === 'start' ? null : 1,
    nextReceiptVersion: operation === 'erase' ? null : operation === 'start' ? 1 : 2,
    beforeHash: operation === 'start' ? null : H,
    afterHash: operation === 'erase' ? null : H,
    nextPolicyVersion: operation === 'promote' ? 2 : 1,
  }
  if (operation === 'erase') delete base.nextPhase
  else
    base.nextPhase =
      { promote: 'promoted', suspend: 'suspended', expire: 'expired' }[operation] ?? 'trial'
  add(`trial:${operation}`, base)
  for (const phase of [undefined, null, 'trial', 'promoted', 'suspended', 'expired'])
    change(base, ['nextPhase'], phase, `trial:${operation}:phase:${phase}`, phase === undefined)
  for (const reason of [
    undefined,
    null,
    'explicit_instruction_trial',
    'user_reported_helpfulness',
    'negative_feedback',
    'expiry',
    'qualification_changed',
    'instruction_withdrawn',
    'bad',
  ])
    change(
      base,
      ['nextReason'],
      reason,
      `trial:${operation}:reason:${reason}`,
      reason === undefined,
    )
  for (const key of [
    'expectedPolicyVersion',
    'nextPolicyVersion',
    'expectedReceiptVersion',
    'nextReceiptVersion',
  ])
    for (const value of [null, 0, 1, 2, 3, MAX, MAX + 1])
      change(base, [key], value, `trial:${operation}:${key}:${value}`)
  for (const key of ['beforeHash', 'afterHash'])
    for (const value of [null, H]) change(base, [key], value, `trial:${operation}:${key}:${value}`)
}
for (const operation of ['admit', 'review', 'privacy', 'attempt']) {
  for (const previousVersion of [null, 1, MAX]) {
    for (const nextVersion of [null, 1, 2, MAX]) {
      const base = {
        ...semantic,
        operation,
        id: operation === 'attempt' ? H : 'i',
        previousVersion,
        nextVersion,
        beforeHash: previousVersion === null ? null : H,
        afterHash: nextVersion === null ? null : H,
      }
      delete base.nextState
      for (const nextState of [
        undefined,
        null,
        ...['candidate', 'admitted', 'rejected'].flatMap((status) =>
          ['qualified_candidate', 'structural_low_risk_summary', 'administrator_review'].map(
            (admission) => ({ status, admission }),
          ),
        ),
      ]) {
        add(
          `semantic:${operation}:${previousVersion}:${nextVersion}:${JSON.stringify(nextState)}`,
          { ...base, ...(nextState === undefined ? {} : { nextState }) },
        )
      }
    }
  }
}
for (const operation of ['link', 'unlink', 'split', 'alias_put', 'alias_remove', 'source_recall']) {
  for (const endpointCount of [0, 1, 2, 32, 33]) {
    for (const aliasCount of [0, 1, 2]) {
      for (const withSource of [false, true]) {
        add(`identity:${operation}:${endpointCount}:${aliasCount}:${withSource}`, {
          ...identity,
          operation,
          targets: {
            endpoints: Array(endpointCount).fill(endpoint),
            aliasIds: Array(aliasCount).fill('alias'),
            ...(withSource ? { source } : {}),
          },
        })
      }
    }
  }
}
for (const length of [4096, 4097])
  add(`identity:alias-limit:${length}`, {
    ...identity,
    operation: 'source_recall',
    targets: { endpoints: [], aliasIds: Array(length).fill('alias'), source },
  })
for (const kind of ['assertion', 'commitment', 'other']) {
  for (const pinned of [false, true])
    for (const terminal of [false, true]) {
      const input = clone(pin)
      input.policy.pinned = pinned
      input.policy.terminal = terminal
      input.policy.selection.ref.kind = kind
      if (kind === 'commitment') input.policy.selection.ref.mutationVersion = 1
      add(`pin:${kind}:${pinned}:${terminal}`, input)
      for (const version of [undefined, null, 0, 1, MAX, MAX + 1])
        change(
          input,
          ['policy', 'selection', 'ref', 'mutationVersion'],
          version,
          `pin:${kind}:${pinned}:${terminal}:mutation:${version}`,
          version === undefined,
        )
    }
}
for (const schemaVersion of [1, 2, 3])
  for (const temporalVersion of [undefined, null, 1, 2])
    for (const withMarker of [false, true]) {
      const input = { ...commitment, schemaVersion }
      delete input.temporalVersion
      if (temporalVersion !== undefined) input.temporalVersion = temporalVersion
      if (!withMarker) delete input.privacyMarker
      add(`commitment:versions:${schemaVersion}:${temporalVersion}:${withMarker}`, input)
    }
for (const field of ['activeVersion', 'everActivatedVersion', 'everTerminal'])
  change(commitment, ['privacyMarker', 'head', field], null)
for (const field of ['id', 'sceneId', 'latestVersion', 'mutationVersion', 'deletionEpoch']) {
  change(
    commitment,
    ['privacyMarker', 'head', field],
    typeof head[field] === 'number' ? head[field] + 1 : head[field] + 'different',
  )
  change(
    commitment,
    [{ latestVersion: 'termsVersion' }[field] ?? field],
    typeof head[field] === 'number' ? head[field] + 1 : head[field] + 'different',
  )
}
for (const field of ['activeVersion', 'everActivatedVersion'])
  for (const version of [1, 2, 3]) change(commitment, ['privacyMarker', 'head', field], version)
for (const version of [1, 2, 3])
  for (const state of ['done', 'cancelled', 'pending'])
    change(commitment, ['privacyMarker', 'head', 'everTerminal'], { version, state })
change(commitment, ['privacyMarker', 'reason'], 'requalify')
// WTF-8 identity must survive comparisons. Equal pairs are accepted, distinct
// lone surrogates/replacement characters never collapse into the same identifier.
for (const text of ['\ud800', '\udfff', '\ufffd', '😀', '\udfff\ud800', '\u0000']) {
  const input = clone(commitment)
  input.id = input.privacyMarker.head.id = text
  input.sceneId = input.privacyMarker.head.sceneId = text
  add(`commitment:identity-equal:${JSON.stringify(text)}`, input)
  for (const other of ['\ud800', '\udfff', '\ufffd'])
    change(input, ['privacyMarker', 'head', 'id'], other)
}
const idPaths = [
  [trial, ['policyId'], 512],
  [trial, ['sceneId'], 512],
  [semantic, ['id'], 512],
  [semantic, ['sceneId'], 512],
  [identity, ['operationId'], 160],
  [identity, ['targets', 'endpoints', 0, 'adapterId'], 160],
  [identity, ['targets', 'endpoints', 0, 'accountId'], 160],
  [identity, ['targets', 'endpoints', 0, 'personId'], 160],
  [identity, ['targets', 'aliasIds', 0], 160],
  [identity, ['targets', 'source', 'sceneId'], 160],
  [identity, ['targets', 'source', 'messageId'], 160],
  [pin, ['policy', 'sceneId'], 512],
  [pin, ['policy', 'selection', 'ref', 'id'], 512],
  [deletion, ['id'], 512],
  [deletion, ['sceneId'], 512],
]
for (const [base, path, max] of idPaths)
  for (const unit of ['a', '😀', '\ud800', '\udfff'])
    for (const length of [1, max, max + 1])
      change(
        base,
        path,
        unit.repeat(length),
        `${base.kind}:${path.join('.')}:length:${JSON.stringify(unit)}:${length}`,
      )
for (const revision of [1, 2, 1000, 1001])
  for (const correctionId of [null, 'correction'])
    add(`receipt:revision:${revision}:${correctionId}`, {
      ...receipt,
      receipt: { ...receipt.receipt, revision, correctionId },
    })
for (const status of ['known', 'unknown'])
  for (const amount of [undefined, null, 0, -1, 1.5, MAX, MAX + 1]) {
    const input = clone(receipt)
    input.receipt.receipt.status = status
    delete input.receipt.receipt.amountNanoUsd
    if (amount !== undefined) input.receipt.receipt.amountNanoUsd = amount
    add(`receipt:${status}:${amount}`, input)
  }
for (const category of ['tool', 'storage', 'compute'])
  change(receipt, ['receipt', 'receipt', 'category'], category)
for (const day of [
  '0000-02-29',
  '0001-01-01',
  '1900-02-29',
  '2000-02-29',
  '2024-02-29',
  '2023-02-29',
  '2024-04-31',
  '9999-12-31',
  '2024-01-00',
  '2024-13-01',
  '2024-1-01',
  '2024-01-01\n',
]) {
  change(receipt, ['receipt', 'receipt', 'billingDate'], day)
  change(receipt, ['receipt', 'receipt', 'periodStart'], day)
  change(receipt, ['receipt', 'receipt', 'periodEnd'], day)
}
for (const instant of [
  '0000-02-29T00:00:00Z',
  '1900-02-29T00:00:00Z',
  '2000-02-29T00:00:00Z',
  '2024-02-29T01:02Z',
  '2024-02-29T01:02:03Z',
  '2024-02-29T01:02:03.12345678901234567890Z',
  '2024-02-29T01:02:03+00:00',
  '2024-02-29T01:02:03-23:59',
  '2024-02-29T01:02:03+24:00',
  '2024-02-29T01:02:03+01:60',
  '2024-02-29T24:00:00Z',
  '2024-02-29T01:02:60Z',
  '2024-02-29t01:02:03z',
  '2024-02-29T01:02:03Z\n',
  '2024-02-29T01:02:03',
]) {
  change(receipt, ['receipt', 'recordedAt'], instant)
  change(commitment, ['privacyMarker', 'recordedAt'], instant)
}
for (const suffix of ['\n', '\r', '\r\n', '\u2028', '\u2029', '\ud800', '\ufffd']) {
  change(semantic, ['afterHash'], H + suffix)
  change(receipt, ['receipt', 'receipt', 'id'], 'id' + suffix)
}
for (const value of ['a'.repeat(160), 'a'.repeat(161), '-', '_', '😀', ['id'], ['a'.repeat(64)]])
  change(receipt, ['receipt', 'receipt', 'id'], value)
// Coordinated safe-integer maxima must remain accepted, not merely reject
// overflowing or mutually inconsistent versions.
add('trial:max-collect', {
  ...trial,
  operation: 'collect',
  expectedPolicyVersion: MAX,
  nextPolicyVersion: MAX,
  expectedReceiptVersion: MAX - 1,
  nextReceiptVersion: MAX,
  beforeHash: H,
})
add('trial:max-promote', {
  ...trial,
  operation: 'promote',
  expectedPolicyVersion: MAX - 1,
  nextPolicyVersion: MAX,
  expectedReceiptVersion: MAX - 1,
  nextReceiptVersion: MAX,
  beforeHash: H,
  nextPhase: 'promoted',
})
add('trial:max-erase', {
  ...trial,
  operation: 'erase',
  expectedPolicyVersion: MAX,
  nextPolicyVersion: MAX,
  expectedReceiptVersion: MAX,
  nextReceiptVersion: null,
  beforeHash: H,
  afterHash: null,
  nextPhase: undefined,
})
for (const operation of ['review', 'privacy', 'attempt']) {
  add(`semantic:max-${operation}`, {
    ...semantic,
    operation,
    id: operation === 'attempt' ? H : 'i',
    previousVersion: MAX - 1,
    nextVersion: MAX,
    beforeHash: H,
    nextState:
      operation === 'review'
        ? { status: 'admitted', admission: 'administrator_review' }
        : undefined,
  })
}
add('identity:max-revision', { ...identity, expectedRevision: MAX - 1, nextRevision: MAX })
const maxPin = clone(pin)
maxPin.policy.policyRevision = MAX
Object.assign(maxPin.policy.selection.ref, {
  kind: 'commitment',
  version: MAX,
  deletionEpoch: MAX,
  mutationVersion: MAX,
})
add('pin:max-counters', maxPin)
const maxCommitment = clone(commitment)
Object.assign(maxCommitment, { termsVersion: MAX, mutationVersion: MAX, deletionEpoch: MAX })
Object.assign(maxCommitment.privacyMarker.head, {
  latestVersion: MAX,
  activeVersion: MAX,
  everActivatedVersion: MAX,
  everTerminal: { version: MAX, state: 'done' },
  mutationVersion: MAX,
  deletionEpoch: MAX,
})
add('commitment:max-counters', maxCommitment)
for (const unit of ['a', '😀', '\\ud800', '\\udfff']) {
  // Evaluate escaped lone-surrogate literals as JavaScript strings.
  const text = unit.startsWith('\\u') ? JSON.parse('"' + unit + '"') : unit
  for (const count of [512, 513]) {
    const input = clone(commitment)
    input.id =
      input.sceneId =
      input.privacyMarker.head.id =
      input.privacyMarker.head.sceneId =
        text.repeat(count)
    add(`commitment:matching-id-boundary:${JSON.stringify(text)}:${count}`, input)
  }
}
// Historical lexical spelling is not canonicalized by the reader. Last-key-wins
// and negative zero are JSON.parse behavior, not additional schema rules.
add(
  'delete:raw-negative-zero',
  deletion,
  JSON.stringify(deletion).replace('\"deletionEpoch\":0', '\"deletionEpoch\":-0'),
)
add(
  'delete:raw-duplicate-last-valid',
  deletion,
  '{\"id\":null,' + JSON.stringify(deletion).slice(1),
)
add(
  'delete:raw-duplicate-last-invalid',
  deletion,
  JSON.stringify(deletion).slice(0, -1) + ',\"id\":null}',
)
add(
  'delete:raw-escaped-key',
  deletion,
  JSON.stringify(deletion).replace('\"sceneId\"', '\"\\u0073ceneId\"'),
)
add('unknown-kind', { kind: 'future_unknown', schemaVersion: 1 })
add('unknown-version', {
  kind: 'commitment_delete',
  schemaVersion: 99,
  sceneId: 's',
  id: 'i',
  deletionEpoch: 0,
})
for (const value of [null, [], {}, 1, true, 'event'])
  add(`not-event:${JSON.stringify(value)}`, value)

const directory = mkdtempSync(join(tmpdir(), 'he-authority-domain-oracle-'))
let authority
try {
  authority = new RecoveryAuthority(join(directory, 'authority'), I)
  const cases = [],
    rows = []
  let previous = authority.head()
  for (const entry of inputs) {
    const input = JSON.parse(entry.raw)
    let parsed,
      accepted = false,
      parserAccepted = false
    try {
      parsed = parsers[input?.kind]?.(input)
      parserAccepted = parsed !== undefined
    } catch {}
    let next
    try {
      next = authority.append(input, previous, true)
      accepted = true
    } catch (error) {
      if (!String(error.message).startsWith('recovery_')) throw error
    }
    assert.equal(accepted, parserAccepted, `Parser/authority disagree: ${entry.label}`)
    cases.push({ ...entry, accepted })
    if (accepted) {
      const row = authority.db
        .prepare(
          'SELECT sequence, previous_hash AS previousHash, payload, hash FROM main.revocations WHERE sequence=?',
        )
        .get(next.sequence)
      assert.equal(row.payload, JSON.stringify(parsed))
      rows.push(row)
      previous = next
    }
  }
  // Actual authority read validation of noncanonical stored bytes, separately
  // from append's schema output serialization. Nothing re-encodes these rows.
  authority.db.transaction(() => {
    for (const entry of cases.filter((entry) => entry.accepted && /raw-/.test(entry.label))) {
      const sequence = previous.sequence + 1,
        payload = ' \n' + entry.raw + '\t ',
        previousHash = previous.hash
      const hash = createHash('sha256')
        .update(`${I}|${sequence}|${previousHash}|${payload}`)
        .digest('hex')
      authority.db
        .prepare('INSERT INTO main.revocations VALUES(?,?,?,?)')
        .run(sequence, previousHash, payload, hash)
      rows.push({ sequence, previousHash, payload, hash })
      previous = { instanceId: I, sequence, hash }
    }
  })()
  assert.equal(
    authority.entries().length,
    rows.length,
    'Actual Node must reopen/audit the entire mixed-domain history',
  )
  authority.close()
  authority = new RecoveryAuthority(join(directory, 'authority'))
  assert.deepEqual(authority.head(), previous)
  const sources = Object.fromEntries(
    [
      'src/memory/recovery/ledger.ts',
      'src/memory/recovery/external-cost-contract.ts',
      'src/budget/external-cost/contracts.ts',
      'src/memory/recovery/experience-trial-contract.ts',
      'src/memory/recovery/semantic-contract.ts',
      'src/memory/recovery/identity-contract.ts',
      'src/memory/identities/contracts.ts',
      'src/memory/recovery/working-set-contract.ts',
      'src/memory/working-set/contracts.ts',
      'src/memory/recovery/commitment-contract.ts',
      'src/memory/commitments/temporal-contracts.ts',
      'src/memory/commitments/storage.ts',
      'package-lock.json',
      'node_modules/zod/package.json',
      'node_modules/zod/v4/core/checks.js',
      'node_modules/zod/v4/core/schemas.js',
      'node_modules/zod/v4/core/regexes.js',
      'node_modules/zod/v4/core/util.js',
    ].map((path) => [path, createHash('sha256').update(readFileSync(path)).digest('hex')]),
  )
  const output =
    JSON.stringify(
      { node: process.version, sources, instanceId: I, head: previous, cases, rows },
      null,
      2,
    ) + '\n'
  const target = new URL(
    '../internal/recovery/testdata/authority-domains-node.json',
    import.meta.url,
  )
  if (process.argv.includes('--check'))
    assert.equal(
      readFileSync(target, 'utf8'),
      output,
      'Authority domain fixtures are stale; regenerate explicitly',
    )
  else writeFileSync(target, output)
  console.log(
    `Verified ${cases.length} authority validation cases and ${rows.length} accepted history rows`,
  )
} finally {
  authority?.close()
  rmSync(directory, { recursive: true, force: true })
}
