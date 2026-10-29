import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { Engine } from '../src/engine.ts'
import { Store } from '../src/store.ts'
import {
  MemoryService,
  type MemoryContext,
  type MemoryRecord,
  type MemorySource,
  type ProposalResult,
} from '../src/memory.ts'
import {
  parentBindingState,
  type ProofParentReference,
  type SupportProof,
} from '../src/memory/domain/proofs.ts'
import type { RuntimeExtension, RuntimeMessage, TurnContext } from '../src/runtime.ts'
import type { SceneRef } from '../src/onebot.ts'

const scene: SceneRef = {
  id: 'bound-parent-scene',
  kind: 'group',
  adapterId: 'offline-fixture',
  accountId: 'offline-bot',
  platformSceneId: 'synthetic-group',
}
const otherScene = { ...scene, id: 'other-bound-parent-scene', accountId: 'other-bot' }
const context = (sceneId = scene.id): MemoryContext => ({ sceneId, simulation: false })
function saved(result: ProposalResult): MemoryRecord {
  assert.equal(result.status, 'saved', JSON.stringify(result))
  if (result.status !== 'saved') throw new Error('synthetic_proposal_not_saved')
  return result.memory
}
function fixture() {
  const directory = mkdtempSync(join(tmpdir(), 'he-parent-integration-'))
  const path = join(directory, 'synthetic.sqlite')
  let store: Store, memory: MemoryService
  const open = () => {
    store = new Store(path)
    for (const item of [scene, otherScene])
      if (!store.scene(item.id))
        store.saveScene({
          ...item,
          name: 'Synthetic parent binding',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
    memory = new MemoryService(store)
  }
  open()
  const source = (messageId: string, sceneId = scene.id): MemorySource => ({
    sceneId,
    messageId,
    senderId: 'synthetic-person',
  })
  const capture = (record: MemoryRecord, ctx = context()) => {
    const ref = memory.captureParentReference(ctx, record)
    assert.ok(ref, 'capture must bind an actually returned, authorized record')
    return ref
  }
  const propose = (
    content: string,
    sources: MemorySource[],
    references: ProofParentReference[] = [],
    patch: Partial<MemoryContext> = {},
  ) =>
    memory.propose(
      {
        ...context(sources[0]?.sceneId),
        provenance: references.length ? 'contextual' : 'source-only',
        dependencyMemoryIds: references.map((ref) => ref.id),
        dependencyParentReferences: references,
        sourceEvidence: sources.map((item) => ({ ...item, available: true, isBot: false })),
        ...patch,
      },
      { content, kind: 'claim', sources },
    )
  const supports = (id: string): SupportProof[] => {
    const row = store.db.prepare('SELECT value FROM memory_supports WHERE memory_id=?').get(id) as
      { value: string } | undefined
    return row ? JSON.parse(row.value) : []
  }
  const snapshot = () =>
    JSON.stringify(
      (
        store.db
          .prepare(
            "SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'memory_%' OR name IN ('memories','recall_tombstones')) ORDER BY name",
          )
          .all() as { name: string }[]
      ).map(({ name }) => [
        name,
        store.db
          .prepare(`SELECT * FROM ${name}`)
          .all()
          .map((row) => JSON.stringify(row))
          .sort(),
      ]),
    )
  const revisionBodies = () =>
    JSON.stringify(store.db.prepare('SELECT value FROM memory_assertion_revisions').all())
  const removeHistoryCompanions = () => {
    store.db.transaction(() => {
      for (const table of [
        'memory_assertion_revision_parents',
        'memory_assertion_revision_sources',
        'memory_assertion_revisions',
        'memory_assertion_state',
        'memory_command_receipts',
      ])
        store.db.prepare(`DELETE FROM ${table}`).run()
    })()
  }
  return {
    get store() {
      return store
    },
    get memory() {
      return memory
    },
    source,
    capture,
    propose,
    supports,
    snapshot,
    revisionBodies,
    removeHistoryCompanions,
    reopen: () => {
      store.close()
      open()
    },
    close: () => {
      store.close()
      rmSync(directory, { recursive: true, force: true })
    },
  }
}

for (const change of ['metadata', 'content'] as const)
  test(`bound parent ${change} revision excludes current child and grandchild without erasing their authorized history across restart`, () => {
    const h = fixture()
    try {
      const parent = saved(h.propose('Synthetic parent premise.', [h.source('parent-source')]))
      const reference = h.capture(parent)
      const child = saved(
        h.propose('CHILD_HISTORY_CANARY', [h.source('child-source')], [reference]),
      )
      const childReference = h.capture(child)
      const grandchild = saved(
        h.propose('GRANDCHILD_HISTORY_CANARY', [h.source('grandchild-source')], [childReference]),
      )
      assert.deepEqual(h.supports(parent.id)[0]!.parentReferences, [])
      assert.deepEqual(h.supports(child.id)[0]!.parentReferences, [reference])
      h.reopen()
      assert.deepEqual(h.supports(child.id)[0]!.parentReferences, [reference])
      assert.ok(h.memory.list(context()).some((item) => item.id === grandchild.id))
      h.memory.update(
        parent.id,
        change === 'metadata'
          ? { locked: true }
          : { content: 'Corrected synthetic parent premise.' },
      )
      assert.ok(h.memory.revisionState(parent.id).version > reference.version)
      for (let read = 0; read < 2; read++) {
        for (const descendant of [child, grandchild]) {
          assert.equal(
            h.memory.list(context()).some((item) => item.id === descendant.id),
            false,
          )
          assert.equal(
            h.memory
              .search(context(), descendant.content)
              .some((item) => item.id === descendant.id),
            false,
          )
          assert.equal(
            h.memory.adminList().find((item) => item.id === descendant.id)!.status,
            'active',
          )
          const history = h.memory.historyFor(context(), descendant.id)
          assert.ok(
            history?.revisions.some((revision) => revision.record.content === descendant.content),
          )
          assert.ok(h.revisionBodies().includes(descendant.content))
          assert.equal(h.memory.historyFor(context(otherScene.id), descendant.id), undefined)
        }
        assert.deepEqual(
          h.supports(child.id)[0]!.parentReferences,
          [reference],
          'old bindings never retarget the new version',
        )
        h.reopen()
      }
    } finally {
      h.close()
    }
  })

test('all parents are AND within one bound proof; another complete OR alternative independently preserves current visibility', () => {
  const h = fixture()
  try {
    const parents = ['P', 'Q', 'R'].map((label) =>
      saved(h.propose(`Synthetic ${label} premise.`, [h.source(`source-${label}`)])),
    )
    const refs = parents.map((parent) => h.capture(parent))
    const child = saved(h.propose('AND_OR_CHILD_CANARY', [h.source('child-and')], refs.slice(0, 2)))
    saved(h.propose(child.content, [h.source('child-or')], refs.slice(2)))
    assert.equal(h.supports(child.id).length, 2)
    h.memory.update(parents[0]!.id, { locked: true })
    assert.ok(h.memory.list(context()).some((item) => item.id === child.id))
    h.reopen()
    assert.ok(h.memory.list(context()).some((item) => item.id === child.id))
    h.memory.update(parents[2]!.id, { content: 'Updated synthetic R premise.' })
    assert.equal(
      h.memory.list(context()).some((item) => item.id === child.id),
      false,
    )
    const history = h.memory.historyFor(context(), child.id)!
    assert.ok(history.revisions.some((revision) => revision.supports.length === 2))
    assert.ok(h.revisionBodies().includes(child.content))
    assert.deepEqual(
      h.supports(child.id).flatMap((proof) => proof.parentReferences),
      refs,
    )
    h.reopen()
    assert.equal(
      h.memory.list(context()).some((item) => item.id === child.id),
      false,
    )
    assert.ok(h.memory.historyFor(context(), child.id)?.revisions.length)
  } finally {
    h.close()
  }
})

for (const operation of ['recall', 'delete'] as const)
  test(`parent ${operation} purges physically retained old bound descendant revisions even after a safe manual rewrite`, () => {
    const h = fixture()
    try {
      const parent = saved(h.propose('PARENT_PRIVACY_CANARY', [h.source('privacy-parent-source')]))
      const child = saved(
        h.propose(
          'OLD_BOUND_CHILD_CANARY',
          [h.source('privacy-child-source')],
          [h.capture(parent)],
        ),
      )
      h.memory.update(parent.id, { locked: true })
      assert.ok(h.memory.historyFor(context(), child.id)?.revisions.length)
      h.memory.update(child.id, { content: 'Reviewed independent safe replacement.' })
      assert.deepEqual(h.supports(child.id), [])
      assert.ok(h.revisionBodies().includes('OLD_BOUND_CHILD_CANARY'))
      if (operation === 'recall') h.memory.recall(scene.id, 'privacy-parent-source')
      else h.memory.delete(parent.id)
      for (let read = 0; read < 2; read++) {
        assert.equal(h.revisionBodies().includes('OLD_BOUND_CHILD_CANARY'), false)
        assert.equal(h.revisionBodies().includes('PARENT_PRIVACY_CANARY'), false)
        assert.equal(
          JSON.stringify(h.memory.historyFor(context(), child.id)).includes(
            'OLD_BOUND_CHILD_CANARY',
          ),
          false,
        )
        assert.ok(h.memory.revisionState(child.id).deletionEpoch > 0)
        const indexedParents = h.store.db
          .prepare(
            'SELECT COUNT(*) AS count FROM memory_assertion_revision_parents WHERE parent_id=?',
          )
          .get(parent.id) as { count: number }
        assert.equal(indexedParents.count, 0)
        h.reopen()
      }
    } finally {
      h.close()
    }
  })

test('malformed, stale, mismatched and cross-scene new parent references reject atomically', () => {
  const h = fixture()
  try {
    const parent = saved(
      h.propose('Valid parent reference fixture.', [h.source('valid-parent-source')]),
    )
    const reference = h.capture(parent)
    const foreign = saved(
      h.propose('Other scene parent.', [h.source('foreign-parent-source', otherScene.id)]),
    )
    const foreignRef = h.capture(foreign, context(otherScene.id))
    const variants: Array<{ name: string; patch: Partial<MemoryContext> }> = [
      { name: 'missing references', patch: { dependencyParentReferences: undefined } },
      { name: 'empty references', patch: { dependencyParentReferences: [] } },
      {
        name: 'duplicate references',
        patch: { dependencyParentReferences: [reference, reference] },
      },
      { name: 'duplicate parent IDs', patch: { dependencyMemoryIds: [parent.id, parent.id] } },
      { name: 'extra reference', patch: { dependencyParentReferences: [reference, foreignRef] } },
      { name: 'wrong parent ID', patch: { dependencyParentReferences: [foreignRef] } },
      {
        name: 'future version',
        patch: { dependencyParentReferences: [{ ...reference, version: reference.version + 1 }] },
      },
      {
        name: 'zero version',
        patch: { dependencyParentReferences: [{ ...reference, version: 0 }] },
      },
      {
        name: 'unsafe version',
        patch: {
          dependencyParentReferences: [{ ...reference, version: Number.MAX_SAFE_INTEGER + 1 }],
        },
      },
      {
        name: 'negative epoch',
        patch: { dependencyParentReferences: [{ ...reference, deletionEpoch: -1 }] },
      },
      {
        name: 'different epoch',
        patch: {
          dependencyParentReferences: [
            { ...reference, deletionEpoch: reference.deletionEpoch + 1 },
          ],
        },
      },
      { name: 'source-only with parent', patch: { provenance: 'source-only' } },
      {
        name: 'cross-scene parent',
        patch: { dependencyMemoryIds: [foreign.id], dependencyParentReferences: [foreignRef] },
      },
    ]
    for (const [index, variant] of variants.entries()) {
      const before = h.snapshot()
      const result = h.propose(
        `INVALID_PARENT_CANARY_${index}`,
        [h.source(`invalid-${index}`)],
        [reference],
        variant.patch,
      )
      assert.equal(result.status, 'rejected', variant.name)
      assert.equal(
        h.snapshot(),
        before,
        `${variant.name} must leave no record, proof, history or projection work`,
      )
    }
    h.memory.update(parent.id, { locked: true })
    const before = h.snapshot()
    assert.equal(
      h.propose('STALE_PARENT_CANARY', [h.source('stale-source')], [reference]).status,
      'rejected',
    )
    assert.equal(h.snapshot(), before)
  } finally {
    h.close()
  }
})

test('a history write failure rolls back a newly bound proof, record and revision together', () => {
  const h = fixture()
  try {
    const parent = saved(h.propose('Rollback parent.', [h.source('rollback-parent')]))
    const reference = h.capture(parent)
    const before = h.snapshot()
    h.store.db.exec(
      "CREATE TRIGGER reject_bound_history BEFORE INSERT ON memory_assertion_revisions BEGIN SELECT RAISE(ABORT, 'synthetic_bound_history_failure'); END",
    )
    assert.throws(
      () => h.propose('ROLLBACK_CHILD_CANARY', [h.source('rollback-child')], [reference]),
      /synthetic_bound_history_failure/,
    )
    assert.equal(h.snapshot(), before)
    h.store.db.exec('DROP TRIGGER reject_bound_history')
    const child = saved(
      h.propose('ROLLBACK_CHILD_CANARY', [h.source('rollback-child')], [reference]),
    )
    h.reopen()
    assert.deepEqual(h.supports(child.id)[0]!.parentReferences, [reference])
    assert.ok(
      h.memory
        .historyFor(context(), child.id)
        ?.revisions.some((revision) => revision.record.content === child.content),
    )
  } finally {
    h.close()
  }
})

test('legacy ID-only proofs stay explicitly unknown across lazy history import and restart instead of borrowing current revisions', () => {
  const h = fixture()
  try {
    const parent = saved(h.propose('Legacy parent premise.', [h.source('legacy-parent')]))
    const child = saved(
      h.propose('LEGACY_CHILD_CANARY', [h.source('legacy-child')], [h.capture(parent)]),
    )
    const legacy = h.supports(child.id).map(({ parentReferences: _references, ...proof }) => proof)
    h.store.db
      .prepare('UPDATE memory_supports SET value=? WHERE memory_id=?')
      .run(JSON.stringify(legacy), child.id)
    h.removeHistoryCompanions()
    for (let read = 0; read < 2; read++) {
      assert.ok(h.memory.list(context()).some((item) => item.id === child.id))
      assert.equal(parentBindingState(h.supports(child.id)[0]!), 'legacy_unknown')
      const history = h.memory.historyFor(context(), child.id)!
      assert.ok(history.revisions.length)
      for (const revision of history.revisions)
        for (const proof of revision.supports) {
          assert.equal(proof.parentReferences, undefined)
          assert.equal(parentBindingState(proof), 'legacy_unknown')
        }
      const actual = h.memory.list(context()).find((item) => item.id === parent.id)!
      assert.ok(h.capture(actual).version > 0)
      assert.equal(h.supports(child.id)[0]!.parentReferences, undefined)
      h.reopen()
    }
    h.memory.update(parent.id, { content: 'Changed legacy premise.' })
    assert.equal(
      h.memory.list(context()).some((item) => item.id === child.id),
      false,
    )
    assert.equal(h.supports(child.id)[0]!.parentReferences, undefined)
  } finally {
    h.close()
  }
})

test('capture imports a legacy parent only after exact authorized exposure; simulation and unauthorized calls perform zero writes', () => {
  const h = fixture()
  try {
    const parent = saved(h.propose('Legacy capture fixture.', [h.source('capture-parent')]))
    h.removeHistoryCompanions()
    const before = h.snapshot()
    assert.deepEqual(h.memory.revisionState(parent.id), {
      version: 0,
      deletionEpoch: 0,
      deleted: false,
    })
    assert.equal(h.memory.captureParentReference(context(otherScene.id), parent), undefined)
    assert.equal(
      h.memory.captureParentReference({ ...context(), simulation: true }, parent),
      undefined,
    )
    assert.equal(
      h.memory.captureParentReference(context(), { ...parent, content: 'Unseen replacement.' }),
      undefined,
    )
    assert.equal(h.memory.captureParentReference(context(), { ...parent, locked: true }), undefined)
    assert.equal(h.memory.historyFor({ ...context(), simulation: true }, parent.id), undefined)
    assert.equal(
      h.propose('SIMULATION_CANARY', [h.source('simulation-source')], [], { simulation: true })
        .status,
      'rejected',
    )
    assert.equal(h.snapshot(), before)
    const reference = h.capture(parent)
    assert.equal(reference.version, 1)
    assert.equal(reference.deletionEpoch, 0)
    const imported = h.memory.historyFor(context(), parent.id)!.revisions[0]!
    assert.equal(imported.event.legacyImport, true)
    assert.equal(imported.record.occurredAt, undefined)
    assert.equal(imported.record.validFrom, undefined)
    assert.equal(imported.record.learnedAt, parent.learnedAt)
    assert.equal(imported.record.lineageStatus, parent.lineageStatus)
    h.reopen()
    assert.deepEqual(
      h.capture(h.memory.list(context()).find((item) => item.id === parent.id)!),
      reference,
    )
  } finally {
    h.close()
  }
})

for (const fault of ['redacted-revision', 'changed-epoch', 'moved-scope'] as const)
  test(`historical bound proof fails closed after ${fault}, never substituting the latest parent`, () => {
    const h = fixture()
    try {
      const parent = saved(
        h.propose('Historical authorization parent.', [h.source('history-auth-parent')]),
      )
      const reference = h.capture(parent)
      const child = saved(
        h.propose('HISTORICAL_AUTH_CHILD_CANARY', [h.source('history-auth-child')], [reference]),
      )
      h.memory.update(parent.id, { locked: true })
      assert.ok(
        h.memory
          .historyFor(context(), child.id)
          ?.revisions.some((revision) => revision.record.content === child.content),
      )
      // Fault injection models a missing retained body or changed authorization ledger;
      // normal privacy mutations are separately exercised through recall/delete above.
      if (fault === 'redacted-revision')
        h.store.db
          .prepare(
            'UPDATE memory_assertion_revisions SET value=NULL WHERE assertion_id=? AND version=?',
          )
          .run(parent.id, reference.version)
      else if (fault === 'changed-epoch')
        h.store.db
          .prepare(
            'UPDATE memory_assertion_state SET deletion_epoch=deletion_epoch+1 WHERE assertion_id=?',
          )
          .run(parent.id)
      else {
        const current = h.memory.adminList().find((item) => item.id === parent.id)!
        const moved = { ...current, scope: { kind: 'scene', sceneId: otherScene.id } }
        h.store.db
          .prepare('UPDATE memories SET scope_key=?,value=? WHERE id=?')
          .run(`scene:${otherScene.id}`, JSON.stringify(moved), parent.id)
      }
      for (let read = 0; read < 2; read++) {
        assert.equal(
          h.memory.list(context()).some((item) => item.id === child.id),
          false,
        )
        assert.equal(
          JSON.stringify(h.memory.historyFor(context(), child.id) ?? null).includes(child.content),
          false,
        )
        assert.deepEqual(h.supports(child.id)[0]!.parentReferences, [reference])
        h.reopen()
      }
    } finally {
      h.close()
    }
  })

test('Engine simulation may read existing public knowledge but cannot import parent history or enqueue a bound proof', async () => {
  const h = fixture()
  let engine: Engine | undefined
  try {
    const publicRecord = h.memory.create({
      scope: { kind: 'sharedPublicKnowledge' },
      content: 'Public simulation parent fixture.',
      kind: 'claim',
    })
    h.memory.create({
      scope: { kind: 'scene', sceneId: scene.id },
      content: 'Private simulation parent fixture.',
      kind: 'claim',
    })
    h.removeHistoryCompanions()
    engine = new Engine(h.store)
    engine.router = () => {
      throw new Error('Unexpected synthetic model invocation')
    }
    const extension = (
      engine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((item) => item.tools.some((tool) => tool.name === 'memory.search'))!
    const turn: TurnContext = {
      scene,
      mode: 'simulation',
      turnId: 'simulation-parent-capture',
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    }
    const before = h.snapshot()
    const results = (await extension.execute(
      'memory.search',
      { query: 'simulation parent fixture' },
      turn,
    )) as MemoryRecord[]
    assert.deepEqual(
      results.map((record) => record.id),
      [publicRecord.id],
    )
    assert.equal(
      engine.memory.captureParentReference({ ...context(), simulation: true }, results[0]!),
      undefined,
    )
    const rejected = (await extension.execute(
      'learning.propose',
      {
        content: 'SIMULATED_PARENT_CHILD_CANARY',
        kind: 'claim',
        sourceMessageIds: ['simulation-source'],
      },
      turn,
    )) as { status: string }
    assert.equal(rejected.status, 'rejected')
    assert.equal(engine.contextualLearning.queue.stats().accepted, 0)
    assert.equal(h.snapshot(), before)
    assert.equal(engine.memory.revisionState(publicRecord.id).version, 0)
  } finally {
    await engine?.dispose()
    h.close()
  }
})

test('real Engine read-to-queue-to-commit preserves first parent references across duplicate reads, stale rereads and restart', async () => {
  const h = fixture()
  let engine: Engine | undefined
  try {
    h.store.set('settings', { ...h.store.settings, learningBatchMs: 300000 })
    engine = new Engine(h.store)
    const activeEngine = engine
    activeEngine.router = () => {
      throw new Error('Unexpected synthetic model invocation')
    }
    activeEngine.indexMemory = async () => {}
    const parent = activeEngine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id },
      content: 'Engine bound parent fixture.',
      kind: 'claim',
    })
    const extension = (
      activeEngine.runtime as unknown as { extensions: RuntimeExtension[] }
    ).extensions.find((item) => item.tools.some((tool) => tool.name === 'memory.search'))!
    const turn = (id: string): TurnContext => ({
      scene,
      mode: 'real',
      turnId: id,
      signal: new AbortController().signal,
      dependencies: new Set(),
      validations: [],
    })
    const source: RuntimeMessage = {
      id: 'engine-child-source',
      sceneId: scene.id,
      senderId: 'synthetic-person',
      sentAt: new Date().toISOString(),
      receivedAt: new Date().toISOString(),
      origin: 'live',
      status: 'available',
      parts: [{ type: 'text', text: 'Engine synthetic derived child.' }],
    }
    activeEngine.runtime.ingest(source, scene)
    const firstTurn = turn('first-exposure')
    const search = async (ctx: TurnContext) =>
      (await extension.execute('memory.search', { query: parent.content }, ctx)) as MemoryRecord[]
    assert.equal((await search(firstTurn)).length, 1)
    const firstReference = activeEngine.memory.captureParentReference(context(), parent)!
    assert.equal((await search(firstTurn)).length, 1)
    const candidate = {
      content: 'ENGINE_BOUND_CHILD_CANARY',
      kind: 'claim',
      sourceMessageIds: [source.id],
    }
    const receipts: Array<{ references: ProofParentReference[]; status: string }> = []
    const submit = activeEngine.contextualLearning.submit.bind(activeEngine.contextualLearning)
    activeEngine.contextualLearning.submit = (ctx, input, exposed) => {
      const result = submit(ctx, input, exposed)
      receipts.push({
        references: exposed.map(({ id, version, deletionEpoch }) => ({
          id,
          version,
          deletionEpoch,
        })),
        status: result.status,
      })
      return result
    }
    await extension.execute('learning.propose', candidate, firstTurn)
    await activeEngine.contextualLearning.queue.whenIdle()
    assert.deepEqual(receipts[0]!.references, [firstReference])
    assert.equal(receipts[0]!.status, 'queued')
    const child = activeEngine.memory
      .adminList()
      .find((record) => record.content === candidate.content)!
    assert.ok(child)
    assert.deepEqual(h.supports(child.id)[0]!.parentReferences, [firstReference])
    assert.equal(h.supports(child.id).length, 1)

    activeEngine.memory.update(parent.id, { locked: true })
    assert.deepEqual(
      await search(firstTurn),
      [],
      'a changed second exposure is withheld from the stale turn',
    )
    assert.ok(firstTurn.validations.some((validate) => !validate()))
    const staleResult = await extension.execute(
      'learning.propose',
      { ...candidate, content: 'STALE_ENGINE_CHILD_CANARY' },
      firstTurn,
    )
    assert.deepEqual(staleResult, { status: 'rejected', reason: 'context_ineligible' })
    assert.equal(receipts.length, 1, 'an invalid context is rejected before queue submission')
    assert.deepEqual(
      receipts[0]!.references,
      [firstReference],
      'second read never overwrites the first exposure',
    )
    assert.equal(
      activeEngine.memory
        .adminList()
        .some((record) => record.content === 'STALE_ENGINE_CHILD_CANARY'),
      false,
    )
    const freshTurn = turn('fresh-exposure')
    assert.equal((await search(freshTurn)).length, 1)
    await extension.execute(
      'learning.propose',
      { ...candidate, content: 'FRESH_ENGINE_CHILD_CANARY' },
      freshTurn,
    )
    await activeEngine.contextualLearning.queue.whenIdle()
    const fresh = activeEngine.memory
      .adminList()
      .find((record) => record.content === 'FRESH_ENGINE_CHILD_CANARY')!
    assert.ok(fresh)
    assert.ok(receipts[1]!.references[0]!.version > firstReference.version)
    assert.deepEqual(h.supports(fresh.id)[0]!.parentReferences, receipts[1]!.references)
    await activeEngine.dispose()
    engine = undefined
    h.reopen()
    assert.deepEqual(h.supports(child.id)[0]!.parentReferences, [firstReference])
    assert.equal(
      h.memory.list(context()).some((record) => record.id === child.id),
      false,
    )
    assert.ok(h.memory.historyFor(context(), child.id)?.revisions.length)
    assert.ok(h.memory.list(context()).some((record) => record.id === fresh.id))
  } finally {
    await engine?.dispose()
    h.close()
  }
})
