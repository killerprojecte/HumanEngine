import crypto from 'node:crypto'
import { syncBuiltinESMExports } from 'node:module'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const app = createApp(store, { engine, token: 'synthetic-token' })
const path = '/api/memory-knowledge/query'
const sceneId = 'scene'
const personId = 'person'
const sourceText = 'I may not use this tool if busy'
const source = { sceneId, messageId: 'message', senderId: personId }
const settings = { ...store.settings, memoryKnowledgeEnabled: true }
const scene = { adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic',
  learningEnabled: true, participationEnabled: false, proactiveEnabled: false }
try {
  store.saveScene({ ...scene, id: sceneId, kind: 'group', platformSceneId: sceneId })
  store.saveScene({ ...scene, id: 'foreign', kind: 'private', platformSceneId: 'foreign' })
  store.saveScene({ ...scene, id: 'simulation', kind: 'simulation', platformSceneId: 'simulation' })
  store.set('settings', settings)
  engine.memory.knowledge.options.now = () => Date.parse('2026-11-20T00:00:00Z')
  const ids = ['edge', 'entity']
  engine.memory.knowledge.options.id = () => ids.shift()
  const originalUUID = crypto.randomUUID
  let sequence = 0
  crypto.randomUUID = () => '10000000-0000-4000-8000-' + (++sequence).toString().padStart(12, '0')
  syncBuiltinESMExports()
  const OriginalDate = globalThis.Date
  globalThis.Date = class extends OriginalDate {
    constructor(...args) { super(...(args.length ? args : ['2026-11-20T00:00:00.000Z'])) }
    static now() { return OriginalDate.parse('2026-11-20T00:00:00.000Z') }
  }
  const context = {
    sceneId, simulation: false, provenance: 'source-only',
    sourceEvidence: [{ ...source, available: true, isBot: false, parts: [{ type: 'text', text: sourceText }] }],
  }
  const candidate = {
    kind: 'claim', content: 'Qualified summary.', epistemicKind: 'explicit',
    subject: { kind: 'self', sourceMessageId: source.messageId }, sources: [source],
    knowledge: [{
      registryVersion: 1, predicate: 'uses_tool', subject: { kind: 'self', personId },
      object: { kind: 'new', entityKind: 'tool', title: 'Synthetic tool' },
      polarity: 'negative', modality: 'possible', temporalScope: 'past', conditions: ['if busy'],
      sourceSpans: [{ messageId: source.messageId, partIndex: 0, start: 0, end: sourceText.length, stance: 'self' }],
    }],
  }
  const proposed = engine.memory.propose(context, candidate)
  globalThis.Date = OriginalDate
  crypto.randomUUID = originalUUID
  syncBuiltinESMExports()
  assert.equal(proposed.status, 'saved')
  const [edge] = engine.memory.knowledge.storage.rows()
  assert.ok(edge)
  const backingRef = edge.backing
  const anchor = { kind: 'entity', entityId: edge.object.entity.id, backingRef }
  const person = { sceneId, personId, anchor: { kind: 'person', personId }, predicates: ['uses_tool'], depth: 2 }
  const memory = { ...person, personId: undefined, anchor: { kind: 'memory', backingRef } }
  const seed = { ...person, personId: undefined, anchor, seedBackingRef: backingRef }
  const cases = [
    ['unauthorized', person, 'unauthorized', 401],
    ['strict-root', { ...person, extra: true }, '', 400],
    ['strict-anchor', { ...person, anchor: { ...anchor, extra: true } }, '', 400],
    ['bad-depth', { ...person, depth: 3 }, '', 400],
    ['person-mismatch', { ...person, personId: undefined }, '', 400],
    ['scene-first', { ...person, personId: 'other', sceneId: 'simulation' }, '', 404],
    ['missing-scene', { ...person, sceneId: 'missing' }, '', 404],
    ['person', person, '', 200],
    ['memory', memory, '', 200],
    ['entity-person', { ...person, anchor }, '', 200],
    ['entity-memory', seed, '', 200],
    ['missing-seed', { ...seed, seedBackingRef: undefined }, '', 400],
    ['wrong-person', { ...person, anchor, personId: 'other' }, '', 404],
    ['wrong-epoch', { ...seed, anchor: { ...anchor, backingRef: { ...backingRef, deletionEpoch: 1 } } }, '', 404],
    ['wrong-version', { ...seed, anchor: { ...anchor, backingRef: { ...backingRef, version: 2 } } }, '', 404],
    ['wrong-id', { ...seed, anchor: { ...anchor, entityId: 'other' } }, '', 404],
    ['seed-priority', { ...seed, personId, seedBackingRef: { ...backingRef, version: 2 } }, '', 409],
    ['foreign', { ...memory, sceneId: 'foreign' }, '', 409],
    ['limited', person, 'limit', 200],
    ['limited-seed', seed, '', 404],
    ['disabled', person, 'disable', 200],
    ['disabled-entity', seed, '', 404],
    ['after-recall', person, 'recall', 200],
    ['recalled-memory', memory, '', 409],
  ]
  const scenes = store.db.prepare('SELECT value FROM scenes ORDER BY id').all().map(({ value }) => JSON.parse(value))
  const rows = []
  for (const [name, request, action, want] of cases) {
    if (action === 'limit') store.db.exec(`
      WITH RECURSIVE extra(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM extra WHERE n<200)
      INSERT INTO memory_knowledge_edges(id,scene_id,assertion_id,admission_key,value)
      SELECT 'extra-'||n,scene_id,assertion_id,'key-'||n,json_set(value,'$.id','extra-'||n,'$.object.entity.id','entity-'||n,'$.object.entity.originEdgeId','extra-'||n)
      FROM memory_knowledge_edges,extra WHERE id='edge'
    `)
    if (action === 'disable') store.set('settings', { ...settings, memoryKnowledgeEnabled: false, memoryKnowledgeAnswerEnabled: true, memoryGraphEnabled: true })
    if (action === 'recall') {
      store.db.exec("DELETE FROM memory_knowledge_edges WHERE id LIKE 'extra-%'")
      store.set('settings', settings)
      engine.memory.recall(sceneId, source.messageId)
    }
    const r = await app.request(path, { method: 'POST', headers: {
      Authorization: 'Bearer ' + (action === 'unauthorized' ? 'wrong-token' : 'synthetic-token'),
      'Content-Type': 'application/json',
    }, body: JSON.stringify(request) })
    assert.equal(r.status, want, name)
    const body = await r.json()
    const requestId = body.requestId !== undefined
    delete body.requestId
    assert.doesNotMatch(JSON.stringify(body), /backingFingerprint|originEdgeId|proofId|sourceObservations|subjectWitness|spanHash|eventRef|I may not use this tool/)
    if (name === 'limited') assert.equal(body.coverage, 'limited')
    if (['person', 'memory', 'entity-person', 'entity-memory'].includes(name)) {
      assert.equal(body.edges.length, 1, name)
      assert.equal(body.supportClosure.records.length, 1, name)
    }
    rows.push({ name, request, action, status: r.status, body, requestId,
      cacheControl: r.headers.get('cache-control') ?? '', noSniff: r.headers.get('x-content-type-options') ?? '' })
  }
  const output = JSON.stringify({ node: process.version, scenes, context, candidate, backingRef, learnedAt: proposed.memory.learnedAt, ids: [edge.id, anchor.entityId], rows }, null, 2) + '\n'
  const target = new URL('../internal/app/testdata/memory-knowledge-source.json', import.meta.url)
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
  console.log('PASS', rows.length, 'actual-source knowledge administration reads')
} finally {
  engine.dispose()
  store.close()
}
