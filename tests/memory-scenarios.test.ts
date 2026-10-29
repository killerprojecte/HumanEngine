import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { memoryScenarioIds as ids } from './fixtures/memory-scenarios.ts'
import { memoryScenarioHarness } from './helpers/memory-scenario-harness.ts'

// Scripted records exercise projection/storage boundaries, not natural-language extraction quality.
test('person views use exact supplied identities, not matching nicknames or account-local ID suffixes', () => {
  const h = memoryScenarioHarness()
  try {
    const first = h.saved(h.propose('小林 prefers tea.', [h.source('lin-tea')]))
    const other = h.saved(
      h.propose('小林 prefers coffee.', [h.source('other-lin-coffee', ids.otherLin)], {
        personId: ids.otherLin,
      }),
    )
    const crossAccount = h.saved(
      h.propose('小林 prefers water.', [h.source('account-b-water', ids.accountBLin)], {
        personId: ids.accountBLin,
      }),
    )
    for (const [personId, expected] of [
      [ids.lin, first],
      [ids.otherLin, other],
      [ids.accountBLin, crossAccount],
    ] as const)
      assert.deepEqual(h.memory.personProfile(h.context(), personId).records, [expected])
    assert.deepEqual(h.memory.personProfile(h.context(), '小林').records, [])
    assert.deepEqual(h.memory.personProfile(h.context(), 'lin-1').records, [])
    assert.deepEqual(
      h.memory.people(h.context()),
      [ids.lin, ids.otherLin, ids.accountBLin]
        .sort((a, b) => a.localeCompare(b))
        .map((personId) => ({ personId, recordCount: 1 })),
    )
  } finally {
    h.close()
  }
})

test('the same person in group, other group, private and other account scenes stays scene-isolated', () => {
  const h = memoryScenarioHarness()
  try {
    const scenes = [ids.group, ids.otherGroup, ids.private, ids.otherAccount]
    const records = scenes.map((sceneId, index) =>
      h.saved(
        h.propose(`Scoped preference ${index}.`, [h.source('same-message', ids.lin, sceneId)]),
      ),
    )
    for (const [index, sceneId] of scenes.entries()) {
      const profile = h.memory.personProfile(h.context(sceneId), ids.lin)
      assert.equal(profile.sceneId, sceneId)
      assert.deepEqual(profile.records, [records[index]])
      assert.deepEqual(h.memory.people(h.context(sceneId)), [{ personId: ids.lin, recordCount: 1 }])
    }
    assert.deepEqual(h.memory.personProfile(h.context('unknown-scene'), ids.lin).records, [])
  } finally {
    h.close()
  }
})

test('source speaker, quoted nickname, unassigned subject and shared public knowledge never imply person membership', () => {
  const h = memoryScenarioHarness()
  try {
    const reported = h.saved(
      h.memory.propose(
        {
          ...h.context(),
          provenance: 'source-only',
          sourceEvidence: [
            {
              ...h.source('reported-by-lin'),
              available: true,
              isBot: false,
              mentions: [{ partIndex: 1, userId: ids.zhou }],
            },
          ],
        },
        {
          content: 'Lin reports that Zhou requested tea.',
          kind: 'claim',
          personId: ids.zhou,
          sources: [h.source('reported-by-lin')],
          subject: {
            kind: 'mentioned',
            personId: ids.zhou,
            sourceMessageId: 'reported-by-lin',
            mentionPartIndex: 1,
          },
        },
      ),
    )
    h.saved(
      h.propose('小林 said he wants tea; the subject is unresolved.', [h.source('ambiguous')], {
        personId: undefined,
      }),
    )
    h.memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'Shared example mentioning 小林.',
      kind: 'fact',
      sources: [h.source('shared-by-lin')],
    })
    assert.deepEqual(h.memory.people(h.context()), [{ personId: ids.zhou, recordCount: 1 }])
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).records, [])
    assert.deepEqual(h.memory.personProfile(h.context(), '').records, [])
    assert.deepEqual(h.memory.personProfile(h.context(), ids.zhou).records, [reported])
    assert.equal(reported.sources[0]?.senderId, ids.lin)
  } finally {
    h.close()
  }
})

test('learning OFF keeps existing learned and manual person memory readable but rejects new learned proposals', () => {
  const h = memoryScenarioHarness()
  try {
    const learned = h.saved(h.propose('Existing learned preference.', [h.source('before-off')]))
    h.store.saveScene({ ...h.store.scene(ids.group)!, learningEnabled: false })
    const manual = h.memory.create({
      scope: { kind: 'scene', sceneId: ids.group, personId: ids.lin },
      content: 'Authorized manual preference while learning is off.',
      kind: 'fact',
    })
    const before = h.snapshot()
    assert.deepEqual(h.propose('Must not be written.', [h.source('during-off')]), {
      status: 'rejected',
      reason: 'learning_disabled',
    })
    assert.deepEqual(
      new Set(h.memory.personProfile(h.context(), ids.lin).records.map((record) => record.id)),
      new Set([learned.id, manual.id]),
    )
    assert.deepEqual(h.memory.people(h.context()), [{ personId: ids.lin, recordCount: 2 }])
    assert.equal(h.snapshot(), before)
  } finally {
    h.close()
  }
})

test('simulation person views are empty even for real scene IDs and do not mutate authoritative storage', () => {
  const h = memoryScenarioHarness()
  try {
    h.saved(h.propose('Private person canary.', [h.source('private-person')]))
    h.memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'Public control.',
      kind: 'fact',
    })
    const before = h.snapshot()
    for (const sceneId of [ids.group, 'simulation']) {
      const ctx = { sceneId, simulation: true }
      assert.deepEqual(h.memory.people(ctx), [])
      assert.deepEqual(h.memory.personProfile(ctx, ids.lin), {
        sceneId,
        personId: ids.lin,
        records: [],
        timeline: [],
      })
    }
    assert.equal(h.snapshot(), before)
  } finally {
    h.close()
  }
})

test('person timeline preserves known dates without substituting recorded time for unknown occurrence time', () => {
  const h = memoryScenarioHarness()
  try {
    const unknown = h.saved(
      h.propose('Recently prefers tea; start is unknown.', [h.source('unknown-time')]),
    )
    const known = h.saved(
      h.propose('Past plan with explicit occurrence time.', [h.source('known-time')], {
        occurredAt: '2026-10-19T18:00:00+08:00',
        validUntil: '2099-10-23T19:00:00+08:00',
      }),
    )
    const timeline = h.memory.personProfile(h.context(), ids.lin).timeline
    const unknownItem = timeline.find((item) => item.memoryId === unknown.id)!
    assert.equal(unknownItem.recordedAt, unknown.learnedAt)
    assert.equal(Object.hasOwn(unknownItem, 'occurredAt'), false)
    assert.equal(Object.hasOwn(unknownItem, 'validUntil'), false)
    assert.deepEqual(
      timeline.find((item) => item.memoryId === known.id),
      {
        memoryId: known.id,
        recordedAt: known.learnedAt,
        occurredAt: known.occurredAt,
        validUntil: known.validUntil,
        status: known.status,
        origin: known.origin,
        sourceCount: 1,
      },
    )
    assert.deepEqual(
      timeline,
      [...timeline].sort(
        (a, b) => a.recordedAt.localeCompare(b.recordedAt) || a.memoryId.localeCompare(b.memoryId),
      ),
    )
  } finally {
    h.close()
  }
})

test('person views expose only visible active or disputed records, not complete revision history or expired facts', () => {
  const h = memoryScenarioHarness()
  try {
    const records = ['active', 'disputed', 'superseded', 'invalidated', 'expired'].map((label) =>
      h.memory.create({
        scope: { kind: 'scene', sceneId: ids.group, personId: ids.lin },
        content: `${label} fixture.`,
        kind: 'claim',
        ...(label === 'expired' ? { validUntil: '2000-01-01T00:00:00Z' } : {}),
      }),
    )
    h.memory.update(records[1]!.id, { status: 'disputed' })
    h.memory.update(records[2]!.id, { status: 'superseded' })
    h.memory.update(records[3]!.id, { status: 'invalidated' })
    const profile = h.memory.personProfile(h.context(), ids.lin)
    assert.deepEqual(
      new Set(profile.records.map((record) => record.id)),
      new Set(records.slice(0, 2).map((record) => record.id)),
    )
    assert.deepEqual(
      new Set(profile.timeline.map((item) => item.status)),
      new Set(['active', 'disputed']),
    )
    assert.deepEqual(h.memory.people(h.context()), [{ personId: ids.lin, recordCount: 2 }])
    assert.equal(h.memory.adminList().length, 5)
  } finally {
    h.close()
  }
})

test('profile and people objects are detached projections, and repeated healthy reads create no new facts', () => {
  const h = memoryScenarioHarness()
  try {
    const original = h.saved(h.propose('Original preference.', [h.source('detached')]))
    const before = h.snapshot()
    const profile = h.memory.personProfile(h.context(), ids.lin)
    profile.records[0]!.content = 'Caller mutation must not persist.'
    profile.records[0]!.sources[0]!.senderId = ids.otherLin
    profile.timeline[0]!.recordedAt = '1900-01-01T00:00:00Z'
    profile.records.length = 0
    const people = h.memory.people(h.context())
    people[0]!.recordCount = 999
    people.push({ personId: 'invented', recordCount: 1 })
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).records, [original])
    assert.deepEqual(h.memory.people(h.context()), [{ personId: ids.lin, recordCount: 1 }])
    assert.equal(h.snapshot(), before)
  } finally {
    h.close()
  }
})

test('person views preserve AND sources and OR proof alternatives, then cascade only when no complete proof remains', () => {
  const h = memoryScenarioHarness()
  try {
    const root = h.saved(h.propose('Supported preference.', [h.source('and-a'), h.source('and-b')]))
    const alternative = h.saved(h.propose(root.content, [h.source('or-independent')]))
    assert.equal(alternative.id, root.id)
    const child = h.saved(
      h.propose('Dependent preference summary.', [h.source('summary')], {}, [root.id]),
    )
    h.memory.lock(child.id, true)
    h.memory.recall(ids.group, 'and-a')
    let profile = h.memory.personProfile(h.context(), ids.lin)
    assert.deepEqual(
      new Set(profile.records.map((record) => record.id)),
      new Set([root.id, child.id]),
    )
    assert.ok(
      profile.records.every((record) =>
        record.sources.every((source) => source.messageId !== 'and-a'),
      ),
    )
    h.memory.recall(ids.group, 'or-independent')
    profile = h.memory.personProfile(h.context(), ids.lin)
    assert.deepEqual(profile.records, [])
    assert.deepEqual(profile.timeline, [])
    assert.deepEqual(h.memory.people(h.context()), [])
    assert.equal(
      h.memory.adminList().find((record) => record.id === root.id)?.status,
      'invalidated',
    )
    assert.equal(
      h.memory.adminList().find((record) => record.id === child.id)?.status,
      'invalidated',
    )
  } finally {
    h.close()
  }
})

test('an independent child proof survives parent deletion but never keeps deleted source content in its profile', () => {
  const h = memoryScenarioHarness()
  try {
    const parent = h.saved(h.propose('Parent preference canary.', [h.source('parent')]))
    const child = h.saved(
      h.propose('Child preference.', [h.source('child-context')], {}, [parent.id]),
    )
    h.saved(h.propose(child.content, [h.source('child-independent')]))
    assert.equal(h.memory.delete(parent.id), true)
    const profile = h.memory.personProfile(h.context(), ids.lin)
    assert.deepEqual(
      profile.records.map((record) => record.id),
      [child.id],
    )
    assert.equal(JSON.stringify(profile).includes('Parent preference canary'), false)
    h.memory.recall(ids.group, 'child-independent')
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).records, [])
  } finally {
    h.close()
  }
})

test('correction, direct tombstone and delete are reflected on fresh person projections without stale-cache revival', () => {
  const h = memoryScenarioHarness()
  try {
    const old = h.saved(h.propose('Old preference.', [h.source('old')]))
    assert.equal(h.memory.personProfile(h.context(), ids.lin).records[0]?.id, old.id)
    const current = h.saved(
      h.propose('Corrected preference.', [h.source('new')], { supersedesId: old.id }),
    )
    assert.deepEqual(
      h.memory.personProfile(h.context(), ids.lin).records.map((record) => record.id),
      [current.id],
    )
    h.memory.lock(current.id, true)
    h.store.recall(ids.group, 'new')
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).records, [])
    assert.deepEqual(h.memory.people(h.context()), [])
    assert.equal(h.memory.delete(old.id), true)
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).timeline, [])
  } finally {
    h.close()
  }
})

test('deleted person memory and dependent views stay unavailable after reopening, with late exact-content writes rejected', () => {
  const root = mkdtempSync(join(tmpdir(), 'humanengine-person-scenarios-'))
  const path = join(root, 'memory.sqlite')
  let h = memoryScenarioHarness(path)
  try {
    const parent = h.saved(h.propose('Deleted preference.', [h.source('original')]))
    h.saved(h.propose('Deleted dependent summary.', [h.source('summary')], {}, [parent.id]))
    h.memory.lock(parent.id, true)
    h.memory.delete(parent.id)
    h.close()
    h = memoryScenarioHarness(path)
    assert.deepEqual(h.memory.people(h.context()), [])
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).records, [])
    assert.deepEqual(h.propose('Deleted preference.', [h.source('late-result')]), {
      status: 'rejected',
      reason: 'manually_deleted',
    })
    assert.deepEqual(h.memory.personProfile(h.context(), ids.lin).timeline, [])
  } finally {
    h.close()
    rmSync(root, { recursive: true, force: true })
  }
})
