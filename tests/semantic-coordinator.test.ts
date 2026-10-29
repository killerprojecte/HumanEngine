import assert from 'node:assert/strict'
import test from 'node:test'
import Database from 'better-sqlite3'
import { createServer } from 'node:http'
import {
  SemanticCoordinator,
  type SemanticCoordinatorPorts,
} from '../src/memory/semantic/coordinator.ts'
import {
  captureSemanticInput,
  semanticInputUnchanged,
  type SemanticDirty,
  type SemanticResolvedInput,
} from '../src/memory/semantic/input.ts'
import { SemanticService } from '../src/memory/semantic/candidates.ts'
import {
  semanticHash,
  semanticFingerprint,
  type SemanticAssertion,
  type SemanticSelection,
} from '../src/memory/semantic/contracts.ts'
import { ModelReadPolicy } from '../src/memory/read/model-read-policy.ts'
import { Router, type Completion } from '../src/models.ts'
import { currentBudgetPriority, NativeBudgetGate } from '../src/budget/native-attempt.ts'
import { BudgetLedger } from '../src/budget/ledger.ts'
import { priceKeys } from '../src/budget/estimate.ts'

const ctx = { sceneId: 'scene', simulation: false }
function fixture() {
  const db = new Database(':memory:')
  const scene = {
    id: 'scene',
    memoryReadEnabled: true,
    memoryReadRevision: 0,
    personalizationEnabled: true,
  }
  let enabled = true,
    revision = 0,
    busy = false,
    sequence = 0,
    calls = 0
  const views = new Map<string, SemanticAssertion>()
  const selections: SemanticSelection[] = []
  for (const id of [
    'long-original-assertion-a-0000000000',
    'long-original-assertion-b-0000000000',
    'long-original-assertion-c-0000000000',
  ]) {
    const source = { sceneId: ctx.sceneId, messageId: id, senderId: 'person' }
    views.set(id, {
      record: {
        id,
        scope: { kind: 'scene', sceneId: ctx.sceneId, personId: 'person' },
        content: `${id}: only on weekdays, unless raining.`,
        kind: 'claim',
        epistemicKind: 'explicit',
        sources: [source],
        confidence: 0.5,
        importance: 'medium',
        learnedAt: '2001-01-01T00:00:00Z',
        occurredAt: '2000-01-01T00:00:00Z',
        status: 'active',
        locked: false,
        origin: 'manual',
      },
      proofs: [{ proofId: `proof-${id}`, sources: [source], parents: [], parentReferences: [] }],
      dependencyKey: semanticHash(id),
      roots: [semanticHash(['scene', id])],
      rootCoverage: 'complete',
      derived: false,
    })
    selections.push({ ref: { id, version: 1, deletionEpoch: 0 }, dependencyKey: semanticHash(id) })
  }
  const partitionKey = (personId: string | null) =>
    semanticHash(
      selections
        .filter((selection) => {
          const scope = views.get(selection.ref.id)!.record.scope
          return scope.kind === 'scene' && (scope.personId ?? null) === personId
        })
        .map((selection) => ({
          ...selection,
          dependencyKey: views.get(selection.ref.id)!.dependencyKey,
        }))
        .sort((a, b) => (a.ref.id < b.ref.id ? -1 : a.ref.id > b.ref.id ? 1 : 0)),
    )
  const resolved = (): SemanticResolvedInput => ({
    coverage: 'complete',
    partitionKey: partitionKey('person'),
    entries: selections.map((selection) => ({
      selection,
      assertion: views.get(selection.ref.id)!,
    })),
  })
  const service = new SemanticService(
    db,
    {
      assertReady() {},
      enabled: () => enabled,
      learningEnabled: () => enabled,
      autoSummary: () => true,
      current: (_ctx, ref) =>
        ref.version === 1 && ref.deletionEpoch === 0 ? views.get(ref.id) : undefined,
      anchorCurrent: (_ctx, ref) => views.get(ref.id),
      episodeAnchor: () => false,
      partitionKey: (_ctx, personId) => partitionKey(personId),
    },
    { id: () => `candidate-${++sequence}` },
  )
  const ports: SemanticCoordinatorPorts = {
    readPolicy: new ModelReadPolicy({ scene: () => scene }),
    enabled: () => enabled,
    policyRevision: () => revision,
    foregroundBusy: () => busy,
    timezone: () => 'UTC',
    resolveDirty: () => resolved(),
    valid: (snapshot) => semanticInputUnchanged(snapshot, resolved()),
    beginAttempt: (snapshot) =>
      db.transaction(() =>
        service.beginAttempt(ctx, { inputKey: snapshot.inputKey, parents: snapshot.selections }),
      )(),
    finishAttempt: (snapshot, status) =>
      db.transaction(() => service.finishAttempt(ctx, snapshot.inputKey, status))(),
    commit: (snapshot, proposals, guard) =>
      db.transaction(() => {
        guard()
        const result = service.admit(ctx, snapshot.selections, proposals)
        return { created: result.created.length, rejected: result.rejected.length }
      })(),
    router: (guard) => ({
      complete: async (_role, messages) => {
        guard()
        calls++
        assert.equal(currentBudgetPriority(0), 2)
        const wire = JSON.parse(messages[1]!.content)
        assert.ok(wire.declarations.length >= 2 && wire.declarations.length <= 12)
        assert.equal(wire.declarations[0].record.occurredAt, '2000-01-01T00:00:00Z')
        assert.ok(Date.parse(wire.now) > Date.parse('2026-01-01'))
        const result: Completion = {
          text: '',
          modelId: 'synthetic',
          usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
          assistant: { role: 'assistant', content: '' },
          toolCalls: [
            {
              id: 'synthetic',
              name: 'memory.semantic_propose',
              arguments: {
                kind: 'summary_candidate',
                selectedParentIds: wire.declarations
                  .slice(0, 2)
                  .map((item: { ref: { id: string } }) => item.ref.id),
                text: 'Attributed weekday declarations; rain remains an exception.',
                hypothesis: false,
              },
            },
          ],
        }
        return result
      },
    }),
  }
  const make = () =>
    new SemanticCoordinator(ports, { quietMs: 0, maxWaitMs: 0, minIntervalMs: 0, pollMs: 10 })
  const dirty = (): SemanticDirty => ({
    sceneId: 'scene',
    personId: 'person',
    trigger: selections[0]!,
    reason: 'assertion_committed',
  })
  return {
    db,
    scene,
    views,
    selections,
    service,
    ports,
    make,
    dirty,
    resolved,
    partitionKey,
    calls: () => calls,
    enabled: (value: boolean) => {
      enabled = value
    },
    revision: () => {
      revision++
    },
    busy: (value: boolean) => {
      busy = value
    },
  }
}

test('dirty source uses managed lane and real candidate commit with all visible parents, then unchanged/no restart repeat', async () => {
  const f = fixture()
  let queue = f.make()
  try {
    assert.equal(queue.notify(f.dirty()), 'accepted')
    await queue.whenIdle()
    assert.equal(f.calls(), 1)
    const view = f.service.list(ctx).items[0]!
    assert.equal(view.candidate.parents.length, 3)
    assert.equal(view.candidate.selectedParentIds.length, 2)
    assert.equal(view.declarations.length, 3)
    queue.notify(f.dirty())
    await queue.whenIdle()
    assert.equal(f.calls(), 1)
    queue.dispose()
    queue = f.make()
    await queue.whenIdle()
    assert.equal(f.calls(), 1)
    queue.notify(f.dirty())
    await queue.whenIdle()
    assert.equal(f.calls(), 1)
  } finally {
    queue.dispose()
    f.db.close()
  }
})

test('OFF clears pending, paused notifications do not replay and foreground busy delays without model calls', async () => {
  const f = fixture(),
    queue = f.make()
  try {
    f.busy(true)
    queue.notify(f.dirty())
    await Promise.resolve()
    assert.equal(f.calls(), 0)
    f.enabled(false)
    queue.configure()
    await queue.whenIdle()
    assert.equal(queue.notify(f.dirty()), 'disabled')
    f.enabled(true)
    f.busy(false)
    queue.configure()
    await queue.whenIdle()
    assert.equal(f.calls(), 0)
    queue.notify(f.dirty())
    await queue.whenIdle()
    assert.equal(f.calls(), 1)
  } finally {
    queue.dispose()
    f.db.close()
  }
})

test('first permission ticket cannot survive OFF→ON and exact-source change while awaiting aborts before commit', async () => {
  for (const reason of ['policy', 'parent'] as const) {
    const f = fixture()
    let release!: () => void, entered!: () => void
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    const started = new Promise<void>((resolve) => {
      entered = resolve
    })
    const original = f.ports.router
    let signal: AbortSignal | undefined
    f.ports.router = (guard) => ({
      complete: async (...args) => {
        signal = args[3]
        entered()
        await held
        return original(guard).complete(...args)
      },
    })
    const queue = f.make()
    try {
      queue.notify(f.dirty())
      await started
      if (reason === 'policy') {
        f.scene.memoryReadRevision += 2
        f.revision()
      } else f.views.get(f.selections[0]!.ref.id)!.record.content = 'Changed source after capture'
      queue.configure()
      assert.equal(signal?.aborted, true)
      release()
      await queue.whenIdle()
      assert.equal(f.service.list(ctx).items.length, 0)
      assert.equal(queue.stats().abandoned, 1)
    } finally {
      release()
      queue.dispose()
      f.db.close()
    }
  }
})

test('failed or empty attempts are durably deduplicated; queue never retries unknown outcomes', async () => {
  for (const outcome of ['empty', 'error'] as const) {
    const f = fixture()
    let calls = 0
    f.ports.router = () => ({
      complete: async () => {
        calls++
        if (outcome === 'error') throw Error('synthetic_unknown')
        return {
          text: '',
          modelId: 'synthetic',
          usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
          assistant: { role: 'assistant', content: '' },
          toolCalls: [],
        }
      },
    })
    const queue = f.make()
    try {
      queue.notify(f.dirty())
      await queue.whenIdle()
      queue.notify(f.dirty())
      await queue.whenIdle()
      assert.equal(calls, 1)
      assert.equal(f.service.list(ctx).items.length, 0)
    } finally {
      queue.dispose()
      f.db.close()
    }
  }
})

test('incomplete, derived, same-root and unanchored cross-person inputs cause zero calls', async () => {
  for (const kind of ['limited', 'derived', 'same-root', 'cross-person'] as const) {
    const f = fixture(),
      first = f.views.get(f.selections[0]!.ref.id)!
    if (kind === 'limited') first.rootCoverage = 'limited'
    if (kind === 'derived') first.derived = true
    if (kind === 'same-root')
      for (const value of f.views.values()) value.roots = [semanticHash('one-original-message')]
    if (kind === 'cross-person')
      first.record.scope = { kind: 'scene', sceneId: 'scene', personId: 'another-person' }
    const queue = f.make()
    try {
      queue.notify(f.dirty())
      await queue.whenIdle()
      assert.equal(f.calls(), 0)
    } finally {
      queue.dispose()
      f.db.close()
    }
  }
})

test('trusted common anchor permits only episode-mode mixed subjects and does not expose a title body', () => {
  const f = fixture()
  try {
    f.views.get(f.selections[0]!.ref.id)!.record.scope = {
      kind: 'scene',
      sceneId: 'scene',
      personId: 'another-person',
    }
    const input = captureSemanticInput('scene', {
      ...f.resolved(),
      episodeAnchor: { id: 'exact-event', version: 1 },
      episodeMembershipKey: semanticHash(['exact-event', 1]),
      anchorQualifications: [
        {
          edgeId: 'edge',
          edgeVersion: 1,
          registryVersion: 1,
          predicate: 'participates_in',
          subject: { kind: 'person', personId: 'person' },
          object: {
            kind: 'entity',
            entityId: 'exact-event',
            entityKind: 'event',
            backing: f.selections[0]!.ref,
            originEdgeId: 'edge',
          },
          polarity: 'negative',
          modality: 'possible',
          temporalScope: 'past',
          conditions: ['Only an attributed association'],
          occurredAt: null,
          validFrom: null,
          validUntil: null,
          status: 'candidate',
          admission: 'qualified_candidate',
          interpretation: 'attributed_proposed_association_not_observed_event',
        },
      ],
      anchorDependencies: f.selections.map((selection) => ({
        ref: selection.ref,
        fingerprint: semanticFingerprint(f.views.get(selection.ref.id)!.record),
        proofs: f.views.get(selection.ref.id)!.proofs,
      })),
    })!
    assert.equal(input.episodeOnly, true)
    assert.equal(input.personId, null)
    assert.deepEqual(input.episodeAnchor, { id: 'exact-event', version: 1 })
    const changed = { ...f.resolved(), episodeAnchor: { id: 'exact-event', version: 2 } }
    assert.equal(semanticInputUnchanged(input, changed), false)
  } finally {
    f.db.close()
  }
})

test('actual native Router and budget ledger receive maintenance request, full parent tool selection commits', async () => {
  for (const accounting of ['off', 'on', 'revoked'] as const) {
    const f = fixture()
    let nativeCalls = 0
    const server = createServer(async (req, res) => {
      nativeCalls++
      const chunks: Buffer[] = []
      for await (const chunk of req) chunks.push(Buffer.from(chunk))
      const body = JSON.parse(Buffer.concat(chunks).toString())
      const wire = JSON.parse(body.messages[1].content)
      res.setHeader('content-type', 'application/json')
      res.end(
        JSON.stringify({
          choices: [
            {
              finish_reason: 'tool_calls',
              message: {
                role: 'assistant',
                content: '',
                tool_calls: [
                  {
                    id: 'synthetic',
                    type: 'function',
                    function: {
                      name: body.tools[0].function.name,
                      arguments: JSON.stringify({
                        kind: 'summary_candidate',
                        selectedParentIds: wire.declarations
                          .slice(0, 2)
                          .map((item: { ref: { id: string } }) => item.ref.id),
                        text: 'Bounded synthetic summary with attached originals.',
                        hypothesis: false,
                      }),
                    },
                  },
                ],
              },
            },
          ],
          usage: {
            prompt_tokens: 10,
            completion_tokens: 2,
            total_tokens: 12,
            prompt_tokens_details: { cached_tokens: 0, cache_write_tokens: 0 },
          },
        }),
      )
    })
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    const ledger = new BudgetLedger(f.db, { bootId: 'synthetic-semantic-budget' })
    const gate = new NativeBudgetGate({
      ledger,
      policy: () => ({ dailyNanoUsd: 1_000_000_000, maxUnresolvedNanoUsd: 1_000_000_000 }),
      mode: 'strict',
      maxOutputTokens: 64,
      contract: () => ({
        version: 'synthetic-v1',
        outputCapHonored: true,
        inputCategories: ['inputUsdPerMillion'],
      }),
      estimator: ({ outputCap, contract }) => ({
        source: 'verified_contract',
        estimatorVersion: 'synthetic-v1',
        contractVersion: contract.version,
        enforceable: true,
        inputUpperTokens: 1000,
        outputUpperTokens: outputCap,
        inputCategories: contract.inputCategories,
      }),
    })
    f.ports.router = (guard) =>
      new Router(
        {
          providers: [
            {
              id: 'p',
              name: 'synthetic',
              baseUrl: `http://127.0.0.1:${(server.address() as { port: number }).port}`,
              protocol: 'openai-chat',
              credentialEnv: '',
              retryCount: 0,
              timeoutMs: 1000,
            },
          ],
          models: [
            {
              id: 'm',
              displayName: 'synthetic',
              providerId: 'p',
              apiModelName: 'synthetic',
              capabilities: ['text', 'tools'],
              ...Object.fromEntries(priceKeys.map((key) => [key, 1])),
            },
          ],
          routes: [{ role: 'learning', orderedModelIds: ['m'], timeoutMs: 3000 }],
        },
        undefined,
        undefined,
        undefined,
        () => undefined,
        undefined,
        () => {
          if (accounting === 'revoked') f.scene.memoryReadRevision++
          guard()
          return accounting === 'on' ? gate : undefined
        },
      )
    const queue = f.make()
    try {
      queue.notify(f.dirty())
      await queue.whenIdle()
      assert.equal(nativeCalls, accounting === 'revoked' ? 0 : 1)
      assert.equal(f.service.list(ctx).items.length, accounting === 'revoked' ? 0 : 1)
      const row = f.db.prepare('SELECT id FROM budget_attempts').get() as { id: string } | undefined
      if (accounting === 'on') {
        const receipt = ledger.get(row!.id)!
        assert.equal(receipt.request.lane, 'maintenance')
        assert.equal(receipt.state, 'complete')
      } else assert.equal(row, undefined)
    } finally {
      queue.dispose()
      server.closeAllConnections()
      await new Promise<void>((resolve) => server.close(() => resolve()))
      f.db.close()
    }
  }
})

test('separate subject partitions in one scene each run, never silently consume another person dirty input', async () => {
  const f = fixture()
  const original = [...f.selections]
  for (const [index, selection] of original.entries()) {
    const id = `other-person-assertion-${index}`
    const assertion = structuredClone(f.views.get(selection.ref.id)!)
    assertion.record.id = id
    assertion.record.scope = { kind: 'scene', sceneId: 'scene', personId: 'other-person' }
    assertion.dependencyKey = semanticHash(id)
    assertion.roots = [semanticHash(['other-person', id])]
    f.views.set(id, assertion)
    f.selections.push({
      ref: { id, version: 1, deletionEpoch: 0 },
      dependencyKey: assertion.dependencyKey,
    })
  }
  const resolvePerson = (personId: string | null): SemanticResolvedInput => ({
    coverage: 'complete',
    partitionKey: f.partitionKey(personId),
    entries: f
      .resolved()
      .entries.filter(
        ({ assertion }) =>
          assertion.record.scope.kind === 'scene' && assertion.record.scope.personId === personId,
      ),
  })
  f.ports.resolveDirty = (dirty) => resolvePerson(dirty[0]!.personId)
  f.ports.valid = (snapshot) => semanticInputUnchanged(snapshot, resolvePerson(snapshot.personId))
  const queue = f.make()
  try {
    queue.notify(f.dirty())
    queue.notify({ ...f.dirty(), personId: 'other-person', trigger: f.selections[3]! })
    await queue.whenIdle()
    assert.equal(f.calls(), 2)
    const candidates = f.service.list(ctx).items.map((view) => view.candidate)
    assert.deepEqual(candidates.map((value) => value.personId).sort(), ['other-person', 'person'])
    assert.ok(candidates.every((value) => value.parents.length === 3))
    assert.equal(queue.stats().pending, 0)
  } finally {
    queue.dispose()
    f.db.close()
  }
})

test('new relevant declaration during await revokes the first partition snapshot; fresh quiet input can generate', async () => {
  const f = fixture()
  let release!: () => void, entered!: () => void
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  const started = new Promise<void>((resolve) => {
    entered = resolve
  })
  const original = f.ports.router
  const first = f.resolved()
  // Rechecking the same delivered parents must still detect new partition membership.
  f.ports.valid = (snapshot) =>
    semanticInputUnchanged(snapshot, {
      ...first,
      partitionKey: f.partitionKey('person'),
    })
  f.ports.router = (guard) => ({
    complete: async (...args) => {
      entered()
      await held
      return original(guard).complete(...args)
    },
  })
  const queue = f.make()
  try {
    queue.notify(f.dirty())
    await started
    const id = 'new-relevant-declaration-during-await'
    const added = structuredClone(f.views.get(f.selections[0]!.ref.id)!)
    added.record.id = id
    added.record.content = 'An additional relevant exception, not present in the first input.'
    added.dependencyKey = semanticHash(id)
    added.roots = [semanticHash(['scene', id])]
    f.views.set(id, added)
    f.selections.push({
      ref: { id, version: 1, deletionEpoch: 0 },
      dependencyKey: added.dependencyKey,
    })
    release()
    await queue.whenIdle()
    assert.equal(f.service.list(ctx).items.length, 0)
    assert.equal(queue.stats().abandoned, 1)
    f.ports.router = original
    f.ports.valid = (snapshot) => semanticInputUnchanged(snapshot, f.resolved())
    queue.notify({ ...f.dirty(), trigger: f.selections[3]! })
    await queue.whenIdle()
    const result = f.service.list(ctx)
    assert.equal(result.coverage, 'complete')
    assert.equal(result.items.length, 1)
    assert.equal(result.items[0]!.candidate.parents.length, 4)
    assert.equal(result.items[0]!.candidate.partitionKey, f.partitionKey('person'))
  } finally {
    release()
    queue.dispose()
    f.db.close()
  }
})
