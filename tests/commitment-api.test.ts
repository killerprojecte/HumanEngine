import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import type { CommitmentCommand } from '../src/memory/domain/commitments.ts'
function fixture() {
  const store = new Store(':memory:')
  const engine = new Engine(store)
  const headers = { Authorization: 'Bearer synthetic-commitments' }
  const app = createApp(store, { token: 'synthetic-commitments', engine })
  for (const id of ['scene', 'foreign'])
    store.saveScene({
      id,
      kind: 'group',
      adapterId: 'fixture',
      accountId: 'fixture',
      platformSceneId: id,
      name: 'Synthetic',
      learningEnabled: true,
      participationEnabled: false,
      proactiveEnabled: false,
    })
  return {
    store,
    engine,
    headers,
    app,
    close() {
      engine.dispose()
      store.close()
    },
  }
}
test('commitment API preserves complete qualifications and participant declarations with exact scene/ref authority', async () => {
  const f = fixture()
  const { store, engine, app, headers } = f
  const ctx = { sceneId: 'scene', simulation: false, provenance: 'contextual' as const }
  try {
    for (const key of [
      'memoryCommitmentsEnabled',
      'memoryCommitmentsAnswerEnabled',
      'memoryHistoricalReadEnabled',
    ] as const)
      assert.equal(store.settings[key], false)
    store.set('settings', { ...store.settings, memoryCommitmentsEnabled: true })
    store.set('identity:scene:a', {
      senderDisplayName: 'Same nickname',
      senderPlatformId: 'PRIVATE_PLATFORM_CANARY',
    })
    store.set('identity:scene:b', { senderDisplayName: 'Same nickname' })
    const add = (messageId: string, senderId: string, command: CommitmentCommand) => {
      const source = { sceneId: 'scene', messageId, senderId }
      const target = 'target' in command ? command.target : undefined
      const current = target ? engine.memory.commitments.current(ctx, target) : undefined
      const result = engine.memory.propose(
        {
          ...ctx,
          provenance: target ? 'contextual' : 'source-only',
          sourceEvidence: [
            {
              ...source,
              available: true,
              isBot: false,
              sourceText:
                command.kind === 'propose' || command.kind === 'amend'
                  ? `I propose ${command.terms.title} with the named participant. Time: ${command.terms.timeQualification ?? 'unknown'}. Conditions: ${command.terms.conditions.join('; ') || 'none stated'}.`
                  : command.kind === 'complete'
                    ? 'I explicitly declare my part of the exact plan completed.'
                    : command.kind === 'cancel'
                      ? 'I explicitly cancel my part of the exact plan.'
                      : 'I explicitly confirm the exact plan version just shown to me.',
              mentions: [{ partIndex: 1, userId: senderId === 'a' ? 'b' : 'a' }],
            },
          ],
          ...(target && current
            ? {
                dependencyMemoryIds: [],
                dependencyParentReferences: [],
                exposedCommitments: [{ ...target, mutationVersion: current.mutationVersion }],
              }
            : {}),
        },
        {
          kind: 'fact',
          epistemicKind: 'explicit',
          content: `Synthetic declaration ${messageId}`,
          subject: { kind: 'self', sourceMessageId: messageId },
          sources: [source],
          commitment: command,
        },
      )
      assert.equal(result.status, 'saved')
      return result
    }
    add('proposal', 'a', {
      kind: 'propose',
      terms: {
        kind: 'plan',
        title: 'Synthetic full plan',
        participantIds: ['a', 'b'],
        polarity: 'positive',
        modality: 'asserted',
        temporalScope: 'future',
        conditions: [
          'Only if both participants confirm; never infer consent from acknowledgement.',
        ],
        timeQualification: '18:00; exact date unknown',
      },
    })
    const get = async (path = '/api/memory-commitments?sceneId=scene') =>
      (await app.request(path, { headers })).json()
    assert.equal((await app.request('/api/memory-commitments?sceneId=scene')).status, 401)
    let body = await get()
    assert.equal(body.items.length, 1)
    assert.deepEqual(
      await (await app.request('/api/memories', { headers })).json(),
      [],
      'ordinary admin list excludes internal declaration markers while typed plans remain visible',
    )
    let row = body.items[0]
    assert.equal(row.terms.timeQualification, '18:00; exact date unknown')
    assert.equal(row.terms.occurredAt, undefined)
    assert.equal(row.participants.length, 2)
    assert.notEqual(row.participants[0].personId, row.participants[1].personId)
    assert.ok(
      row.participants.every((p: { displayName: string }) => p.displayName === 'Same nickname'),
    )
    assert.equal(row.qualification, 'declared_not_verified')
    assert.equal(row.sourceBodyCoverage, 'unavailable')
    assert.doesNotMatch(JSON.stringify(body), /PRIVATE_PLATFORM_CANARY|backingFingerprint|proofId/)
    const path = `/api/memory-commitments/${row.ref.id}?sceneId=scene&version=${row.ref.version}&deletionEpoch=${row.ref.deletionEpoch}`
    assert.equal((await app.request(path, { headers })).status, 200)
    assert.equal(
      (await app.request(path.replace('sceneId=scene', 'sceneId=foreign'), { headers })).status,
      404,
    )
    assert.equal(
      (await app.request(path.replace('version=1', 'version=999'), { headers })).status,
      404,
    )
    add('confirm-a', 'a', { kind: 'confirm', target: row.ref })
    add('confirm-b', 'b', { kind: 'confirm', target: row.ref })
    body = await get()
    row = body.items[0]
    assert.equal(
      row.confirmation,
      'proposed',
      'conditions must not become unconditional confirmation',
    )
    assert.ok(row.participants.every((p: { confirmed: boolean }) => !p.confirmed))
    add('unconditional', 'a', {
      kind: 'propose',
      terms: {
        kind: 'plan',
        title: 'Unconditional synthetic plan',
        participantIds: ['a', 'b'],
        polarity: 'positive',
        modality: 'asserted',
        temporalScope: 'future',
        conditions: [],
        timeQualification: '19:00; date unknown',
      },
    })
    const next = (await get()).items.find(
      (item: { terms?: { title: string } }) => item.terms?.title === 'Unconditional synthetic plan',
    )
    add('second-confirm-b', 'b', { kind: 'confirm', target: next.ref })
    const confirmed = (await get()).items.find(
      (item: { ref: { id: string } }) => item.ref.id === next.ref.id,
    )
    assert.equal(confirmed.confirmation, 'confirmed')
    assert.ok(confirmed.participants.every((p: { confirmed: boolean }) => p.confirmed))
    assert.equal(
      (await get('/api/memory-commitments?sceneId=scene&personId=missing')).items.length,
      0,
    )
    assert.equal(
      (await app.request('/api/memory-commitments?sceneId=scene&limit=13', { headers })).status,
      400,
    )
    assert.equal(
      (await app.request('/api/memory-commitments?sceneId=scene&extra=1', { headers })).status,
      400,
    )
    const historyPath = `/api/memory-commitments/${next.ref.id}/history?sceneId=scene&version=${next.ref.version}&deletionEpoch=${next.ref.deletionEpoch}`
    assert.equal((await app.request(historyPath, { headers })).status, 404)
    store.set('settings', { ...store.settings, memoryHistoricalReadEnabled: true })
    assert.equal((await app.request(historyPath)).status, 401)
    add('amend-plan', 'a', {
      kind: 'amend',
      target: next.ref,
      terms: {
        kind: 'plan',
        title: 'Unconditional synthetic plan',
        participantIds: ['a', 'b'],
        polarity: 'positive',
        modality: 'asserted',
        temporalScope: 'future',
        conditions: [],
        timeQualification: '20:00; date unknown',
      },
    })
    const proposed = (await get()).items.find(
      (item: { ref: { id: string; version: number } }) =>
        item.ref.id === next.ref.id && item.ref.version !== next.ref.version,
    )
    add('amend-confirm-b', 'b', { kind: 'confirm', target: proposed.ref })
    const newPath = `/api/memory-commitments/${proposed.ref.id}/history?sceneId=scene&version=${proposed.ref.version}&deletionEpoch=${proposed.ref.deletionEpoch}`
    const historyResponse = await app.request(newPath, { headers })
    assert.equal(historyResponse.status, 200)
    const planHistory = await historyResponse.json()
    assert.equal(planHistory.interpretation, 'previously_recorded_not_current_truth')
    assert.equal(planHistory.coverage, 'limited')
    assert.equal(planHistory.sourceBodyCoverage, 'unavailable')
    assert.equal(planHistory.revisions.length, 2)
    assert.deepEqual(
      planHistory.revisions.map(
        (item: { terms: { timeQualification: string } }) => item.terms.timeQualification,
      ),
      ['19:00; date unknown', '20:00; date unknown'],
    )
    assert.equal(
      (await app.request(newPath.replace('sceneId=scene', 'sceneId=foreign'), { headers })).status,
      404,
    )
    const temporalHead = (await get()).items.find(
      (item: { ref: { id: string } }) => item.ref.id === proposed.ref.id,
    )
    const temporalPath = `/api/memory-commitments/${proposed.ref.id}/timeline?sceneId=scene&version=${proposed.ref.version}&deletionEpoch=${proposed.ref.deletionEpoch}&mutationVersion=${temporalHead.mutationVersion}`
    assert.equal((await app.request(temporalPath)).status, 401)
    const temporalResponse = await app.request(temporalPath, { headers })
    assert.equal(temporalResponse.status, 200)
    const temporal = await temporalResponse.json()
    assert.equal(temporal.interpretation, 'previously_recorded_declaration_state_not_current_truth')
    assert.equal(temporal.stateTime, 'server_recorded_not_occurred')
    assert.equal(temporal.coverage, 'limited')
    assert.equal(temporal.window.maxTransitions, 64)
    assert.equal(temporal.window.throughMutationVersion, temporalHead.mutationVersion)
    assert.ok(
      temporal.recordedStates.some(
        (transition: { states: { terms?: { timeQualification?: string } }[] }) =>
          transition.states.some(
            (state) => state.terms?.timeQualification === '19:00; date unknown',
          ),
      ),
    )
    assert.doesNotMatch(
      JSON.stringify(temporal),
      /PRIVATE_PLATFORM_CANARY|backingFingerprint|proofId|sourceText/,
    )
    assert.equal((await app.request(temporalPath + '&limit=21', { headers })).status, 400)
    assert.equal(
      (await app.request(temporalPath.replace('sceneId=scene', 'sceneId=foreign'), { headers }))
        .status,
      404,
    )
    const beforeRecorded = await (
      await app.request(temporalPath + '&asOfRecorded=2000-01-01T00%3A00%3A00Z', { headers })
    ).json()
    assert.deepEqual(beforeRecorded.recordedStates, [])
    add('done-a', 'a', { kind: 'complete', target: proposed.ref })
    assert.equal(
      (await app.request(temporalPath, { headers })).status,
      404,
      'same terms with newer mutation must not refresh an old timeline anchor',
    )
    add('done-b', 'b', { kind: 'complete', target: proposed.ref })
    assert.ok(
      (await get()).items.every((item: { ref: { id: string } }) => item.ref.id !== proposed.ref.id),
      'default open excludes terminal plans',
    )
    assert.ok(
      (await get('/api/memory-commitments?sceneId=scene&lifecycle=terminal')).items.some(
        (item: { lifecycle: string }) => item.lifecycle === 'done',
      ),
    )
    assert.ok(
      (await get('/api/memory-commitments?sceneId=scene&lifecycle=all')).items.some(
        (item: { ref: { id: string } }) => item.ref.id === proposed.ref.id,
      ),
    )
    assert.equal(
      (await app.request('/api/memory-commitments?sceneId=scene&lifecycle=invalid', { headers }))
        .status,
      400,
    )
    engine.memory.recall('scene', 'amend-confirm-b')
    const afterRecall = (await get()).items.find(
      (item: { ref: { id: string } }) => item.ref.id === next.ref.id,
    )
    assert.ok(afterRecall, 'lost confirmation remains a qualified open record')
    assert.notEqual(
      afterRecall?.confirmation,
      'confirmed',
      'withdrawn required participant cannot remain confirmed',
    )
    store.set('settings', { ...store.settings, memoryCommitmentsEnabled: false })
    assert.deepEqual((await get()).items, [])
    assert.equal((await app.request(path, { headers })).status, 404)
    const before = store.db.prepare('SELECT total_changes() AS n').get()
    assert.equal(
      (await app.request('/api/memory-commitments?sceneId=simulation', { headers })).status,
      404,
    )
    assert.deepEqual(store.db.prepare('SELECT total_changes() AS n').get(), before)
  } finally {
    f.close()
  }
})

test('historical assertion API retains saved status without claiming current truth and denies revoked/deleted scopes', async () => {
  const f = fixture()
  const { store, engine, app, headers } = f
  try {
    const record = engine.memory.create({
      scope: { kind: 'scene', sceneId: 'scene', personId: 'a' },
      kind: 'claim',
      content: 'Earlier synthetic claim, only if conditions hold',
      epistemicKind: 'reported',
    })
    const url = `/api/memory-assertion-history?sceneId=scene&memoryId=${record.id}`
    assert.equal((await app.request(url)).status, 401)
    assert.equal((await app.request(url, { headers })).status, 404)
    store.set('settings', { ...store.settings, memoryHistoricalReadEnabled: true })
    engine.memory.update(record.id, { content: 'Later synthetic claim with different conditions' })
    const response = await app.request(url, { headers })
    assert.equal(response.status, 200)
    const body = await response.json()
    assert.equal(body.interpretation, 'previously_recorded_not_current_truth')
    assert.equal(body.sourceBodyCoverage, 'unavailable')
    assert.equal(body.revisions.length, 2)
    assert.ok(body.revisions.some((r: { content: string }) => r.content === record.content))
    assert.equal(body.revisions[0].validityCoverage, 'unknown')
    assert.doesNotMatch(JSON.stringify(body), /parentReferences|eventRef|deletionEpoch|sourceText/)
    assert.equal((await app.request(url + '&asOfRecorded=2026-10-01', { headers })).status, 400)
    assert.equal((await app.request(url + '&limit=21', { headers })).status, 400)
    assert.equal(
      (await app.request(url.replace('sceneId=scene', 'sceneId=foreign'), { headers })).status,
      404,
    )
    const subset = await (
      await app.request(url + '&asOfRecorded=2000-01-01T00%3A00%3A00.000Z', { headers })
    ).json()
    assert.deepEqual(subset.revisions, [])
    engine.memory.delete(record.id)
    assert.equal((await app.request(url, { headers })).status, 404)
  } finally {
    f.close()
  }
})
