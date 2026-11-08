import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'
const root = process.env.RECONCILIATION_ROOT || process.cwd()
const outputRoot = process.env.RECONCILIATION_OUTPUT || root
const Database = createRequire(resolve(root, 'package.json'))('better-sqlite3')
const load = (p) => import(pathToFileURL(resolve(root, p)))
const { ReconciliationPolicy } = await load('src/memory/write/reconciliation-policy.ts')
const { AssertionRepository } = await load('src/memory/storage/assertion-repository.ts')
const { fingerprint } = await load('src/memory/domain/record-values.ts')
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
Date.now = () => 1793188800000
const scope = { kind: 'scene', sceneId: 's', personId: 'p' }
const context = { sceneId: 's', provenance: 'contextual' }
const source = { sceneId: 's', messageId: 'm', senderId: 'p' }
const candidate = {
  content: 'Synthetic fact',
  kind: 'claim',
  epistemicKind: 'reported',
  personId: 'p',
}
const attribution = { witness: { kind: 'mentioned' } }
const proof = {
  sources: [source],
  parents: [],
  subjectWitness: { kind: 'mentioned' },
}
const record = {
  id: 'old',
  scope,
  content: candidate.content,
  kind: 'claim',
  epistemicKind: 'reported',
  origin: 'learned',
  status: 'active',
  sources: [source],
}
const row = (r, fp = fingerprint(r.scope, r.content)) => ({
  id: r.id,
  scope_key: r.scope.kind === 'scene' ? `scene:${r.scope.sceneId}` : 'sharedPublicKnowledge',
  person_id: r.scope.personId ?? null,
  content: r.content,
  fingerprint: fp,
  value: JSON.stringify(r),
})
const cases = []
const add = (name, patch = {}) => {
  const input = {
    ctx: context,
    candidate,
    scope,
    attribution,
    parents: [],
    rows: [],
    proofs: {},
    deleted: [],
    recalled: [],
    ...patch,
  }
  const db = new Database(':memory:')
  db.exec(
    'CREATE TABLE memories(id TEXT PRIMARY KEY,scope_key TEXT,person_id TEXT,content TEXT,fingerprint TEXT UNIQUE,value TEXT);CREATE TABLE memory_supports(memory_id TEXT PRIMARY KEY,value TEXT);CREATE TABLE memory_deleted(fingerprint TEXT PRIMARY KEY,deleted_at TEXT);CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT)',
  )
  for (const r of input.rows)
    db.prepare(
      'INSERT INTO memories VALUES(@id,@scope_key,@person_id,@content,@fingerprint,@value)',
    ).run(r)
  for (const [id, proofs] of Object.entries(input.proofs))
    db.prepare('INSERT INTO memory_supports VALUES(?,?)').run(id, JSON.stringify(proofs))
  for (const fp of input.deleted)
    db.prepare('INSERT INTO memory_deleted VALUES(?,?)').run(fp, 'now')
  for (const s of input.recalled)
    db.prepare('INSERT INTO recall_tombstones VALUES(?,?)').run(s.sceneId, s.messageId)
  const store = {
    db,
    assertRecoveryReady() {},
    recalled: (s, m) =>
      !!db.prepare('SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?').get(s, m),
  }
  const repo = new AssertionRepository(store, {
    beforeWrite() {},
    removeProjection() {},
  })
  const policy = new ReconciliationPolicy(store, repo)
  const expected = policy.inspect(
    input.ctx,
    input.candidate,
    input.scope,
    input.attribution,
    input.parents,
  )
  cases.push({
    name,
    input: JSON.stringify(input),
    expected: JSON.stringify(expected),
  })
  db.close()
}
add('new')
for (const v of [null, '', false, 0])
  add(`invalid-merge-${v}`, { candidate: { ...candidate, mergeIntoId: v } })
for (const v of [null, '', 'old'])
  add(`merge-and-supersede-${v}`, {
    candidate: { ...candidate, mergeIntoId: 'old', supersedesId: v },
  })
add('manual-deletion-before-target', {
  candidate: { ...candidate, mergeIntoId: 'missing' },
  deleted: [fingerprint(scope, candidate.content)],
})
const existing = { rows: [row(record)], proofs: { old: [proof] } }
add('compatible-existing', existing)
add('existing-no-proofs-protected', { rows: [row(record)] })
for (const kind of ['fact', 'claim'])
  for (const ep of ['reported', 'inferred', 'hypothesis', 'observed', undefined])
    add(`protection-${kind}-${ep}`, {
      ...existing,
      rows: [row({ ...record, kind, epistemicKind: ep })],
      candidate: { ...candidate, epistemicKind: ep },
    })
for (const witness of [
  undefined,
  { kind: 'self' },
  { kind: 'mentioned' },
  { kind: 'general' },
  null,
])
  add(`proof-witness-${JSON.stringify(witness)}`, {
    ...existing,
    proofs: { old: [{ ...proof, subjectWitness: witness }] },
  })
add('invalidated-proof-ignored', {
  ...existing,
  proofs: {
    old: [
      { ...proof, invalidatedBy: 'x' },
      { ...proof, subjectWitness: { kind: 'self' }, invalidatedBy: 'x' },
      proof,
    ],
  },
})
add('all-invalidated-protected', {
  ...existing,
  proofs: { old: [{ ...proof, invalidatedBy: 'x' }] },
})
add('empty-proof-sources', {
  ...existing,
  proofs: { old: [{ ...proof, sources: [] }] },
})
add('recalled-proof-protected', { ...existing, recalled: [source] })
add('surviving-empty-proof', {
  ...existing,
  proofs: { old: [proof, { ...proof, sources: [] }] },
  recalled: [source],
})
add('incompatible-epistemic-before-merge', {
  ...existing,
  candidate: { ...candidate, mergeIntoId: 'old', epistemicKind: 'inferred' },
})
add('incompatible-self-proof', {
  ...existing,
  attribution: { witness: { kind: 'self' } },
})
add('both-witness-absent', {
  ...existing,
  attribution: {},
  proofs: { old: [{ sources: [] }] },
})
add('undefined-versus-null-epistemic', {
  ...existing,
  attribution: {},
  rows: [row({ ...record, epistemicKind: undefined })],
  proofs: { old: [] },
  candidate: { ...candidate, epistemicKind: null },
})
const merge = { ...existing, candidate: { ...candidate, mergeIntoId: 'old' } }
add('merge-accepted', merge)
add('merge-missing', {
  candidate: { ...candidate, mergeIntoId: 'missing' },
  attribution: {},
})
for (const [name, patch] of Object.entries({
  locked: { locked: true },
  manual: { origin: 'manual' },
  disputed: { status: 'disputed' },
  superseded: { status: 'superseded' },
  foreign: { scope: { ...scope, sceneId: 'foreign' } },
  person: { scope: { ...scope, personId: 'other' } },
  public: { scope: { kind: 'sharedPublicKnowledge' } },
}))
  add(`merge-denied-${name}`, {
    ...merge,
    rows: [row({ ...record, ...patch })],
  })
for (const date of [
  'invalid',
  '2026-10-28T12:00:00.000Z',
  '2026-10-28T12:00:00.001Z',
  '2020-01-01',
  '2030-01-01',
  '2026-02-30',
  null,
  1,
  [],
  ['2030-01-01'],
])
  add(`merge-date-${JSON.stringify(date)}`, {
    ...merge,
    rows: [row({ ...record, validUntil: date })],
  })
add('merge-recalled-record-only', {
  ...merge,
  proofs: { old: [{ ...proof, sources: [] }] },
  recalled: [source],
})
add('merge-stored-fingerprint-deleted', {
  ...merge,
  rows: [row(record, 'history:stored')],
  deleted: ['history:stored'],
})
add('merge-content-fingerprint-not-stored', {
  ...merge,
  rows: [row(record, 'history:stored')],
  ctx: { ...context, provenance: 'source-only' },
})
add('source-only-exact-normalized', {
  ...merge,
  candidate: { ...merge.candidate, content: '  SYNTHETIC   FACT  ' },
  ctx: { ...context, provenance: 'source-only' },
})
add('source-only-mismatch-before-locked', {
  ...merge,
  candidate: { ...merge.candidate, content: 'Different' },
  rows: [row({ ...record, locked: true })],
  ctx: { ...context, provenance: 'source-only' },
})
const correction = {
  ...existing,
  candidate: { ...candidate, content: 'Updated fact', supersedesId: 'old' },
}
add('correction-accepted', correction)
for (const [name, patch] of Object.entries({
  locked: { locked: true },
  manual: { origin: 'manual' },
  disputed: { status: 'disputed' },
  superseded: { status: 'superseded' },
  foreign: { scope: { ...scope, sceneId: 'foreign' } },
  person: { scope: { ...scope, personId: 'other' } },
}))
  add(`correction-${name}`, {
    ...correction,
    rows: [row({ ...record, ...patch })],
  })
add('correction-missing', { candidate: correction.candidate, attribution: {} })
add('correction-missing-mentioned', { candidate: correction.candidate })
add('correction-same-row', {
  ...existing,
  candidate: { ...candidate, supersedesId: 'old' },
})
add('correction-depends-on-old', { ...correction, parents: ['old'] })
add('correction-compares-candidate-person', {
  ...correction,
  scope: { ...scope, personId: 'different' },
})
add('correction-expired-and-recalled-allowed', {
  ...correction,
  attribution: {},
  rows: [row({ ...record, validUntil: '2000-01-01' })],
  recalled: [source],
})
for (const sources of [[], [source], [source, { ...source, sceneId: 'foreign' }]])
  for (const status of ['active', 'disputed'])
    add(`public-correction-${sources.length}-${status}`, {
      ...correction,
      rows: [
        row({
          ...record,
          scope: { kind: 'sharedPublicKnowledge' },
          sources,
          status,
        }),
      ],
    })
for (const v of [null, '', false, 0])
  add(`falsy-correction-${v}`, {
    candidate: { ...candidate, supersedesId: v },
  })
add('protected-existing-before-correction', {
  ...existing,
  candidate: { ...candidate, supersedesId: 'missing' },
  rows: [row({ ...record, kind: 'fact' })],
})
// Strict optional equality must not collapse missing person/witness fields into null.
for (const priorPerson of [undefined, null]) {
  for (const incomingPerson of [undefined, null]) {
    const targetScope = { ...scope, personId: priorPerson }
    const incomingScope = { ...scope, personId: incomingPerson }
    const tag = `${priorPerson}-${incomingPerson}`
    add(`merge-optional-person-${tag}`, {
      ...merge,
      scope: incomingScope,
      candidate: { ...merge.candidate, personId: incomingPerson },
      rows: [row({ ...record, scope: targetScope })],
    })
    add(`correction-optional-person-${tag}`, {
      ...correction,
      scope: incomingScope,
      candidate: { ...correction.candidate, personId: incomingPerson },
      rows: [row({ ...record, scope: targetScope })],
    })
  }
}
for (const priorKind of [undefined, null]) {
  for (const incomingKind of [undefined, null]) {
    add(`optional-witness-kind-${priorKind}-${incomingKind}`, {
      ...existing,
      attribution: { witness: { kind: incomingKind } },
      proofs: { old: [{ ...proof, subjectWitness: { kind: priorKind } }] },
    })
  }
}
const files = [
  'src/memory/write/reconciliation-policy.ts',
  'src/memory/storage/assertion-repository.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/domain/proofs.ts',
  'package-lock.json',
  'backend/tests/generate-reconciliation-fixtures.mjs',
]
const sources = Object.fromEntries(
  files.map((p) => [
    p,
    createHash('sha256')
      .update(
        readFileSync(p.startsWith('backend/tests/') ? new URL(import.meta.url) : resolve(root, p)),
      )
      .digest('hex'),
  ]),
)
const raw =
  JSON.stringify({ node: process.version, timezone: 'UTC', sources, cases }, null, 2) + '\n'
const path = resolve(outputRoot, 'backend/internal/reconciliation/testdata/reconciliation.json')
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), raw)
else {
  mkdirSync(resolve(path, '..'), { recursive: true })
  writeFileSync(path, raw)
}
console.log(`${cases.length} actual-source reconciliation cases`)
