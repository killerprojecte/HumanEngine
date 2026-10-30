import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { components } from '../app/types/api.d.ts'

const page = readFileSync(new URL('../app/pages/memories.vue', import.meta.url), 'utf8')

// These are narrow source-wiring regressions, not a substitute for rendered browser QA.
test('memory browser retains real tab panels, bounded record rendering and detail commands', () => {
  assert.doesNotMatch(page, /:content="false"/)
  assert.equal((page.match(/:unmount-on-hide="false"/g) ?? []).length, 3)
  for (const slot of ['records', 'people', 'knowledge', 'relations', 'activity'])
    assert.match(page, new RegExp(`<template #${slot}(?:\\s|>)`))
  for (const component of [
    'MemoryPeople',
    'KnowledgePanel',
    'SemanticPanel',
    'MemoryRelations',
    'MemoryCommitments',
    'MemoryExperience',
    'MemoryWorkspace',
  ])
    assert.match(page, new RegExp(`<${component}\\b`))
  assert.match(page, /v-for="memory in pageItems"/)
  assert.doesNotMatch(page, /v-for="memory in (?:memories|visible)"/)
  assert.match(page, /if \(page\.value > count\) page\.value = count/)
  for (const command of ['delete', 'lock', 'revalidate'])
    assert.match(page, new RegExp(`ask\\('${command}'\\)`))
})

test('person filter derives its domain from same-scene administration records, not the current profile projection', () => {
  const scoped = page.slice(
    page.indexOf('const scopedPeople ='),
    page.indexOf('const personOptions ='),
  )
  assert.match(scoped, /memories\.value\.flatMap/)
  assert.match(scoped, /memory\.scope\.kind === 'scene'/)
  assert.match(scoped, /memory\.scope\.sceneId === currentSceneId\.value/)
  assert.match(page, /\.\.\.scopedPeople\.value\.map/)
  // Reka SelectItem disallows the empty value used by the all-people option.
  assert.match(page, /<select\b[^>]*v-model="personFilter"[^>]*>/s)
  assert.doesNotMatch(page, /<USelect\b[^>]*v-model="personFilter"[^>]*>/s)
})

test('creation reveals the saved record and details preserve distinct time meanings', () => {
  const create = page.slice(
    page.indexOf("if (mode.value === 'new') {", page.indexOf('async function save')),
    page.indexOf('} else if (original)'),
  )
  for (const assignment of [
    "query.value = ''",
    "personFilter.value = ''",
    "kindFilter.value = 'all'",
    "statusFilter.value = 'all'",
    "section.value = 'records'",
  ])
    assert.ok(create.includes(assignment), assignment)
  for (const field of ['learnedAt', 'observedAt', 'occurredAt', 'validFrom', 'validUntil'])
    assert.ok(page.includes(`selected.${field}`), field)
})

test('management API retains future and inactive person records that the current profile intentionally omits', async () => {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const app = createApp(store, { token: 'synthetic-memory-browser', engine })
  const headers = { Authorization: 'Bearer synthetic-memory-browser' }
  try {
    for (const id of ['browser-scene', 'other-scene'])
      store.saveScene({
        id,
        kind: 'group',
        adapterId: 'fixture',
        accountId: 'synthetic',
        platformSceneId: id,
        name: 'Synthetic UI audit',
        learningEnabled: false,
        participationEnabled: false,
        proactiveEnabled: false,
      })
    const create = (personId: string, sceneId = 'browser-scene', validFrom?: string) =>
      engine.memory.create({
        scope: { kind: 'scene', sceneId, personId },
        content: `Synthetic ${personId}`,
        kind: 'fact',
        ...(validFrom ? { validFrom } : {}),
      })
    create('current-person')
    create('future-person', 'browser-scene', '2099-01-01T00:00:00.000Z')
    const inactive = create('inactive-person')
    engine.memory.update(inactive.id, { status: 'invalidated' })
    create('foreign-person', 'other-scene')
    const response = await app.request('/api/memories', { headers })
    assert.equal(response.status, 200)
    const records = (await response.json()) as components['schemas']['Memory'][]
    const personIds = records.flatMap((record) =>
      record.scope.kind === 'scene' &&
      record.scope.sceneId === 'browser-scene' &&
      record.scope.personId
        ? [record.scope.personId]
        : [],
    )
    assert.deepEqual(personIds.sort(), ['current-person', 'future-person', 'inactive-person'])
    const projected = await app.request('/api/memory-people?sceneId=browser-scene', { headers })
    assert.equal(projected.status, 200)
    const directory = (await projected.json()) as { people: { personId: string }[] }
    assert.deepEqual(
      directory.people.map((person) => person.personId),
      ['current-person'],
    )
    assert.equal((await app.request('/api/memories')).status, 401)
  } finally {
    await engine.dispose()
    store.close()
  }
})
