import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

const store = new Store(':memory:')
const engine = new Engine(store)
const token = 'synthetic-token'
const app = createApp(store, { engine, token })
const base = { adapterId: 'fixture', accountId: 'fixture', name: 'Synthetic',
  learningEnabled: false, participationEnabled: false, proactiveEnabled: false }
const scenes = [
  { ...base, id: 'local', kind: 'group', platformSceneId: 'local', learningEnabled: true },
  { ...base, id: 'foreign', kind: 'private', platformSceneId: 'foreign' },
  { ...base, id: 'simulation', kind: 'simulation', platformSceneId: 'simulation' },
]
const names = {
  'identity:local:subject': { senderDisplayName: ' \u202aSame nickname\u202c\n ', senderPlatformId: 'PRIVATE_PLATFORM' },
  'identity:local:Subject': { senderDisplayName: 'Same nickname' },
  'identity:local:speaker': { senderDisplayName: 'Reporting speaker' },
  'identity:foreign:subject': { senderDisplayName: 'FOREIGN_NAME' },
  'identity:local:missing': { senderDisplayName: 'UNBACKED_NAME' },
}
const source = { sceneId: 'local', messageId: 'source', senderId: 'speaker' }
const proposal = {
  context: { sceneId: 'local', simulation: false, provenance: 'source-only', botUserId: 'bot',
    sourceEvidence: [{ ...source, available: true, isBot: false, mentions: [{ partIndex: 2, userId: 'subject' }],
      sentAt: '2024-10-01T12:00:00Z', receivedAt: '2024-10-01T12:00:02Z' }] },
  candidate: { content: 'Mentioned preference', kind: 'fact', epistemicKind: 'explicit',
    subject: { kind: 'mentioned', personId: 'subject', sourceMessageId: 'source', mentionPartIndex: 2 },
    sources: [source], occurredAt: '2024-09-01T10:00:00Z', validFrom: '2024-09-01T10:00:00Z' },
}
const seeds = [
  { scope: { kind: 'scene', sceneId: 'local', personId: 'subject' }, kind: 'claim', content: 'Manual statement' },
  { scope: { kind: 'scene', sceneId: 'local', personId: 'Subject' }, kind: 'claim', content: 'Distinct same nickname' },
  { scope: { kind: 'scene', sceneId: 'foreign', personId: 'subject' }, kind: 'fact', content: 'FOREIGN_RECORD' },
  { scope: { kind: 'scene', sceneId: 'local' }, kind: 'fact', content: 'UNRESOLVED_RECORD' },
  { scope: { kind: 'sharedPublicKnowledge' }, kind: 'fact', content: 'PUBLIC_RECORD' },
  { scope: { kind: 'scene', sceneId: 'local', personId: 'expired' }, kind: 'fact', content: 'EXPIRED_RECORD', validUntil: '2025-01-01T00:00:00Z' },
]
const directory = '/api/memory-people'
const profile = directory + '/profile'
const cases = [
  ['unauthorized', directory + '?sceneId=local', { authorized: false }],
  ['directory', directory + '?sceneId=local'],
  ['profile-with-witness', profile + '?sceneId=local&personId=subject'],
  ['other-same-nickname', profile + '?sceneId=local&personId=Subject'],
  ['absent-person', profile + '?sceneId=local&personId=missing'],
  ['private-scene', directory + '?sceneId=foreign'],
  ['absent-scene', directory + '?sceneId=missing'],
  ['simulation', profile + '?sceneId=simulation&personId=subject'],
  ['required-key', profile + '?sceneId=local'],
  ['strict', directory + '?sceneId=local&unknown=x'],
  ['duplicate', directory + '?sceneId=local&sceneId=foreign'],
  ['head', directory + '?sceneId=local', { method: 'HEAD' }],
  ['after-recall', profile + '?sceneId=local&personId=subject', { recall: true }],
  ['after-delete', profile + '?sceneId=local&personId=subject', { delete: true }],
]
try {
  for (const scene of scenes) store.saveScene(scene)
  for (const [key, value] of Object.entries(names)) store.set(key, value)
  const learned = engine.memory.propose(proposal.context, proposal.candidate)
  assert.equal(learned.status, 'saved')
  const manual = seeds.map((input) => engine.memory.create(input))
  const memoryIds = [learned.memory.id, ...manual.map((row) => row.id)]
  // Keep learnedAt and locale ordering independently deterministic on both owners.
  const times = memoryIds.map((_, i) => `2025-02-${String(i + 1).padStart(2, '0')}T00:00:00.000Z`)
  const update = store.db.prepare("UPDATE memories SET value=json_set(value, '$.learnedAt', ?) WHERE id=?")
  memoryIds.forEach((id, i) => update.run(times[i], id))
  const rows = []
  for (const [name, path, options = {}] of cases) {
    if (options.recall) engine.memory.recall(source.sceneId, source.messageId)
    if (options.delete) engine.memory.delete(manual[0].id)
    const response = await app.request('http://fixture.invalid' + path, {
      method: options.method, headers: { Authorization: 'Bearer ' + (options.authorized === false ? 'wrong-token' : token) },
    })
    const body = options.method === 'HEAD' ? null : await response.json()
    for (const row of body?.records ?? []) row.id = '@' + memoryIds.indexOf(row.id)
    for (const entry of body?.timeline ?? []) entry.memoryId = '@' + memoryIds.indexOf(entry.memoryId)
    rows.push({ name, path, ...options, status: response.status, body,
      privateHeaders: response.headers.get('cache-control') === 'no-store' })
  }
  const pick = (name) => rows.find((row) => row.name === name)
  const evidence = pick('profile-with-witness').body.records.find((row) => row.id === '@0').subjectEvidence
  assert.equal(evidence.witnesses[0].subjectDisplayName, 'Same nickname')
  assert.equal(evidence.witnesses[0].sources[0].senderDisplayName, 'Reporting speaker')
  assert.deepEqual(pick('after-recall').body.records.map((row) => row.id), ['@1'])
  assert.deepEqual(pick('after-delete').body.records, [])
  assert.equal(pick('duplicate').status, 400)
  const target = new URL('../internal/app/testdata/memory-people-source.json', import.meta.url)
  const output = JSON.stringify({ node: process.version, scenes, names, proposal, seeds, times, source, cases: rows }, null, 2) + '\n'
  if (process.argv.includes('--check')) assert.equal(readFileSync(target, 'utf8'), output)
  else writeFileSync(target, output)
  console.log(`PASS ${rows.length} actual-source memory people cases`)
} finally {
  engine.dispose()
  store.close()
}
