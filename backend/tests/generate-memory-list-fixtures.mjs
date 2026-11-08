/** Actual fixed SQL list and AssertionAuthority against synthetic SQLite rows.
 * Stable past/future timestamps avoid pretending Date.now changes SQLite now. */
import assert from 'node:assert/strict'
import Database from 'better-sqlite3'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { readMemoryList } from '../../src/memory/read/list.ts'
assert.equal(process.version, 'v24.22.1')
Date.now = () => 1793188800000
const source = (id = 'm') => ({ sceneId: 's', messageId: id, senderId: 'p' })
const record = (id, extra = {}) => ({
  id,
  scope: { kind: 'scene', sceneId: 's', personId: 'p' },
  kind: 'experience',
  content: `Synthetic ${id}`,
  sources: [source()],
  importance: 'low',
  confidence: 0.5,
  learnedAt: '2000-01-01T00:00:00.000Z',
  status: 'active',
  locked: false,
  origin: 'learned',
  ...extra,
})
const proof = (id = 'm', extra = {}) => ({
  sources: [source(id)],
  parents: [],
  parentReferences: [],
  ...extra,
})
const scenarios = []
function scenario(name, records, options = {}) {
  const db = new Database(':memory:')
  db.exec(
    'CREATE TABLE memories(id TEXT PRIMARY KEY,scope_key TEXT,person_id TEXT,content TEXT,value TEXT,fingerprint TEXT); CREATE TABLE memory_supports(memory_id TEXT PRIMARY KEY,value TEXT); CREATE TABLE recall_tombstones(scene_id TEXT,message_id TEXT); CREATE TABLE memory_assertion_state(assertion_id TEXT PRIMARY KEY,version INTEGER NOT NULL,deletion_epoch INTEGER NOT NULL,deleted INTEGER NOT NULL); CREATE TABLE memory_assertion_revisions(assertion_id TEXT,version INTEGER,scope_key TEXT,recorded_at TEXT,idempotency_key TEXT,payload_hash TEXT,value TEXT,PRIMARY KEY(assertion_id,version))',
  )
  const rows = records.map((r) => ({
    id: r.id,
    scope_key:
      r.scope.kind === 'sharedPublicKnowledge' ? 'shared:public' : `scene:${r.scope.sceneId}`,
    person_id: r.scope.personId ?? null,
    content: r.content,
    value: JSON.stringify(r),
    fingerprint: r.id,
  }))
  for (const r of rows)
    db.prepare(
      'INSERT INTO memories VALUES(@id,@scope_key,@person_id,@content,@value,@fingerprint)',
    ).run(r)
  for (const [id, proofs] of Object.entries(options.proofs ?? {}))
    db.prepare('INSERT INTO memory_supports VALUES(?,?)').run(id, JSON.stringify(proofs))
  for (const id of options.recalled ?? [])
    db.prepare('INSERT INTO recall_tombstones VALUES(?,?)').run('s', id)
  const store = {
    db,
    assertRecoveryReady() {
      if (options.notReady) throw Error('synthetic_not_ready')
    },
    recalled(s, m) {
      return !!db
        .prepare('SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?')
        .get(s, m)
    },
    sourceEvents: {
      registered() {
        return undefined
      },
    },
    scene() {
      return { id: 's', adapterId: 'adapter', accountId: 'account' }
    },
  }
  const ops = options.ops ?? [
    { sceneId: 's', simulation: false },
    { sceneId: 's', simulation: false, personId: 'p' },
    { sceneId: 's', simulation: true },
    { sceneId: 's', simulation: true, personId: 'p' },
    { sceneId: 's', simulation: false, ids: [] },
    { sceneId: 's', simulation: false, ids: ['a', 'a', 'shared', 'absent'] },
  ]
  const results = ops.map((input) => {
    try {
      return { value: readMemoryList(store, input) }
    } catch (e) {
      return { error: e.message }
    }
  })
  scenarios.push({
    name,
    rows,
    proofs: options.proofs ?? {},
    recalled: options.recalled ?? [],
    notReady: options.notReady ?? false,
    ops,
    results,
  })
  db.close()
}
scenario(
  'scene person simulation exact IDs and rowid',
  [
    record('a'),
    record('b', { scope: { kind: 'scene', sceneId: 's', personId: 'q' } }),
    record('shared', {
      scope: { kind: 'sharedPublicKnowledge' },
      observedAt: 'discarded',
      supersedesId: 'discarded',
    }),
    record('other', { scope: { kind: 'scene', sceneId: 'other' } }),
  ],
  {
    ops: [
      { sceneId: 's', simulation: false },
      { sceneId: 's', simulation: false, personId: '' },
      { sceneId: 's', simulation: false, personId: 'p' },
      { sceneId: 's', simulation: true },
      { sceneId: 's', simulation: true, personId: 'p' },
      { sceneId: 's', simulation: true, personId: '' },
      { sceneId: 's', simulation: false, ids: [] },
      { sceneId: 's', simulation: false, ids: ['a', 'a', 'shared', 'absent'] },
      { sceneId: 's', simulation: false, ids: ['b', 'a'] },
    ],
  },
)
scenario('SQL value filters', [
  record('a'),
  record('null-internal', { internalUse: null }),
  record('false-internal', { internalUse: false }),
  record('internal', { internalUse: 'workspace' }),
  record('quarantine', { lineageStatus: 'quarantined' }),
  record('inactive', { status: 'superseded' }),
  record('disputed', { status: 'disputed' }),
  record('future', { validFrom: '2100-01-01' }),
  record('expired', { validUntil: '2000-01-01' }),
  record('invalid-from', { validFrom: 'invalid' }),
  record('invalid-until', { validUntil: 'invalid' }),
  record('null-times', { validFrom: null, validUntil: null }),
  record('valid-window', { validFrom: '2000-01-01', validUntil: '2100-01-01' }),
])
scenario(
  'fresh real proof qualification',
  [
    record('a', { lineageStatus: 'verified' }),
    record('unsupported', { lineageStatus: 'verified' }),
    record('recalled', { lineageStatus: 'verified' }),
    record('alternative', { lineageStatus: 'verified' }),
    record('legacy-recalled', { sources: [source('gone')] }),
  ],
  {
    proofs: {
      a: [proof()],
      recalled: [proof('gone')],
      alternative: [proof('gone'), proof('live')],
    },
    recalled: ['gone'],
  },
)
scenario(
  'invalidated proof and parent graph',
  [
    record('a', { lineageStatus: 'verified' }),
    record('parent'),
    record('child', { lineageStatus: 'verified' }),
    record('missing', { lineageStatus: 'verified' }),
  ],
  {
    proofs: {
      a: [proof('m', { invalidatedBy: 'recall' })],
      child: [{ sources: [source()], parents: ['parent'] }],
      missing: [{ sources: [source()], parents: ['absent'] }],
    },
  },
)
scenario(
  'no prefix cap exact ID',
  [...Array.from({ length: 65 }, (_, i) => record(`r${i}`)), record('quote"id')],
  {
    ops: [
      {
        sceneId: 's',
        simulation: false,
        ids: [...Array.from({ length: 65 }, (_, i) => `r${i}`), 'quote"id', 'r64'],
      },
      { sceneId: 's', simulation: false },
    ],
  },
)
scenario('readiness before simulation short circuit', [], {
  notReady: true,
  ops: [
    { sceneId: 's', simulation: true, personId: 'p' },
    { sceneId: 's', simulation: false, ids: [] },
  ],
})
const files = [
  'src/memory/read/list.ts',
  'src/memory/read/scope.ts',
  'src/memory/read/assertion-authority.ts',
  'src/memory/read/applicability.ts',
  'src/memory/read/provenance.ts',
  'src/memory/storage/assertion-repository.ts',
  'src/memory/storage/assertion-history.ts',
  'src/memory/read/bound-parents.ts',
  'src/memory/read/domain-source-events.ts',
  'src/memory/domain/proofs.ts',
  'src/memory/domain/record-values.ts',
  'src/memory/write/subject-attribution.ts',
  'package-lock.json',
]
const output = {
  sourceNode: process.version,
  nowMillis: Date.now(),
  sources: Object.fromEntries(
    files.map((p) => [p, createHash('sha256').update(readFileSync(p)).digest('hex')]),
  ),
  scenarios,
}
const path = 'backend/internal/memoryread/testdata/source-oracle.json',
  data = JSON.stringify(output, null, 2) + '\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(path, 'utf8'), data)
else {
  mkdirSync('backend/internal/memoryread/testdata', { recursive: true })
  writeFileSync(path, data)
}
console.log(`memory list oracle: ${scenarios.length} actual-source scenarios`)
