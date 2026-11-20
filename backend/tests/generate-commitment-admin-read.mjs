import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Store } from '../../src/store.ts'
import { Engine } from '../../src/engine.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const scenes = ['scene', 'foreign'].map((id) => ({
  id, kind: id === 'scene' ? 'group' : 'private', adapterId: 'fixture',
  accountId: 'fixture', platformSceneId: id, name: 'Synthetic',
  learningEnabled: true, participationEnabled: false, proactiveEnabled: false,
}))
const names = {
  'identity:scene:a': { senderDisplayName: ' \u202aSame nickname\u202c ', senderPlatformId: 'PRIVATE_PLATFORM_CANARY' },
  'identity:scene:b': { senderDisplayName: 'Same nickname' },
  'identity:foreign:a': { senderDisplayName: 'PRIVATE_FOREIGN_CANARY' },
}
const inputs = ['proposal', 'other'].map((messageId, index) => {
  const participantIds = ['a', index === 0 ? 'b' : 'c']
  const source = { sceneId: 'scene', senderId: 'a', messageId }
  const terms = { kind: 'plan', title: index === 0 ? 'Synthetic full plan' : 'Synthetic other plan',
    participantIds, polarity: 'positive', modality: 'asserted', temporalScope: 'future',
    conditions: ['Only if participants confirm'], timeQualification: '18:00; exact date unknown' }
  return {
    context: { sceneId: 'scene', simulation: false, provenance: 'source-only',
      sourceEvidence: [{ ...source, available: true, isBot: false,
        sourceText: `I propose ${terms.title} with the named participant. Time: ${terms.timeQualification}. Conditions: ${terms.conditions.join('; ')}.`,
        mentions: [{ partIndex: 1, userId: participantIds[1] }] }],
    },
    candidate: { kind: 'fact', epistemicKind: 'explicit', content: `Synthetic declaration ${messageId}`,
      subject: { kind: 'self', sourceMessageId: messageId }, sources: [source],
      commitment: { kind: 'propose', terms } },
  }
})
const list = '/api/memory-commitments'
const detail = list + '/@0?sceneId=scene&version=1&deletionEpoch=0'
const history = detail.replace('/@0?', '/@0/history?')
const timeline = detail.replace('/@0?', '/@0/timeline?') + '&mutationVersion=1'
const cases = [
  ['unauthorized', list + '?sceneId=scene', { auth: false }],
  ['disabled-list', list + '?sceneId=scene'],
  ['disabled-detail', list + '/missing?sceneId=scene&version=1&deletionEpoch=0'],
  ['enabled', list + '?sceneId=scene', { seed: true }],
  ['limit-coverage', list + '?sceneId=scene&limit=1'],
  ['person-filter', list + '?sceneId=scene&personId=b'],
  ['terminal-filter', list + '?sceneId=scene&lifecycle=terminal'],
  ['all', list + '?sceneId=scene&lifecycle=all'],
  ['detail', detail],
  ['numeric-coercion', detail.replace('version=1&deletionEpoch=0', 'version=0x1&deletionEpoch=')],
  ['bad-version', detail.replace('version=1', 'version=0')],
  ['stale-version', detail.replace('version=1', 'version=2')],
  ['stale-epoch', detail.replace('deletionEpoch=0', 'deletionEpoch=1')],
  ['other-scene', detail.replace('sceneId=scene', 'sceneId=foreign')],
  ['absent-scene', detail.replace('sceneId=scene', 'sceneId=missing')],
  ['duplicate-scene', list + '?sceneId=scene&sceneId=foreign'],
  ['unknown-query', list + '?sceneId=scene&other=x'],
  ['invalid-limit', list + '?sceneId=scene&limit=13'],
  ['head', detail, { method: 'HEAD' }],
  ['history-disabled', history],
  ['timeline-disabled', timeline],
  ['history', history, { historical: true }],
  ['timeline', timeline],
  ['timeline-head', timeline, { method: 'HEAD' }],
  ['timeline-before', timeline + '&asOfRecorded=2000-01-01T00%3A00%3A00Z'],
  ['timeline-as-of', timeline + '&limit=2&asOfRecorded=2099-12-31T23%3A59%3A59.99999Z'],
  ['timeline-date-invalid', timeline + '&asOfRecorded=2000-02-30T00%3A00%3A00Z'],
  ['timeline-offset-invalid', timeline + '&asOfRecorded=2000-01-01T00%3A00%3A00%2B00%3A00'],
  ['timeline-invalid-limit', timeline + '&limit=21'],
  ['timeline-missing-mutation', timeline.replace('&mutationVersion=1', '')],
  ['timeline-foreign', timeline.replace('sceneId=scene', 'sceneId=foreign')],
  ['history-foreign', history.replace('sceneId=scene', 'sceneId=foreign')],
  ['history-missing-scene', history.replace('sceneId=scene', 'sceneId=missing')],
  ['after-recall-list', list + '?sceneId=scene', { recall: true }],
  ['after-recall-detail', detail],
  ['after-recall-history', history],
  ['after-recall-stale-timeline-anchor', timeline],
  ['after-recall-timeline', timeline.replace('mutationVersion=1', 'mutationVersion=2')],
  ['after-recall-timeline-limit', timeline.replace('mutationVersion=1', 'mutationVersion=2') + '&limit=1'],

]
const ids = []
try {
  for (const scene of scenes) store.saveScene(scene)
  for (const [key, value] of Object.entries(names)) store.set(key, value)
  const rows = []
  for (const [name, path, options = {}] of cases) {
    if (options.seed) {
      store.set('settings', { ...store.settings, memoryCommitmentsEnabled: true })
      for (const { context, candidate } of inputs) {
        const saved = engine.memory.propose(context, candidate)
        assert.equal(saved.status, 'saved')
        assert.equal(saved.commitment?.status, 'saved')
        ids.push(saved.commitment.item.ref.id)
      }
    }
    if (options.historical) store.set('settings', { ...store.settings, memoryHistoricalReadEnabled: true })
    if (options.recall) engine.memory.recall('scene', 'proposal')
    const response = await app.request('http://fixture.invalid' + path.replace('@0', ids[0]), {
      method: options.method, headers: { Authorization: 'Bearer ' + (options.auth === false ? 'wrong-token' : token) },
    })
    const body = options.method === 'HEAD' ? null : await response.json()
    const stamp = (row, key) => {
      if (row?.[key] === undefined || row[key] === null) return
      assert.equal(new Date(row[key]).toISOString(), row[key])
      row[key] = '@recorded'
    }
    const states = body?.recordedStates ?? []
    for (const item of body?.items ?? body?.revisions ?? (body?.ref ? [body] : states.flatMap((row) => row.states))) {
      item.ref.id = '@' + ids.indexOf(item.ref.id)
      stamp(item, 'recordedAt')
    }
    for (const state of states) stamp(state, 'recordedAt')
    stamp(body?.window, 'firstRecordedAt')
    rows.push({ name, path, ...options, status: response.status, body,
      privateHeaders: response.headers.get('cache-control') === 'no-store' })
  }
  const pick = (name) => rows.find((row) => row.name === name)
  assert.equal(pick('limit-coverage').body.coverage, 'limited')
  assert.equal(pick('person-filter').body.items[0].ref.id, '@0')
  assert.equal(pick('numeric-coercion').status, 200)
  assert.equal(pick('after-recall-detail').body.terms, undefined)
  assert.equal(pick('history').body.revisions[0].terms.title, 'Synthetic full plan')
  assert.equal(pick('timeline').body.recordedStates[0].states[0].terms.title, 'Synthetic full plan')
  assert.deepEqual(pick('timeline-before').body.recordedStates, [])
  assert.equal(pick('after-recall-stale-timeline-anchor').status, 404)
  assert.ok(pick('after-recall-history').body.revisions.every((row) => !row.terms))
  assert.ok(pick('after-recall-timeline').body.recordedStates.every((row) => row.states.every((item) => !item.terms)))
  assert.doesNotMatch(JSON.stringify(rows), /PRIVATE_.*CANARY|backingFingerprint|proofId/)
  const target = new URL('../internal/app/testdata/commitment-admin-read-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, scenes, names, inputs, cases: rows }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
  console.log(`PASS ${rows.length} actual-source commitment admin read cases`)
} finally {
  engine.dispose()
  store.close()
}
