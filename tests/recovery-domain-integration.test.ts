import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, mkdir, readFile, rm, access } from 'node:fs/promises'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import type { MemoryCandidate, MemorySource } from '../src/memory.ts'
import type { ProofParentReference } from '../src/memory/domain/proofs.ts'
import { Engine } from '../src/engine.ts'
import { createApp } from '../src/api.ts'
import { MemeService, type MemeContext, type MemeTags } from '../src/memes.ts'
import type { HabitContext } from '../src/habits.ts'
import type { Router, Completion } from '../src/models.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'
import type { SceneRef } from '../src/onebot.ts'
import { metadata, RecoveryAuthority } from '../src/memory/recovery/ledger.ts'
import { backup, digest } from '../scripts/backup.ts'
import { restore } from '../scripts/restore.ts'

// Real local services and backup/restore scripts. No network, real credentials or model inference.
const scene: SceneRef = {
  id: 'recovery-a',
  kind: 'group',
  adapterId: 'offline',
  accountId: 'bot',
  platformSceneId: 'synthetic-a',
}
const other: SceneRef = {
  ...scene,
  id: 'recovery-b',
  kind: 'private',
  platformSceneId: 'synthetic-b',
}
const token = 'synthetic-recovery-admin'
const tags: MemeTags = {
  appearance: ['synthetic'],
  style: ['geometric'],
  subject: ['square'],
  emotion: ['neutral'],
  intent: ['greeting'],
  tone: ['plain'],
  intensity: ['low'],
  memeMeaning: ['fixture'],
  ocr: '',
}
const context = (sceneId = scene.id) => ({
  sceneId,
  simulation: false,
  provenance: 'contextual' as const,
})
const source = (messageId: string, sceneId = scene.id, senderId = 'synthetic-person') => ({
  sceneId,
  messageId,
  senderId,
})
async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'he-recovery-domains-')),
    database = join(root, 'live', 'humanengine.sqlite'),
    mediaRoot = join(root, 'live', 'media')
  await mkdir(mediaRoot, { recursive: true })
  let modelCalls = 0,
    sends = 0
  const opened: Array<{ store: Store; engine: Engine }> = []
  const open = (path: string, media: string) => {
    const store = new Store(path)
    const fresh = !store.scene(scene.id)
    for (const s of [scene, other])
      if (!store.scene(s.id))
        store.saveScene({
          ...s,
          name: 'Synthetic recovery',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
    if (fresh)
      store.set('settings', {
        ...store.settings,
        learningBatchMs: 300000,
        learningMaxWaitMs: 300000,
        memoryGraphEnabled: true,
        memoryGraphAutoAdmissionEnabled: true,
        memoryGraphAnswerEnabled: true,
        memoryExperienceEnabled: true,
        memoryExperienceAutoActivateEnabled: true,
        memoryExperienceHintsEnabled: true,
      })
    const engine = new Engine(store)
    Object.defineProperty(engine, 'memes', { value: new MemeService(store, media) })
    engine.router = () =>
      ({
        complete: async () => {
          modelCalls++
          throw new Error('Unexpected model call during offline recovery')
        },
      }) as unknown as Router
    engine.runtime.addAdapter('offline', {
      send: async () => {
        sends++
        throw new Error('Unexpected platform send during offline recovery')
      },
      history: async () => ({ items: [], coverage: 'complete' }),
      getMessage: async () => ({ items: [], coverage: 'complete' }),
      dispose() {},
    })
    opened.push({ store, engine })
    return { store, engine, app: createApp(store, { engine, token }) }
  }
  const live = open(database, mediaRoot)
  const authorityDirectory = metadata(live.store.db)!.authority
  return {
    root,
    database,
    mediaRoot,
    authorityDirectory,
    ...live,
    stats: () => ({ modelCalls, sends }),
    propose(
      content: string,
      sources: MemorySource[],
      parents: ProofParentReference[] = [],
      patch: Partial<MemoryCandidate> = {},
    ) {
      const result = live.engine.memory.propose(
        {
          ...context(sources[0]!.sceneId),
          provenance: parents.length ? 'contextual' : 'source-only',
          dependencyMemoryIds: parents.map((ref) => ref.id),
          dependencyParentReferences: parents,
          sourceEvidence: sources.map((item) => ({
            ...item,
            available: true,
            isBot: false,
            origin: 'live' as const,
            text: content,
          })),
        },
        { content, kind: 'claim', sources, ...patch },
      )
      assert.equal(result.status, 'saved', JSON.stringify(result))
      if (result.status !== 'saved') throw new Error('fixture_memory_rejected')
      return result.memory
    },
    async asset(messageId: string, target = scene, bytes?: Buffer) {
      const original =
        bytes ??
        (await sharp({ create: { width: 3, height: 3, channels: 3, background: '#27a3cb' } })
          .png()
          .toBuffer())
      const evidence = source(messageId, target.id)
      const ctx: MemeContext = {
        ...context(target.id),
        sourceEvidence: [{ ...evidence, available: true, isBot: false }],
      }
      const asset = await live.engine.memes.import(ctx, original, evidence)
      live.engine.memes.update(asset.id, { tags, available: true })
      return { asset: live.engine.memes.adminGet(asset.id)!, bytes: original, ctx }
    },
    habit(messageId: string, content = 'SYNTHETIC_RECOVERY_HABIT') {
      const evidence = source(messageId)
      const ctx: HabitContext = {
        ...context(),
        sourceEvidence: [
          {
            ...evidence,
            available: true,
            isBot: false,
            origin: 'live',
            text: 'Use concise replies.',
            timestamp: Date.now(),
          },
        ],
      }
      const result = live.engine.habits.propose(ctx, {
        kind: 'sceneRule',
        content,
        sources: [evidence],
      })
      assert.equal(result.status, 'saved')
      if (result.status !== 'saved') throw new Error('fixture_habit_rejected')
      return result.habit
    },
    feedback(messageId: string, senderId = 'synthetic-person') {
      const now = new Date().toISOString(),
        targetId = `bot-for-${messageId}`
      const message = (id: string, sender: string, text: string): RuntimeMessage => ({
        id,
        sceneId: scene.id,
        senderId: sender,
        origin: 'live',
        status: 'available',
        sentAt: now,
        receivedAt: now,
        parts: [{ type: 'text', text }],
      })
      live.engine.runtime.ingest(message(targetId, 'bot', 'Synthetic prior answer.'), scene)
      live.engine.runtime.ingest(
        { ...message(messageId, senderId, '请先说结论'), replyToId: targetId },
        scene,
      )
      const policy = live.engine.experience
        .adminList(context())
        .items.find((item) =>
          item.sources.some((outcome) => outcome.source.messageId === messageId),
        )
      assert.equal(policy?.status, 'active')
      return policy!
    },
    advanceAuthority(messageId = 'external-revocation') {
      const authority = new RecoveryAuthority(authorityDirectory)
      try {
        return authority.append({ kind: 'source', sceneId: scene.id, messageId }, authority.head())
      } finally {
        authority.close()
      }
    },
    async snapshot(name = 'backup') {
      const destination = join(root, name)
      const manifest = await backup({ database, mediaRoot, destination })
      return {
        destination,
        manifest,
        hashes: await Promise.all(
          manifest.files.map(async (file) => [
            file.path,
            digest(await readFile(join(destination, file.path))),
          ]),
        ),
      }
    },
    async recover(backupRoot: string, name = 'restored') {
      const result = await restore({
        backupRoot,
        destination: join(root, name),
        authorityDirectory,
      })
      return { ...result, ...open(result.database, result.mediaRoot) }
    },
    request(path: string) {
      return live.app.request(path, { headers: { Authorization: `Bearer ${token}` } })
    },
    async close() {
      let failure: unknown
      for (const { engine, store } of opened.reverse()) {
        try {
          await engine.dispose()
        } catch (error) {
          failure ??= error
          engine.runtime.dispose()
          await engine.memes.dispose()
        } finally {
          store.close()
        }
      }
      await rm(root, { recursive: true, force: true })
      if (failure) throw failure
      assert.deepEqual({ modelCalls, sends }, { modelCalls: 0, sends: 0 })
    },
  }
}

test('authenticated original-file inspection allows an initially disabled asset without granting send eligibility', async () => {
  const h = await fixture()
  try {
    const { asset, bytes, ctx } = await h.asset('disabled-source')
    h.engine.memes.update(asset.id, { disabled: true })
    assert.deepEqual(h.engine.memes.search(ctx, ''), [])
    assert.throws(() => h.engine.memes.original(ctx, asset.id), /unavailable|not_available/)
    const response = await h.request(`/api/memes/${asset.id}/file`)
    assert.equal(response.status, 200)
    assert.deepEqual(Buffer.from(await response.arrayBuffer()), bytes)
  } finally {
    await h.close()
  }
})

test('an admin original-file await cannot deliver bytes under a superseded asset safety snapshot', async () => {
  const h = await fixture()
  try {
    const { asset, bytes } = await h.asset('changing-source')
    const get = h.engine.memes.adminGet.bind(h.engine.memes)
    let calls = 0
    h.engine.memes.adminGet = (id) => {
      const snapshot = get(id)
      if (++calls === 1) queueMicrotask(() => h.engine.memes.update(id, { disabled: true }))
      return snapshot
    }
    const response = await h.request(`/api/memes/${asset.id}/file`)
    assert.ok(calls >= 2, 'the actual route rechecks after its asynchronous file read')
    assert.equal(response.status, 400)
    assert.notDeepEqual(Buffer.from(await response.arrayBuffer()), bytes)
    assert.equal(get(asset.id)?.disabled, true)
  } finally {
    await h.close()
  }
})

test('authority advancement during actual admin file read blocks delivery even though local asset metadata is unchanged', async () => {
  const h = await fixture()
  try {
    const { asset, bytes } = await h.asset('authority-source')
    const get = h.engine.memes.adminGet.bind(h.engine.memes)
    let fired = false
    h.engine.memes.adminGet = (id) => {
      const snapshot = get(id)
      if (!fired) {
        fired = true
        queueMicrotask(() => h.advanceAuthority('authority-source'))
      }
      return snapshot
    }
    const response = await h.request(`/api/memes/${asset.id}/file`)
    assert.ok(fired)
    assert.notEqual(response.status, 200)
    assert.notDeepEqual(Buffer.from(await response.arrayBuffer()), bytes)
    assert.throws(() => h.store.assertRecoveryReady(), /recovery_authority_head_changed/)
  } finally {
    await h.close()
  }
})

test('an independent authority gap blocks actual administrative readers and a previously cached thumbnail', async () => {
  const h = await fixture()
  try {
    const { asset, ctx } = await h.asset('cache-source')
    h.habit('habit-source')
    assert.equal(h.engine.memes.adminPage().total, 1)
    assert.equal(h.engine.habits.adminList().length, 1)
    assert.ok((await h.engine.memes.thumbnail(asset.id)).length > 0)
    h.advanceAuthority('cache-source')
    assert.throws(() => h.engine.memes.adminPage(), /recovery_authority_head_changed/)
    assert.throws(() => h.engine.habits.adminList(), /recovery_authority_head_changed/)
    assert.throws(() => h.engine.memes.search(ctx, ''), /recovery_authority_head_changed/)
    assert.throws(() => h.engine.memes.original(ctx, asset.id), /recovery_authority_head_changed/)
    await assert.rejects(
      () => h.engine.memes.thumbnail(asset.id),
      /recovery_authority_head_changed/,
    )
  } finally {
    await h.close()
  }
})

test('a late actual recall extension fault rolls back every local domain and gate while external source intent blocks live service', async () => {
  const h = await fixture()
  try {
    const recalled = 'atomic-recalled-source'
    const { asset } = await h.asset(recalled)
    const habit = h.habit(recalled),
      policy = h.feedback(recalled)
    const memory = h.engine.memory.create({
      scope: { kind: 'scene', sceneId: scene.id },
      content: 'ATOMIC_ROLLBACK_MEMORY_BODY',
      kind: 'fact',
      sources: [source(recalled)],
    })
    const snapshotBeforeCrash = await h.snapshot('before-crash')
    const beforeGate = metadata(h.store.db)!
    const tables = [
      'memories',
      'memory_assertion_revisions',
      'memory_assertion_revision_sources',
      'habits',
      'meme_assets',
      'meme_sources',
      'memory_experience_outcomes',
      'memory_experience_policies',
      'memory_experience_validations',
      'recall_tombstones',
    ]
    const snapshot = () =>
      tables.map((table) => [table, h.store.db.prepare(`SELECT * FROM ${table}`).all()])
    const before = snapshot()
    h.engine.runtime.addExtension({
      tools: [],
      execute: async () => undefined,
      recalled: () => {
        throw new Error('synthetic_late_domain_fault')
      },
    })
    assert.throws(() => h.engine.runtime.recall(scene.id, recalled), /synthetic_late_domain_fault/)
    assert.deepEqual(snapshot(), before, 'late failure cannot acknowledge earlier domain mutations')
    assert.deepEqual(
      metadata(h.store.db),
      beforeGate,
      'local applied head rolls back with all domains',
    )
    const authority = new RecoveryAuthority(h.authorityDirectory)
    try {
      assert.ok(authority.head().sequence > beforeGate.head.sequence)
      assert.ok(
        authority
          .entries()
          .some(
            (entry) =>
              entry.revocation.kind === 'source' && entry.revocation.messageId === recalled,
          ),
      )
    } finally {
      authority.close()
    }
    assert.throws(() => h.engine.memory.list(context()), /recovery_authority_head_changed/)
    assert.throws(() => h.engine.habits.adminList(), /recovery_authority_head_changed/)
    assert.throws(() => h.engine.memes.adminGet(asset.id), /recovery_authority_head_changed/)
    assert.throws(
      () => h.engine.experience.hints(context(), { personId: 'synthetic-person' }),
      /recovery_authority_head_changed/,
    )
    assert.ok(memory.id && habit.id && policy.id)
    await assert.rejects(() => h.snapshot('blocked-backup'), /recovery_authority_head_changed/)
    const recovered = await h.recover(snapshotBeforeCrash.destination, 'after-crash')
    assert.equal(recovered.engine.memory.list(context()).length, 0)
    assert.equal(
      recovered.engine.habits.adminList().find((row) => row.id === habit.id)?.status,
      'invalidated',
    )
    assert.equal(recovered.engine.memes.adminGet(asset.id)?.disabled, true)
    assert.equal(recovered.engine.experience.adminList(context()).items.length, 0)
    assert.equal(recovered.store.recalled(scene.id, recalled), true)
    recovered.store.assertRecoveryReady()
    await unchangedBackup(snapshotBeforeCrash)
  } finally {
    await h.close()
  }
})

test('actual Runtime rejects a Replyer send when independent authority advances after planning', async () => {
  const h = await fixture()
  try {
    h.store.set('settings', { ...h.store.settings, debounceMs: 0, maxWaitMs: 100 })
    h.store.saveScene({ ...h.store.scene(scene.id)!, participationEnabled: true })
    let scriptedCalls = 0,
      enteredReplyer = false
    const result = (toolCalls: Completion['toolCalls']): Completion => ({
      text: '',
      toolCalls,
      modelId: 'offline-recovery-script',
      usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
      assistant: { role: 'assistant', content: '', toolCalls },
    })
    h.engine.router = () =>
      ({
        complete: async (role, messages, tools = []) => {
          scriptedCalls++
          if (tools.some((tool) => tool.name === 'participation.decide'))
            return result([
              {
                id: 'decision',
                name: 'participation.decide',
                arguments: { action: 'reply', reasonCode: 'synthetic_recovery' },
              },
            ])
          if (tools.some((tool) => tool.name === 'reply.plan'))
            return result([
              { id: 'plan', name: 'reply.plan', arguments: fixtureReplyPlan(messages)! },
            ])
          assert.equal(role, 'replyer')
          enteredReplyer = true
          h.advanceAuthority('late-send-revocation')
          return result([
            {
              id: 'send',
              name: 'messages.send',
              arguments: {
                operationId: 'must-not-reach-adapter',
                segments: [{ parts: [{ type: 'text', text: 'MUST_NOT_BE_SENT' }] }],
              },
            },
          ])
        },
      }) as Router
    const now = new Date().toISOString()
    h.engine.runtime.ingest(
      {
        id: 'send-question',
        sceneId: scene.id,
        senderId: 'synthetic-person',
        origin: 'live',
        status: 'available',
        sentAt: now,
        receivedAt: now,
        parts: [{ type: 'text', text: 'Synthetic question.' }],
      },
      scene,
    )
    for (let n = 0; n < 200 && (!enteredReplyer || h.engine.runtime.activeTasks); n++)
      await new Promise((resolve) => setTimeout(resolve, 5))
    assert.ok(enteredReplyer)
    assert.equal(scriptedCalls, 3)
    assert.equal(h.engine.runtime.activeTasks, 0)
    assert.equal(h.stats().sends, 0)
    assert.ok(h.store.logs.some((row) => row.stage === 'turn' && row.status === 'cancelled'))
    assert.throws(() => h.store.assertRecoveryReady(), /recovery_authority_head_changed/)
  } finally {
    await h.close()
  }
})

test('a non-recovery shutdown failure remains visible while every later Engine cleanup still runs', async () => {
  const h = await fixture()
  const original = h.engine.embeddingIndex.dispose.bind(h.engine.embeddingIndex)
  try {
    const failure = new Error('synthetic_non_gate_shutdown_fault'),
      called: string[] = []
    const memes = h.engine.memes.dispose.bind(h.engine.memes),
      plugins = h.engine.plugins.dispose.bind(h.engine.plugins),
      runtime = h.engine.runtime.dispose.bind(h.engine.runtime)
    h.engine.embeddingIndex.dispose = async () => {
      throw failure
    }
    h.engine.memes.dispose = async () => {
      called.push('memes')
      await memes()
    }
    h.engine.plugins.dispose = async () => {
      called.push('plugins')
      await plugins()
    }
    h.engine.runtime.dispose = () => {
      called.push('runtime')
      runtime()
    }
    await assert.rejects(
      () => h.engine.dispose(),
      (error) => error instanceof AggregateError && error.errors.includes(failure),
    )
    assert.deepEqual(called, ['memes', 'plugins', 'runtime'])
  } finally {
    h.engine.embeddingIndex.dispose = original
    await h.close()
  }
})

function readableState(engine: Engine) {
  const ordered = <T extends { id: string }>(values: T[]) =>
    values.sort((a, b) => a.id.localeCompare(b.id))
  return {
    memories: [scene, other].map((target) =>
      ordered(
        engine.memory
          .list(context(target.id))
          .map(({ id, content, sources, status }) => ({ id, content, sources, status })),
      ),
    ),
    habits: ordered(
      engine.habits.adminList().map(({ id, sources, status, weight, confidence }) => ({
        id,
        sources,
        status,
        weight,
        confidence,
      })),
    ),
    memes: ordered(
      engine.memes
        .adminList()
        .map(
          ({
            id,
            sources,
            contentHash,
            disabled,
            available,
            knownOnly,
            shared,
            privacyReviewed,
            preference,
          }) => ({
            id,
            sources,
            contentHash,
            disabled,
            available,
            knownOnly: !!knownOnly,
            shared,
            privacyReviewed,
            preference,
          }),
        ),
    ),
    experience: ordered(
      engine.experience
        .adminList(context())
        .items.map(({ id, version, status, supportIds, counterexampleIds, validationId }) => ({
          id,
          version,
          status,
          supportIds,
          counterexampleIds,
          validationId,
        })),
    ),
  }
}
async function unchangedBackup(
  snapshot: Awaited<ReturnType<Awaited<ReturnType<typeof fixture>>['snapshot']>>,
) {
  assert.deepEqual(
    await Promise.all(
      snapshot.manifest.files.map(async (file) => [
        file.path,
        digest(await readFile(join(snapshot.destination, file.path))),
      ]),
    ),
    snapshot.hashes,
  )
}

test('old backup plus latest source authority reproduces live cross-domain OR and scene eligibility without terminalizing recall', async (t) => {
  t.mock.timers.enable({ apis: ['Date'], now: new Date('2026-10-28T00:00:00Z') })
  const h = await fixture()
  try {
    const revoked = 'shared-recalled-source'
    const independent = h.propose('INDEPENDENT_OR_SURVIVOR', [source(revoked)])
    assert.equal(
      h.propose(independent.content, [source('lawful-independent-source')]).id,
      independent.id,
    )
    const parent = h.propose('REVOKED_PARENT_BODY_CANARY', [source(revoked)], [], {
      subject: { kind: 'self', sourceMessageId: revoked },
      epistemicKind: 'explicit',
      relations: [
        {
          predicate: 'participates_in',
          modality: 'asserted',
          temporalScope: 'current',
          personId: 'synthetic-person',
          target: { kind: 'new', entityKind: 'plan', title: 'REVOKED_RELATION_TARGET_CANARY' },
          polarity: 'positive',
          conditions: [],
        },
      ],
    })
    const reference = h.engine.memory.captureParentReference(context(), parent)!
    assert.ok(reference)
    const child = h.propose('REVOKED_BOUND_CHILD_CANARY', [source('child-source')], [reference], {
      subject: { kind: 'self', sourceMessageId: 'child-source' },
      epistemicKind: 'explicit',
    })
    h.propose('OTHER_SCENE_SAME_MESSAGE_SURVIVES', [source(revoked, other.id)])
    const habit = h.habit(revoked)
    assert.equal(h.habit('independent-habit-source').id, habit.id)
    const finalHabit = h.habit(revoked, 'FINAL_SOURCE_HABIT_AUDIT_ROW')
    const { asset, bytes, ctx } = await h.asset(revoked)
    assert.equal((await h.asset('independent-media-source', scene, bytes)).asset.id, asset.id)
    const otherAsset = (await h.asset(revoked, other, bytes)).asset
    assert.notEqual(otherAsset.id, asset.id)
    const policy = h.feedback(revoked),
      otherPolicy = h.feedback('independent-style-source', 'other-person')
    assert.equal(h.engine.memory.relations.list(context()).items.length, 1)
    const snapshot = await h.snapshot()
    h.engine.runtime.recall(scene.id, revoked)
    const expected = readableState(h.engine)
    assert.equal(
      h.engine.habits.adminList().find((row) => row.id === finalHabit.id)?.status,
      'invalidated',
    )
    assert.ok(h.engine.memory.list(context()).some((row) => row.id === independent.id))
    assert.ok(!h.engine.memory.list(context()).some((row) => row.id === child.id))
    assert.equal(h.engine.memory.relations.list(context()).items.length, 0)
    assert.deepEqual(
      h.engine.experience.adminList(context()).items.map((row) => row.id),
      [otherPolicy.id],
    )
    assert.equal(h.engine.memes.search(ctx, '').length, 1)
    const recovered = await h.recover(snapshot.destination)
    assert.deepEqual(readableState(recovered.engine), expected)
    assert.ok(
      recovered.engine.memory
        .search(context(), 'INDEPENDENT_OR_SURVIVOR')
        .some((row) => row.id === independent.id),
    )
    assert.equal(recovered.engine.memory.relations.list(context()).items.length, 0)
    assert.equal(
      recovered.store.db
        .prepare(
          'SELECT 1 FROM memory_assertion_revision_sources WHERE scene_id=? AND message_id=?',
        )
        .get(scene.id, revoked),
      undefined,
    )
    const history = JSON.stringify(
      recovered.store.db
        .prepare('SELECT value FROM memory_assertion_revisions WHERE assertion_id IN (?,?)')
        .all(parent.id, child.id),
    )
    assert.ok(!history.includes('REVOKED_PARENT_BODY_CANARY'))
    assert.ok(!history.includes('REVOKED_BOUND_CHILD_CANARY'))
    assert.equal(
      recovered.store.db
        .prepare('SELECT 1 FROM memory_experience_policies WHERE id=?')
        .get(policy.id),
      undefined,
    )
    assert.equal(
      recovered.store.db
        .prepare('SELECT 1 FROM meme_sources WHERE scene_id=? AND message_id=?')
        .get(scene.id, revoked),
      undefined,
    )
    assert.deepEqual(
      await readFile(recovered.engine.memes.original(context(), asset.id).path),
      bytes,
    )
    assert.deepEqual(
      await readFile(recovered.engine.memes.original(context(other.id), otherAsset.id).path),
      bytes,
    )
    assert.equal(metadata(recovered.store.db)!.head.sequence, metadata(h.store.db)!.head.sequence)
    const repeated = await h.recover(snapshot.destination, 'restored-again')
    assert.deepEqual(readableState(repeated.engine), expected)
    assert.deepEqual(metadata(repeated.store.db)!.head, metadata(recovered.store.db)!.head)
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

test('post-backup terminal deletes purge bodies, bound histories, policies and scoped original bytes without deleting lawful neighbours', async () => {
  const h = await fixture()
  try {
    const parent = h.propose('TERMINAL_PARENT_BODY_CANARY', [source('terminal-parent')])
    const child = h.propose(
      'TERMINAL_CHILD_HISTORY_CANARY',
      [source('terminal-child')],
      [h.engine.memory.captureParentReference(context(), parent)!],
    )
    h.engine.memory.update(child.id, { content: 'Safe independent manual replacement.' })
    const habit = h.habit('terminal-habit'),
      policy = h.feedback('terminal-policy')
    const { asset, bytes } = await h.asset('terminal-media')
    const survivor = (await h.asset('other-media', other, bytes)).asset
    const snapshot = await h.snapshot()
    h.engine.memory.delete(parent.id)
    h.engine.habits.delete(habit.id)
    h.engine.experience.delete(context(), policy.id, policy.version)
    assert.equal(await h.engine.memes.adminDelete(asset.id), true)
    const recovered = await h.recover(snapshot.destination)
    assert.equal(
      recovered.engine.memory.adminList().some((row) => row.id === parent.id),
      false,
    )
    assert.equal(
      recovered.engine.habits.adminList().some((row) => row.id === habit.id),
      false,
    )
    assert.equal(recovered.engine.memes.adminGet(asset.id), undefined)
    assert.equal(recovered.engine.experience.adminList(context()).items.length, 0)
    const revisions = JSON.stringify(
      recovered.store.db.prepare('SELECT value FROM memory_assertion_revisions').all(),
    )
    assert.ok(!revisions.includes('TERMINAL_PARENT_BODY_CANARY'))
    assert.ok(!revisions.includes('TERMINAL_CHILD_HISTORY_CANARY'))
    assert.equal(
      recovered.store.db
        .prepare('SELECT 1 FROM memory_assertion_revision_parents WHERE parent_id=?')
        .get(parent.id),
      undefined,
    )
    await assert.rejects(() => readFile(join(recovered.mediaRoot, `${asset.id}.${asset.format}`)), {
      code: 'ENOENT',
    })
    assert.deepEqual(
      await readFile(recovered.engine.memes.original(context(other.id), survivor.id).path),
      bytes,
    )
    assert.equal(
      recovered.engine.memory.list(context()).find((row) => row.id === child.id)?.content,
      'Safe independent manual replacement.',
    )
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

for (const status of ['suspended', 'retired'] as const)
  test(`post-backup experience ${status} restores exact reviewed version, not old active eligibility`, async () => {
    const h = await fixture()
    try {
      const policy = h.feedback(`policy-${status}`),
        survivor = h.feedback('surviving-policy', 'other-person')
      const snapshot = await h.snapshot()
      const reviewed = h.engine.experience.review(context(), policy.id, policy.version, status)
      const recovered = await h.recover(snapshot.destination)
      const actual = recovered.engine.experience
        .adminList(context())
        .items.find((row) => row.id === policy.id)!
      assert.equal(actual.status, status)
      assert.equal(actual.version, reviewed.version)
      assert.equal(
        recovered.engine.experience.hints(context(), { personId: 'synthetic-person' }).length,
        0,
      )
      assert.equal(
        recovered.engine.experience.hints(context(), { personId: 'other-person' })[0]?.id,
        survivor.id,
      )
      await unchangedBackup(snapshot)
    } finally {
      await h.close()
    }
  })

for (const operation of ['disable', 'demote', 'unshare', 'disable-then-regrant'] as const)
  test(`ordered meme ${operation} suffix restores exact authorization flags and retains nondeleted bytes`, async () => {
    const h = await fixture()
    try {
      const { asset, bytes } = await h.asset(`media-${operation}`)
      if (operation === 'unshare')
        h.engine.memes.update(asset.id, { shared: true, privacyReviewed: true })
      const snapshot = await h.snapshot()
      if (operation === 'demote') h.engine.memes.adminSetIncluded(asset.id, false)
      else if (operation === 'unshare')
        h.engine.memes.update(asset.id, { shared: false, privacyReviewed: false })
      else {
        h.engine.memes.update(asset.id, { disabled: true })
        if (operation === 'disable-then-regrant')
          h.engine.memes.update(asset.id, { disabled: false, available: true })
      }
      const expected = readableState(h.engine).memes
      const recovered = await h.recover(snapshot.destination)
      assert.deepEqual(readableState(recovered.engine).memes, expected)
      const shouldSelect = operation === 'unshare' || operation === 'disable-then-regrant'
      assert.equal(recovered.engine.memes.search(context(), '').length, shouldSelect ? 1 : 0)
      assert.equal(recovered.engine.memes.search(context(other.id), '').length, 0)
      assert.deepEqual(
        await readFile(join(recovered.mediaRoot, `${asset.id}.${asset.format}`)),
        bytes,
      )
      if (shouldSelect)
        assert.deepEqual(
          await readFile(recovered.engine.memes.original(context(), asset.id).path),
          bytes,
        )
      else
        assert.throws(
          () => recovered.engine.memes.original(context(), asset.id),
          /unavailable|not_available/,
        )
      await unchangedBackup(snapshot)
    } finally {
      await h.close()
    }
  })

test('a suffix regrant missing its new source qualification quarantines instead of returning a silently demoted successful restore', async () => {
  const h = await fixture()
  try {
    const { asset, bytes } = await h.asset('old-media-source'),
      snapshot = await h.snapshot()
    h.engine.memes.adminSetIncluded(asset.id, false)
    const fresh = source('new-qualified-source'),
      ctx: MemeContext = {
        ...context(),
        sourceEvidence: [{ ...fresh, available: true, isBot: false }],
      }
    assert.equal((await h.engine.memes.import(ctx, bytes, fresh)).id, asset.id)
    h.engine.memes.adminSetIncluded(asset.id, true)
    assert.equal(h.engine.memes.search(context(), '').length, 1)
    await assert.rejects(
      () => h.recover(snapshot.destination, 'missing-proof'),
      /recovery_meme_qualification_missing/,
    )
    await assert.rejects(() => access(join(h.root, 'missing-proof')), { code: 'ENOENT' })
    await assert.rejects(() => access(join(h.root, 'missing-proof.quarantine')), { code: 'ENOENT' })
    const qualified = await h.snapshot('newer-qualified-backup')
    const recovered = await h.recover(qualified.destination, 'qualified')
    assert.deepEqual(readableState(recovered.engine).memes, readableState(h.engine).memes)
    assert.equal(recovered.engine.memes.adminGet(asset.id)?.sources.length, 2)
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

test('an already-applied deterministic asset deletion cannot kill a lawful re-admission present in a newer verified backup', async () => {
  const h = await fixture()
  try {
    const { asset, bytes } = await h.asset('old-generation-source')
    assert.equal(await h.engine.memes.adminDelete(asset.id), true)
    const next = (await h.asset('new-generation-source', scene, bytes)).asset
    assert.equal(next.id, asset.id, 'scoped content IDs are deterministic, unlike memory UUIDs')
    assert.deepEqual(
      next.sources.map((item) => item.messageId),
      ['new-generation-source'],
    )
    const snapshot = await h.snapshot()
    const recovered = await h.recover(snapshot.destination)
    assert.deepEqual(recovered.engine.memes.adminGet(asset.id)?.sources, next.sources)
    assert.equal(recovered.engine.memes.search(context(), '')[0]?.id, asset.id)
    assert.deepEqual(
      await readFile(recovered.engine.memes.original(context(), asset.id).path),
      bytes,
    )
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

test('experience reactivation needs its actual new validation receipt; an older backup cannot invent it', async () => {
  const h = await fixture()
  try {
    const policy = h.feedback('reactivation-source'),
      snapshot = await h.snapshot()
    const suspended = h.engine.experience.review(context(), policy.id, policy.version, 'suspended')
    const validated = h.engine.experience.validate(context(), policy.id, suspended.version)
    const active = h.engine.experience.review(context(), policy.id, validated.version, 'active')
    assert.notEqual(active.validationId, policy.validationId)
    await assert.rejects(
      () => h.recover(snapshot.destination, 'old-validation'),
      /recovery_experience_qualification_missing/,
    )
    await assert.rejects(() => access(join(h.root, 'old-validation')), { code: 'ENOENT' })
    const newer = await h.snapshot('new-validation-backup')
    const recovered = await h.recover(newer.destination, 'new-validation')
    assert.equal(
      recovered.engine.experience.hints(context(), { personId: 'synthetic-person' })[0]?.version,
      active.version,
    )
    assert.equal(
      recovered.engine.experience.adminList(context()).items[0]?.validationId,
      active.validationId,
    )
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

test('unrecognized future experience tables cannot be silently published by a current domain recovery reader', async () => {
  const h = await fixture()
  try {
    h.store.db.exec(
      'CREATE TABLE memory_experience_future_v2(id TEXT PRIMARY KEY, value TEXT NOT NULL)',
    )
    h.store.db
      .prepare('INSERT INTO memory_experience_future_v2 VALUES(?,?)')
      .run('future-policy', JSON.stringify({ body: 'FUTURE_PRIVATE_POLICY_CANARY' }))
    const snapshot = await h.snapshot()
    await assert.rejects(
      () => h.recover(snapshot.destination, 'future-schema'),
      /recovery_.*(schema|format|unsupported)/,
    )
    await assert.rejects(() => access(join(h.root, 'future-schema')), { code: 'ENOENT' })
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

test('unsupported columns on an otherwise known experience table fail closed before publication', async () => {
  const h = await fixture()
  try {
    h.store.db.exec(
      'ALTER TABLE memory_experience_policies ADD COLUMN future_authority_version INTEGER NOT NULL DEFAULT 2',
    )
    const snapshot = await h.snapshot()
    await assert.rejects(
      () => h.recover(snapshot.destination, 'future-columns'),
      /recovery_.*(schema|format|unsupported)/,
    )
    await assert.rejects(() => access(join(h.root, 'future-columns')), { code: 'ENOENT' })
    await unchangedBackup(snapshot)
  } finally {
    await h.close()
  }
})

for (const kind of ['unknown-kind', 'future-domain-version'] as const)
  test(`a valid hash chain with ${kind} fails closed without publishing an old snapshot`, async () => {
    const h = await fixture()
    try {
      const { asset } = await h.asset('future-event-source'),
        snapshot = await h.snapshot()
      const authority = new RecoveryAuthority(h.authorityDirectory)
      try {
        const head = authority.head(),
          sequence = head.sequence + 1
        const event =
          kind === 'unknown-kind'
            ? { kind: 'future_permission_v2', schemaVersion: 1, id: asset.id }
            : {
                kind: 'meme_delete',
                schemaVersion: 2,
                id: asset.id,
                contentHash: asset.contentHash,
                qualificationHash: '0'.repeat(64),
                format: asset.format,
              }
        const payload = JSON.stringify(event),
          hash = digest(Buffer.from(`${head.instanceId}|${sequence}|${head.hash}|${payload}`))
        // Simulate an independently retained future writer, not a broken hash chain.
        authority.db
          .prepare('INSERT INTO revocations VALUES(?,?,?,?)')
          .run(sequence, head.hash, payload, hash)
      } finally {
        authority.close()
      }
      await assert.rejects(() => h.recover(snapshot.destination, kind))
      await assert.rejects(() => access(join(h.root, kind)), { code: 'ENOENT' })
      await unchangedBackup(snapshot)
    } finally {
      await h.close()
    }
  })
