// Invoke from backend/tests after integrating overlay. Actual MemoryService,
// actual Store/revision/lifecycle/authority; synthetic SQLite only. Never models.
import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync, readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { Store } from '../../src/store.ts'
import { MemoryService } from '../../src/memory.ts'
assert.equal(process.version, 'v24.22.1')
const store = new Store(':memory:')
store.saveScene({
  id: 's',
  kind: 'group',
  adapterId: 'adapter',
  accountId: 'account',
  platformSceneId: 's',
  name: 'synthetic',
  participationEnabled: true,
  learningEnabled: true,
  proactiveEnabled: false,
})
const memory = new MemoryService(store)
const ctx = { sceneId: 's', simulation: false, provenance: 'contextual' }
const record = {
  id: 'a',
  scope: { kind: 'scene', sceneId: 's', personId: 'p' },
  kind: 'experience',
  content: 'Synthetic',
  sources: [],
  status: 'active',
  learnedAt: '2000-01-01T00:00:00.000Z',
}
const put = (r) =>
  store.db
    .prepare(
      'INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)',
    )
    .run(r.id, 'scene:s', 'p', r.content, JSON.stringify(r), r.id)
const outcomes = []
const call = (name, fn) => {
  try {
    outcomes.push({ name, value: fn() })
  } catch (e) {
    outcomes.push({ name, error: e.message })
  }
}
try {
  put(record)
  call('legacy_duplicate_score', () =>
    memory.captureParentReferences(ctx, [
      { ...record, score: 42 },
      { ...record, score: 9 },
    ]),
  )
  call('reordered', () =>
    memory.captureParentReferences(ctx, [Object.fromEntries(Object.entries(record).reverse())]),
  )
  call('stale', () => memory.captureExposedReferences(ctx, [{ ...record, content: 'not current' }]))
  call('parent_41', () => memory.captureParentReferences(ctx, Array(41).fill(record)))
  call('exposed_41', () => memory.captureExposedReferences(ctx, Array(41).fill(record)))
  call('simulation_100', () =>
    memory.captureParentReferences({ ...ctx, simulation: true }, Array(100).fill(record)),
  )
  const unsupported = {
    ...record,
    id: 'unsupported',
    lineageStatus: 'verified',
  }
  put(unsupported)
  call('refresh_capture', () => memory.captureExposedReferences(ctx, [unsupported]))
  call('unsupported_row', () =>
    JSON.parse(store.db.prepare('SELECT value FROM memories WHERE id=?').get('unsupported').value),
  )
  call('states', () =>
    store.db.prepare('SELECT * FROM memory_assertion_state ORDER BY assertion_id').all(),
  )
  const sources = [
    'src/memory.ts',
    'src/memory/write/revision-coordinator.ts',
    'src/memory/lifecycle/proof-invalidation.ts',
    'src/memory/semantic/notifications.ts',
    'backend/tests/generate-memory-service-fixtures.mjs',
    'package-lock.json',
  ]
  const hashes = Object.fromEntries(
    sources.map((p) => [
      p,
      createHash('sha256')
        .update(readFileSync(new URL('../../' + p, import.meta.url)))
        .digest('hex'),
    ]),
  )
  const dir = new URL('../internal/memoryservice/testdata/', import.meta.url)
  const target = new URL('source.json', dir)
  const output = JSON.stringify({ hashes, outcomes }, null, 2) + '\n'
  if (process.argv.includes('--check')) {
    assert.equal(readFileSync(target, 'utf8'), output, 'memory service source oracle differs')
  } else {
    mkdirSync(dir, { recursive: true })
    writeFileSync(target, output)
  }
} finally {
  memory.dispose()
  store.close()
}
