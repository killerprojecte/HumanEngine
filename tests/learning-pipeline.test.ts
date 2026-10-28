import { fixtureReplyPlan } from './helpers/reply-plan.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createHash } from 'node:crypto'
import sharp from 'sharp'
import { Store } from '../src/store.ts'
import { Engine } from '../src/engine.ts'
import type { AdapterContext } from '../src/plugin-adapters.ts'
import type { RuntimeMessage } from '../src/runtime.ts'
import type { MemeAsset } from '../src/memes.ts'
import type { SceneRef } from '../src/onebot.ts'

const usage = { inputTokens: 4, outputTokens: 2, totalTokens: 6 }
const httpFact = 'HTTP status codes describe response outcomes.'
const privateFact = 'HTTP 299 denotes release approval.'
const jsonFact = 'JSON values include objects and arrays.'
const groupFact = 'The group uses amber as a release label.'
const tags = {
  appearance: ['color blocks'],
  style: ['cartoon'],
  subject: ['abstract shapes'],
  emotion: ['happy'],
  intent: ['greeting'],
  tone: ['gentle'],
  intensity: ['low'],
  memeMeaning: ['welcome'],
  ocr: '',
}
async function animated(format: 'gif' | 'webp') {
  const pixels = Buffer.concat([
    Buffer.from(Array(4).fill([255, 0, 0]).flat()),
    Buffer.from(Array(4).fill([0, 0, 255]).flat()),
  ])
  return sharp(pixels, { raw: { width: 2, height: 4, channels: 3, pageHeight: 2 } })
    .toFormat(format, { loop: 0, delay: [100, 200] })
    .toBuffer()
}
async function waitFor(predicate: () => boolean, label: string) {
  const deadline = Date.now() + 10000
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error(`pipeline_wait_failed:${label}`)
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
}

test(
  'Engine background learning and animated media intake produce guarded reusable knowledge and original-byte sends',
  { timeout: 60000 },
  async () => {
    const root = await mkdtemp(join(tmpdir(), 'humanengine-learning-pipeline-'))
    const oldCwd = process.cwd(),
      oldKey = process.env.HUMANENGINE_LEARNING_FIXTURE_KEY
    process.chdir(root)
    process.env.HUMANENGINE_LEARNING_FIXTURE_KEY = 'fixture-only'
    const store = new Store(':memory:')
    store.set('settings', {
      ...store.settings,
      learningBatchMs: 100,
      debounceMs: 0,
      maxWaitMs: 100,
    })
    const originals = new Map<string, Buffer>()
    for (const format of ['gif', 'webp'] as const)
      originals.set(`https://fixture.invalid/${format}`, await animated(format))
    const fetched: string[] = []
    const engine = new Engine(store, {
      fetchMedia: async (url, signal) => {
        if (signal?.aborted) throw new Error('fixture_cancelled')
        fetched.push(url)
        const bytes = originals.get(url)
        if (!bytes) throw new Error('unexpected_fixture_url')
        return bytes
      },
    })
    const lifetime = new AbortController()
    let transport!: AdapterContext, scope!: SceneRef, otherScope!: SceneRef
    let groupId = '',
      originalNameId = '',
      publicHttpId = ''
    const reviews: Array<{
      id: string
      scopeKind: string
      input: { candidate: string; originalContext: unknown[] }
    }> = []
    const reconciliationInputs: Array<{
      relatedMemories: Array<{ id: string; content: string; canRevise: boolean }>
    }> = []
    const learningInputs: Array<{
      phase: string
      related: Array<{ id: string; content: string; canRevise: boolean }>
    }> = []
    const visionInputs: Array<{ images: string[]; text: string }> = []
    const sentBytes: Buffer[] = []
    let searchConsumed = false,
      privacyProbe = false,
      publicSourceId = ''
    const privacyResults: unknown[] = []
    const unregister = engine.protocols.register(
      'fixture:pipeline',
      'Local pipeline fixture',
      {
        async complete(context, messages, tools) {
          const output = (name: string, arguments_: Record<string, unknown>) => ({
            text: '',
            toolCalls: [{ id: `fixture-${name}`, name, arguments: arguments_ }],
            usage,
          })
          if (context.model?.id === 'vision') {
            visionInputs.push({
              images: messages[1]?.images ?? [],
              text: messages[1]?.content ?? '',
            })
            return {
              text: JSON.stringify({
                ...tags,
                selection: {
                  usable: true,
                  shareable: true,
                  confidence: 0.99,
                  containsPersonalData: false,
                  sourceSpecific: false,
                  containsRealPerson: false,
                },
              }),
              toolCalls: [],
              usage,
            }
          }
          if (tools.some((tool) => tool.name === 'knowledge.review')) {
            const input = JSON.parse(messages[1]!.content)
            const record = engine.memory
              .adminList()
              .find((record) => record.content === input.candidate)!
            reviews.push({ id: record.id, scopeKind: record.scope.kind, input })
            const privateContext =
              input.candidate === privateFact &&
              JSON.stringify(input.originalContext).includes('PRIVATE_TECH')
            return output('knowledge.review', {
              shareable: !privateContext,
              publicnessConfidence: 0.99,
              sourceSpecific: privateContext,
              containsPersonalData: false,
            })
          }
          if (tools.some((tool) => tool.name === 'learning.reconcile')) {
            const input = JSON.parse(messages[1]!.content)
            reconciliationInputs.push(input)
            const candidate = input.candidates[0]?.candidate
            const targetId =
              candidate?.content === groupFact
                ? groupId
                : candidate?.content === 'The participant uses the name Blair.'
                  ? originalNameId
                  : candidate?.content === 'HTTP status codes classify response outcomes.' ||
                      candidate?.content === 'HTTP means a different thing here.'
                    ? publicHttpId
                    : undefined
            const target = input.relatedMemories.find(
              (memory: { content: string }) =>
                memory.content ===
                engine.memory.adminList().find((record) => record.id === targetId)?.content,
            )
            return target
              ? output('learning.reconcile', {
                  candidateIndex: 0,
                  targetId: target.id,
                  action: candidate.content === groupFact ? 'merge' : 'supersede',
                })
              : { text: '', toolCalls: [], usage }
          }
          if (context.model?.id === 'learning') {
            const input = JSON.parse(messages[1]!.content) as {
              messages: RuntimeMessage[]
              relatedMemories: Array<{ id: string; content: string; canRevise: boolean }>
            }
            const source = input.messages.find((message) =>
              message.parts.some((part) => part.type === 'text' && /^[A-Z_]+:/.test(part.text)),
            )
            const text =
              source?.parts
                .filter((part) => part.type === 'text')
                .map((part) => part.text)
                .join(' ') ?? ''
            const phase = text.split(':')[0]!
            learningInputs.push({ phase, related: input.relatedMemories ?? [] })
            if (!source) return { text: '', toolCalls: [], usage }
            const common = { sourceMessageIds: [source.id], importance: 'medium', confidence: 0.6 }
            switch (phase) {
              case 'NAME_INITIAL':
                return output('learning.propose', {
                  ...common,
                  content: 'The participant uses the name Alex.',
                  kind: 'fact',
                  personId: 'person-a',
                })
              case 'NAME_CHANGE':
                return output('learning.propose', {
                  ...common,
                  content: 'The participant uses the name Blair.',
                  kind: 'fact',
                  personId: 'person-a',
                })
              case 'PRIVATE_TECH':
                return output('learning.propose', {
                  ...common,
                  content: privateFact,
                  kind: 'fact',
                  publicTechnicalDomain: 'http',
                })
              case 'TECH_HTTP':
                return output('learning.propose', {
                  ...common,
                  content: httpFact,
                  kind: 'fact',
                  publicTechnicalDomain: 'http',
                })
              case 'TECH_CORRECTION':
                return output('learning.propose', {
                  ...common,
                  content: 'HTTP status codes classify response outcomes.',
                  kind: 'fact',
                })
              case 'OTHER_CORRECTION':
                return output('learning.propose', {
                  ...common,
                  content: 'HTTP means a different thing here.',
                  kind: 'fact',
                })
              case 'TECH_JSON':
              case 'JSON_REPEAT':
                return output('learning.propose', {
                  ...common,
                  content: jsonFact,
                  kind: 'fact',
                  publicTechnicalDomain: 'json',
                })
              case 'GROUP_SEED':
                return output('learning.propose', { ...common, content: groupFact, kind: 'term' })
              case 'GROUP_MERGE':
              case 'GROUP_LOCKED':
              case 'GROUP_RECALLED':
                return output('learning.propose', {
                  ...common,
                  content: groupFact,
                  kind: 'term',
                })
              default:
                return { text: '', toolCalls: [], usage }
            }
          }
          if (context.model?.id === 'planner') {
            const plan = fixtureReplyPlan(messages)
            if (!plan)
              return output('participation.decide', {
                action: 'reply',
                reasonCode: 'fixture-media-request',
              })
            const results = messages.filter((message) => message.role === 'tool')
            if (privacyProbe) {
              if (!results.length) return output('memory.search', { query: 'HTTP' })
              if (results.length === 1) {
                const found = JSON.parse(results[0]!.content).value.find(
                  (record: { content: string }) => record.content === httpFact,
                )
                return output('memory.source', { memoryId: found.id })
              }
              privacyResults.push(JSON.parse(results[1]!.content).value)
              return output('reply.plan', { ...plan, action: 'noReply' })
            }
            if (!results.length) return output('memes.search', { query: 'greeting' })
            const receipt = JSON.parse(results[0]!.content) as {
              id: string
              value: { items: MemeAsset[]; coverage: string }
            }
            assert.equal(receipt.value.coverage, 'partial')
            const assets = receipt.value.items
            searchConsumed =
              assets.length === 2 && assets.every((asset) => asset.sources.length === 0)
            return output('reply.plan', {
              ...plan,
              assets: assets.map((asset) => ({ assetId: asset.id, evidenceId: receipt.id })),
            })
          }
          if (context.model?.id === 'thinker') {
            const plan = JSON.parse(
              messages.find((message) => message.role === 'user')!.content,
            ).plan
            return output('messages.send', {
              operationId: 'original-media',
              segments: plan.assets.map((asset: { assetId: string }) => ({
                parts: [{ type: 'media', assetId: asset.assetId, mediaType: 'image' }],
              })),
            })
          }
          throw new Error('unexpected_fixture_role')
        },
        async embeddings(_context, inputs) {
          return { vectors: inputs.map(() => [1, 0]), usage }
        },
      },
      lifetime.signal,
    )
    const roles = ['planner', 'thinker', 'learning', 'vision', 'embedding'] as const
    store.set('models', {
      providers: [
        {
          id: 'fixture',
          name: 'Fixture',
          protocol: 'fixture:pipeline',
          baseUrl: 'https://fixture.invalid/v1',
          credentialEnv: 'HUMANENGINE_LEARNING_FIXTURE_KEY',
        },
      ],
      models: roles.map((role) => ({
        id: role,
        displayName: role,
        providerId: 'fixture',
        apiModelName: 'fixture',
        capabilities: ['text', 'tools', 'vision', 'embedding'],
      })),
      routes: roles.map((role) => ({ role, orderedModelIds: [role], timeoutMs: 1000 })),
    })
    let sequence = 0
    const ingress = (
      text: string,
      senderId = 'person-a',
      selected = scope,
      parts?: RuntimeMessage['parts'],
    ) => {
      const id = transport.messageId(selected, String(++sequence))
      const now = new Date().toISOString()
      transport.receive(
        {
          id,
          sceneId: selected.id,
          senderId,
          sentAt: now,
          receivedAt: now,
          origin: 'live',
          status: 'available',
          parts: parts ?? [{ type: 'text', text }],
        },
        selected,
        false,
      )
      return id
    }
    const learn = async (text: string, senderId = 'person-a', selected = scope) => {
      const before = learningInputs.length,
        phase = text.split(':')[0]!,
        id = ingress(text, senderId, selected)
      await waitFor(
        () => learningInputs.slice(before).some((input) => input.phase === phase),
        phase,
      )
      // Fixture model/embedding/review promises resolve locally; drain their commit chain.
      await new Promise<void>((resolve) => setImmediate(resolve))
      return id
    }
    try {
      await engine.plugins.register({
        manifest: {
          id: 'fixture',
          name: 'Fixture transport',
          version: '1.0.0',
          description: 'No network transport',
          configSchema: { type: 'object' },
          capabilities: ['adapter'],
        },
        setup(context) {
          context.registerAdapter('chat', (bridge) => {
            transport = bridge
            scope = bridge.scene('account', 'group', 'a')
            otherScope = bridge.scene('account', 'group', 'b')
            return {
              start: async () => bridge.status('connected'),
              send: async (scene, segment) => {
                for (const part of segment.parts)
                  if (part.type === 'media') {
                    const resolved = bridge.media(part.assetId, scene)
                    if (!resolved?.file.startsWith('base64://'))
                      throw new Error('missing_original_fixture_media')
                    sentBytes.push(Buffer.from(resolved.file.slice('base64://'.length), 'base64'))
                  }
                return {
                  status: 'sent',
                  messageId: bridge.messageId(scene, `sent-${sentBytes.length}`),
                }
              },
              getMessage: async () => ({ items: [], coverage: 'unavailable' }),
              history: async () => ({ items: [], coverage: 'unavailable' }),
              dispose() {},
            }
          })
        },
      })
      await engine.plugins.enable('fixture')
      for (const scene of [scope, otherScope])
        store.saveScene({
          ...scene,
          name: 'Fixture group',
          participationEnabled: false,
          learningEnabled: true,
          proactiveEnabled: false,
        })
      await learn('NAME_INITIAL: The participant says their name is Alex.')
      originalNameId = engine.memory
        .adminList()
        .find((memory) => memory.content.includes('Alex'))!.id
      publicSourceId = await learn('TECH_HTTP: HTTP status codes describe response outcomes.')
      const promoted = engine.memory.adminList().find((memory) => memory.content === httpFact)!
      publicHttpId = promoted.id
      assert.equal(promoted.scope.kind, 'sharedPublicKnowledge')
      assert.equal(reviews.find((review) => review.input.candidate === httpFact)?.id, publicHttpId)
      assert.equal(reviews.find((review) => review.id === publicHttpId)?.scopeKind, 'scene')
      assert.ok(
        JSON.stringify(
          reviews.find((review) => review.id === publicHttpId)?.input.originalContext,
        ).includes('TECH_HTTP'),
      )
      assert.equal(promoted.confidence, 0.6, 'publicness review is not factual corroboration')
      assert.deepEqual(
        engine.memory
          .list({ sceneId: otherScope.id, simulation: false })
          .find((memory) => memory.id === publicHttpId)?.sources,
        [],
      )
      const simulation = engine.runtime.createSimulation()
      privacyProbe = true
      try {
        await engine.runtime.simulate(
          simulation,
          'Check the public fact provenance without opening its original scene.',
        )
      } finally {
        privacyProbe = false
        engine.runtime.endSimulation(simulation)
      }
      assert.deepEqual((privacyResults[0] as { sources: unknown[] }).sources, [])
      // Source IDs are intentionally not exposed across scenes; unknown refs are rejected before reads.
      assert.equal(privacyResults.length, 1)
      assert.equal(sentBytes.length, 0)
      await learn('PRIVATE_TECH: This private group convention uses HTTP 299 for release approval.')
      const privateRecord = engine.memory
        .adminList()
        .find((memory) => memory.content === privateFact)!
      assert.equal(privateRecord.scope.kind, 'scene')
      assert.ok(
        reviews.some(
          (review) =>
            review.id === privateRecord.id &&
            JSON.stringify(review.input.originalContext).includes('PRIVATE_TECH'),
        ),
      )
      assert.ok(
        !engine.memory
          .list({ sceneId: otherScope.id, simulation: false })
          .some((memory) => memory.id === privateRecord.id),
      )
      await learn('OTHER_CORRECTION: HTTP is different here.', 'outsider', otherScope)
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === publicHttpId)?.status,
        'active',
      )
      const groupSource = await learn('GROUP_SEED: The group uses amber as a release label.')
      groupId = engine.memory.adminList().find((memory) => memory.content === groupFact)!.id
      const corroborating = await learn(
        'GROUP_MERGE: The group uses amber as a release label.',
        'person-b',
      )
      const merged = engine.memory.adminList().find((memory) => memory.id === groupId)!
      assert.equal(merged.content, groupFact)
      assert.equal(merged.sources.length, 2)
      assert.ok(
        reconciliationInputs.some((input) =>
          input.relatedMemories.some((memory) => memory.content === groupFact && memory.canRevise),
        ),
      )
      await learn('NAME_CHANGE: The participant now uses Blair rather than Alex.')
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === originalNameId)?.status,
        'superseded',
      )
      assert.equal(
        engine.memory.list({ sceneId: scope.id, simulation: false }, 'person-a')[0]?.content,
        'The participant uses the name Blair.',
      )
      assert.deepEqual(
        engine.memory.list({ sceneId: otherScope.id, simulation: false }, 'person-a'),
        [],
      )
      await learn('TECH_CORRECTION: HTTP status codes classify response outcomes.')
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === publicHttpId)?.status,
        'superseded',
      )
      assert.ok(
        !engine.memory
          .list({ sceneId: otherScope.id, simulation: false })
          .some((memory) => memory.id === publicHttpId),
      )
      await learn('TECH_JSON: JSON values include objects and arrays.')
      const json = engine.memory.adminList().find((memory) => memory.content === jsonFact)!
      assert.equal(json.scope.kind, 'sharedPublicKnowledge')
      assert.equal(engine.memory.delete(json.id), true)
      const reviewCount = reviews.length
      await learn('JSON_REPEAT: JSON values include objects and arrays.', 'person-b')
      assert.equal(reviews.length, reviewCount)
      assert.ok(!engine.memory.adminList().some((memory) => memory.content === jsonFact))
      engine.memory.lock(groupId, true)
      await learn('GROUP_LOCKED: Amber means release-ready in this group.', 'person-c')
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === groupId)?.sources.length,
        2,
      )
      transport.recall(scope, groupSource)
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === groupId)?.sources.length,
        1,
      )
      transport.recall(scope, corroborating)
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === groupId)?.status,
        'invalidated',
      )
      await learn('GROUP_RECALLED: Amber means release-ready in this group.', 'person-d')
      assert.equal(
        engine.memory.adminList().find((memory) => memory.id === groupId)?.status,
        'invalidated',
      )
      for (const format of ['gif', 'webp'] as const) {
        const descriptor = `ob:media:${Buffer.from(JSON.stringify({ sceneId: scope.id, file: `https://fixture.invalid/${format}`, type: 'image' })).toString('base64url')}`
        ingress('', 'media-author', scope, [
          { type: 'text', text: 'A generic animated greeting made of colored shapes.' },
          { type: 'media', assetId: descriptor, mediaType: 'image' },
        ])
      }
      await waitFor(
        () =>
          engine.memes
            .adminList()
            .filter((asset) => asset.analyzed && asset.available && asset.shared).length === 2,
        'automatic-media-selection',
      )
      assert.equal(fetched.length, 2)
      assert.equal(visionInputs.length, 2)
      assert.ok(
        visionInputs.every(
          (input) =>
            input.images.length === 2 &&
            input.images[0] !== input.images[1] &&
            input.text.includes('0, 1 out of 2 frames'),
        ),
      )
      for (const [url, bytes] of originals) {
        const id = createHash('sha256').update(bytes).digest('hex')
        const asset = engine.memes.adminList().find((asset) => asset.id === id)!
        assert.equal(asset.frames, 2)
        assert.equal(asset.selectionOrigin, 'automatic')
        assert.equal(asset.sharingOrigin, 'automatic')
        assert.ok(asset.tags.intent.includes('greeting'))
        assert.ok(asset.format === url.split('/').at(-1))
        assert.deepEqual(
          await readFile(engine.memes.original({ sceneId: scope.id, simulation: false }, id).path),
          bytes,
        )
      }
      const enabled = {
        ...store.scene(scope.id)!,
        participationEnabled: true,
        learningEnabled: false,
        proactiveEnabled: true,
      }
      store.saveScene(enabled)
      engine.sceneChanged(enabled)
      await engine.runtime.proactive(scope.id)
      assert.equal(searchConsumed, true)
      assert.equal(sentBytes.length, 2)
      assert.deepEqual(
        new Set(sentBytes.map((bytes) => createHash('sha256').update(bytes).digest('hex'))),
        new Set(
          [...originals.values()].map((bytes) => createHash('sha256').update(bytes).digest('hex')),
        ),
      )
      const passive = {
        ...enabled,
        participationEnabled: false,
        learningEnabled: true,
        proactiveEnabled: false,
      }
      store.saveScene(passive)
      engine.sceneChanged(passive)
      const gif = engine.memes.adminList().find((asset) => asset.format === 'gif')!,
        webp = engine.memes.adminList().find((asset) => asset.format === 'webp')!
      engine.memes.update(gif.id, { available: false, shared: false })
      await engine.analyzeMeme(scope.id, gif.id, { force: true })
      assert.equal(engine.memes.adminList().find((asset) => asset.id === gif.id)?.available, false)
      assert.equal(engine.memes.adminList().find((asset) => asset.id === gif.id)?.shared, false)
      engine.memes.update(webp.id, { disabled: true })
      await engine.analyzeMeme(scope.id, webp.id, { force: true }).catch(() => undefined)
      assert.equal(engine.memes.adminList().find((asset) => asset.id === webp.id)?.disabled, true)
      assert.deepEqual(
        engine.memes.search({ sceneId: otherScope.id, simulation: false }, 'greeting'),
        [],
      )
    } finally {
      lifetime.abort()
      unregister()
      await engine.dispose()
      store.close()
      process.chdir(oldCwd)
      if (oldKey === undefined) delete process.env.HUMANENGINE_LEARNING_FIXTURE_KEY
      else process.env.HUMANENGINE_LEARNING_FIXTURE_KEY = oldKey
      await rm(root, { recursive: true, force: true })
    }
  },
)
