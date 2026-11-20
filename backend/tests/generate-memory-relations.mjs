import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const times = { createdAt: '2025-02-01T00:00:00.000Z', reviewedAt: '2025-02-02T00:00:00.000Z' }
const base = { adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic',
  learningEnabled: true, participationEnabled: false, proactiveEnabled: false }
const scenes = [
  { ...base, id: 'local', kind: 'group', platformSceneId: 'local' },
  { ...base, id: 'foreign', kind: 'private', platformSceneId: 'foreign' },
  { ...base, id: 'simulation', kind: 'simulation', platformSceneId: 'simulation' },
]
const names = {
  'identity:local:subject': { senderDisplayName: ' \u202aLocal subject\u202c\n ', senderPlatformId: 'PRIVATE_PLATFORM' },
  'identity:local:speaker': { senderDisplayName: 'Reporting speaker' },
  'identity:local:nameless': { senderDisplayName: '\u2066\t\u2069' },
  'identity:foreign:subject': { senderDisplayName: 'Foreign subject' },
}
const makeProposal = (i, sceneId, personId, senderId, auto) => {
  const source = { sceneId, messageId: 'message-' + i, senderId }
  const subject = personId === senderId ? { kind: 'self', sourceMessageId: source.messageId }
    : { kind: 'mentioned', personId, sourceMessageId: source.messageId, mentionPartIndex: 2 }
  const context = { sceneId, simulation: false, provenance: 'source-only', botUserId: 'bot',
    sourceEvidence: [{ ...source, available: true, isBot: false,
      ...(subject.kind === 'mentioned' ? { mentions: [{ partIndex: 2, userId: personId }] } : {}) }] }
  const candidate = { kind: 'claim', content: 'Synthetic assertion ' + i, epistemicKind: 'explicit',
    subject, sources: [source], relations: [{ predicate: 'participates_in', personId,
      target: { kind: 'new', entityKind: i === 1 ? 'event' : 'plan', title: 'Synthetic title ' + i },
      polarity: i === 1 ? 'negative' : 'positive', modality: i === 1 ? 'possible' : 'asserted',
      temporalScope: i === 1 ? 'past' : 'current', conditions: i === 1 ? ['Only if the day changes'] : [],
      ...(i === 1 ? { occurredAt: '2025-01-01T00:00:00Z', validFrom: '2025-01-01T00:00:00Z' } : {}) }] }
  return { context, candidate, ids: ['entity-' + i, 'relation-' + i], auto }
}
const proposals = [
  makeProposal(0, 'local', 'subject', 'subject', true),
  makeProposal(1, 'local', 'subject', 'speaker', false),
  makeProposal(2, 'local', 'nameless', 'nameless', false),
  makeProposal(3, 'foreign', 'subject', 'subject', false),
]
const path = '/api/memory-relations'
const local = path + '?sceneId=local'
const cases = [
  ['unauthorized', local + '&unknown=1', { authorized: false }],
  ['unauthorized-head', local, { authorized: false, method: 'HEAD' }],
  ['listing', local],
  ['head', local, { method: 'HEAD' }],
  ['head-invalid', local + '&limit=41', { method: 'HEAD' }],
  ['person', local + '&personId=subject'],
  ['person-case-sensitive', local + '&personId=Subject'],
  ['candidate', local + '&status=candidate'],
  ['accepted', local + '&status=accepted'],
  ['rejected', local + '&status=rejected'],
  ['limited', local + '&limit=0x1'],
  ['limit-exponent', local + '&limit=+2e0'],
  ['hidden-backing-keeps-graph-coverage', local + '&limit=1', { hide: true }],
  ['visible-backing-restored', local + '&limit=1', { hide: false }],
  ['missing-required', path],
  ['empty-person', local + '&personId='],
  ['strict', local + '&unknown=1'],
  ['duplicate', local + '&sceneId=foreign'],
  ['duplicate-limit', local + '&limit=1&limit=2'],
  ['invalid-status', local + '&status=ACCEPTED'],
  ['invalid-limit', local + '&limit=2.5'],
  ['limit-bounds', local + '&limit=41'],
  ['private-scene', path + '?sceneId=foreign'],
  ['missing-scene', path + '?sceneId=missing'],
  ['simulation', path + '?sceneId=simulation'],
  ['disabled', local, { enabled: false }],
  ['reenabled', local, { enabled: true }],
  ['after-recall', local, { recall: true }],
  ['after-delete', local, { delete: true }],
]
const headers = { Authorization: 'Bearer ' + token }
try {
  for (const scene of scenes) store.saveScene(scene)
  for (const [key, name] of Object.entries(names)) store.set(key, name)
  const memoryIds = []
  for (const { context, candidate, ids, auto } of proposals) {
    store.set('settings', { ...store.settings, memoryGraphEnabled: true, memoryGraphAutoAdmissionEnabled: auto })
    // Supply stable entity/edge IDs through the real candidate owner's constructor options.
    const remaining = [...ids]
    engine.memory.relations.options.id = () => remaining.shift()
    const result = engine.memory.propose(context, candidate)
    assert.equal(result.status, 'saved')
    assert.equal(remaining.length, 0)
    memoryIds.push(result.memory.id)
  }
  const reviewed = await app.request(path + '/relation-2/review', {
    method: 'POST', headers: { ...headers, 'Content-Type': 'application/json' },
    body: JSON.stringify({ sceneId: 'local', expectedVersion: 1, status: 'rejected' }),
  })
  assert.equal(reviewed.status, 200, await reviewed.text())
  const before = store.db.prepare('SELECT total_changes() AS n')
  const rows = []
  for (const [name, requestPath, options = {}] of cases) {
    if (options.enabled !== undefined)
      store.set('settings', { ...store.settings, memoryGraphEnabled: options.enabled })
    if (options.hide === true)
      store.db.prepare("UPDATE memories SET value=json_set(value,'$.internalUse','knowledge_declaration') WHERE id=?").run(memoryIds[0])
    if (options.hide === false)
      store.db.prepare("UPDATE memories SET value=json_remove(value,'$.internalUse') WHERE id=?").run(memoryIds[0])
    if (options.recall) engine.memory.recall('local', 'message-1')
    if (options.delete) engine.memory.delete(memoryIds[0])
    const n = before.get().n
    const response = await app.request('http://fixture.invalid' + requestPath, {
      method: options.method, headers: { ...headers, ...(options.authorized === false ? { Authorization: 'Bearer wrong-token' } : {}), 'X-Request-ID': 'synthetic-spoof' },
    })
    assert.match(response.headers.get('x-request-id'), /^[a-f0-9-]{36}$/u)
    const content = await response.text()
    if (options.method === 'HEAD') assert.equal(content, '')
    const body = content ? JSON.parse(content) : null
    assert.doesNotMatch(content, /PRIVATE_PLATFORM|deletionEpoch|proofId|actorId|backing/u)
    for (const item of body?.items ?? []) {
      for (const field of Object.keys(times)) if (field in item) {
        assert.match(item[field], /^\d{4}-\d\d-\d\dT.*Z$/u)
        item[field] = times[field]
      }
    }
    if (response.status !== 200) assert.equal(before.get().n, n)
    rows.push({ name, path: requestPath, ...options, status: response.status, body,
      cacheControl: response.headers.get('cache-control') ?? '',
      noSniff: response.headers.get('x-content-type-options') ?? '' })
  }
  const pick = (name) => rows.find((row) => row.name === name)
  const listing = pick('listing').body
  assert.deepEqual(listing.items.map(({ id, status }) => [id, status]),
    [['relation-0', 'accepted'], ['relation-1', 'candidate'], ['relation-2', 'rejected']])
  assert.equal(listing.items[0].autoReason, 'self_explicit_no_visible_conflict')
  assert.equal(listing.items[1].sourceGroups[0].sources[0].senderName, 'Reporting speaker')
  assert.equal(listing.items[2].personName, undefined)
  assert.equal(pick('limited').body.coverage, 'limited')
  assert.equal(pick('limited').body.items.length, 1)
  assert.equal(pick('disabled').body.enabled, false)
  assert.equal(pick('hidden-backing-keeps-graph-coverage').body.coverage, 'limited')
  assert.deepEqual(pick('hidden-backing-keeps-graph-coverage').body.items, [])
  assert.equal(pick('visible-backing-restored').body.items[0].id, 'relation-0')
  assert.deepEqual(pick('after-delete').body.items.map(({ id }) => id), ['relation-2'])
  assert.equal(pick('duplicate').status, 400)
  const sourceHashes = Object.fromEntries([
    'src/api.ts', 'src/memory/interfaces/relation-routes.ts', 'src/memory/relations/candidates.ts',
  ].map((file) => [file, createHash('sha256').update(readFileSync(new URL('../../' + file, import.meta.url))).digest('hex')]))
  const target = new URL('../internal/app/testdata/memory-relations-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    sourceHashes, scenes, names, proposals, times, cases: rows }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
  console.log(`PASS ${rows.length} actual-source memory relations cases`)
} finally {
  engine.dispose()
  store.close()
}
