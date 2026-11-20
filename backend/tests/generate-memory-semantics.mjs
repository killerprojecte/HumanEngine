import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const memory = engine.memory
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const base = { adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic',
  learningEnabled: true, participationEnabled: false, proactiveEnabled: false }
const scenes = [
  { ...base, id: 'local', kind: 'group', platformSceneId: 'local' },
  { ...base, id: 'foreign', kind: 'private', platformSceneId: 'foreign' },
  { ...base, id: 'simulation', kind: 'simulation', platformSceneId: 'simulation' },
]
const seeds = ['former', 'current'].flatMap((personId, group) =>
  Array.from({ length: group === 0 ? 2 : 5 }, (_, i) => {
    const source = { sceneId: 'local', messageId: personId + '-' + i, senderId: personId }
    const content = 'Attribution and original conditions retained: ' + source.messageId
    return { label: '@' + source.messageId, context: { sceneId: 'local', simulation: false,
      provenance: 'source-only', sourceEvidence: [{ ...source, sourceText: content,
        parts: [{ type: 'text', text: content }], available: true, isBot: false }] },
    proposal: { kind: 'fact', content, epistemicKind: 'explicit', sources: [source],
      subject: { kind: 'self', sourceMessageId: source.messageId } } }
  }))
const groups = [
  { person: 'former', ids: ['a-stale'], picks: [[0, 1]] },
  { person: 'current', ids: Array.from({ length: 13 }, (_, i) => 'b-' + String(i).padStart(2, '0')),
    picks: [[0, 1],[0, 2],[0, 3],[0, 4],[1, 2],[1, 3],[1, 4],[2, 3],[2, 4],[3, 4],[0, 1, 2],[0, 1, 3],[0, 1, 4]] },
]
const path = '/api/memory-semantics'
const local = path + '?sceneId=local'
const cases = [
  { name: 'auth-first', path: local + '&wrong=x', authorized: false },
  { name: 'default-prefix', path: local },
  { name: 'raw-prefix-unavailable', path: local + '&limit=0x1' },
  { name: 'qualified-rejected', path: local + '&limit=+2e0' },
  { name: 'all', path: local + '&limit=40' },
  { name: 'head', path: local, method: 'HEAD' },
  { name: 'head-invalid', path: local + '&limit=41', method: 'HEAD' },
  { name: 'limit-bad-int', path: local + '&limit=1.5' },
  { name: 'duplicates', path: local + '&limit=1&limit=2' },
  { name: 'strict', path: local + '&status=admitted' },
  { name: 'required-id', path },
  { name: 'id-max', path: path + '?sceneId=' + encodeURIComponent('🙂'.repeat(512)) },
  { name: 'id-over-max', path: path + '?sceneId=' + encodeURIComponent('🙂'.repeat(513)) },
  { name: 'foreign-private', path: path + '?sceneId=foreign' },
  { name: 'missing', path: path + '?sceneId=missing' },
  { name: 'simulation', path: path + '?sceneId=simulation' },
  { name: 'disabled-even-answer-on', path: local, enabled: false, answer: true },
  { name: 'expired-but-qualified', path: local + '&limit=2', enabled: true, answer: false, expired: true },
  { name: 'after-recall', path: local, recall: true },
]
try {
  for (const scene of scenes) store.saveScene(scene)
  store.set('settings', { ...store.settings, memorySemanticEnabled: true, memorySemanticAnswerEnabled: false })
  const ctx = { sceneId: 'local', simulation: false, provenance: 'contextual' }
  const labels = new Map()
  const active = { former: [], current: [] }
  for (const seed of seeds) {
    const result = memory.propose(seed.context, seed.proposal)
    assert.equal(result.status, 'saved')
    const { id } = result.memory
    labels.set(id, seed.label)
    const { version, deletionEpoch } = memory.revisionState(id)
    const ref = { id, version, deletionEpoch }
    const value = memory.semanticCurrent(ctx, ref)
    assert.ok(value)
    active[seed.proposal.sources[0].senderId].push({ ref, dependencyKey: value.dependencyKey })
  }
  const admittedAt = '2099-02-01T00:00:00.000Z'
  const now = Date.parse(admittedAt)
  memory.semantic.options.now = () => now
  for (const group of groups) {
    const parents = active[group.person]
    for (const [index, selection] of group.picks.entries()) {
      const id = group.ids[index]
      memory.semantic.options.id = () => id
      const result = store.db.transaction(() => memory.semantic.admit(ctx, parents, [{
        kind: 'summary_candidate', selectedParentIds: selection.map((i) => parents[i].ref.id),
        text: 'Synthetic semantic item ' + id, hypothesis: id === 'b-00',
      }]))()
      assert.equal(result.created.length, 1, JSON.stringify(result))
    }
  }
  store.db.transaction(() => memory.semantic.review(ctx, 'b-00', 1, 'rejected'))()
  store.db.prepare('UPDATE memory_assertion_state SET version=version+1 WHERE assertion_id=?').run(active.former[0].ref.id)
  const responses = []
  function normalize(value, key) {
    if (typeof value === 'string') {
      if (key === 'learnedAt') {
        assert.match(value, /^\d{4}-\d\d-\d\dT.*Z$/u)
        return '@learnedAt'
      }
      return labels.get(value) ?? value
    }
    if (Array.isArray(value)) return value.map((v) => normalize(v, ''))
    if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, normalize(v, k)]))
    return value
  }
  for (const entry of cases) {
    if (entry.enabled !== undefined)
      store.set('settings', { ...store.settings, memorySemanticEnabled: entry.enabled, memorySemanticAnswerEnabled: entry.answer })
    const expiryUpdate = store.db.prepare("UPDATE memory_semantic_candidates SET value=json_set(value,'$.applicableUntil',?) WHERE id='b-00'")
    const originalExpiry = entry.expired
      ? store.db.prepare("SELECT json_extract(value,'$.applicableUntil') AS expiry FROM memory_semantic_candidates WHERE id='b-00'").get().expiry
      : undefined
    if (entry.expired) {
      expiryUpdate.run('2000-01-01T00:00:00.000Z')
      memory.semantic.options.now = () => Date.now()
    }
    if (entry.recall) memory.recall('local', 'current-1')
    const response = await app.request('http://fixture.invalid' + entry.path, {
      method: entry.method,
      headers: { Authorization: 'Bearer ' + (entry.authorized === false ? 'wrong-token' : token), 'X-Request-ID': 'synthetic-spoof' },
    })
    assert.match(response.headers.get('x-request-id'), /^[a-f0-9-]{36}$/u)
    const wire = await response.text()
    if (entry.method === 'HEAD') assert.equal(wire, '')
    assert.doesNotMatch(wire, /proofId|fingerprint|dependencyKey|partitionKey|inputKey|[a-f0-9]{64}/u)
    const body = wire ? normalize(JSON.parse(wire), '') : null
    if (entry.expired) {
      expiryUpdate.run(originalExpiry)
      memory.semantic.options.now = () => now
    }
    responses.push({ ...entry, status: response.status, body,
      cacheControl: response.headers.get('cache-control') ?? '',
      noSniff: response.headers.get('x-content-type-options') ?? '' })
  }
  const pick = (name) => responses.find((row) => row.name === name).body
  assert.equal(pick('default-prefix').items.length, 11)
  assert.equal(pick('default-prefix').coverage, 'limited')
  assert.deepEqual(pick('raw-prefix-unavailable'), { enabled: true, coverage: 'limited',
    items: [], unavailable: [{ id: 'a-stale', version: 1, status: 'candidate', reason: 'qualification_unavailable' }] })
  assert.equal(pick('qualified-rejected').items[0].status, 'rejected')
  assert.equal(pick('qualified-rejected').items[0].declarations.length, 5)
  assert.equal(pick('qualified-rejected').items[0].rootCount, 5)
  assert.equal(pick('qualified-rejected').items[0].applicability, 'current')
  assert.equal(pick('all').items.length, 13)
  assert.equal(pick('all').coverage, 'complete')
  assert.deepEqual(pick('disabled-even-answer-on'), { enabled: false, coverage: 'complete', items: [], unavailable: [] })
  assert.equal(pick('expired-but-qualified').items[0].applicability, 'review_needed')
  assert.deepEqual(pick('after-recall').items, [])
  // Pages overlap heavily; store exact wire items once. The Go oracle
  // reconstructs each ordered page before byte-for-byte comparison.
  const items = []
  const seenItems = new Map()
  for (const response of responses) {
    if (!response.body?.items) continue
    response.body.items = response.body.items.map((item) => {
      const key = JSON.stringify(item)
      if (!seenItems.has(key)) {
        seenItems.set(key, items.length)
        items.push(item)
      }
      return seenItems.get(key)
    })
  }
  const sourceHashes = Object.fromEntries([
    'src/api.ts', 'src/memory/interfaces/semantic-routes.ts', 'src/memory/semantic/candidates.ts',
  ].map((file) => [file, createHash('sha256').update(readFileSync(new URL('../../' + file, import.meta.url))).digest('hex')]))
  const target = new URL('../internal/app/testdata/memory-semantics-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    sourceHashes, scenes, seeds, groups, admittedAt, items, cases: responses }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
  console.log(`PASS ${responses.length} actual-source memory semantics GET/HEAD cases`)
} finally {
  engine.dispose()
  store.close()
}
