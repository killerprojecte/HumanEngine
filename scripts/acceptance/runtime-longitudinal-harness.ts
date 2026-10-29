import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { setTimeout as delay } from 'node:timers/promises'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'
import { installAdapter, type AdapterContext } from '../../src/plugins/adapters.ts'
import type { ChatMessage, ToolCall, ToolDefinition } from '../../src/models.ts'
import type { RuntimeMessage } from '../../src/runtime.ts'
import type { SceneRef, Message } from '../../src/onebot.ts'
import type { CommitmentView } from '../../src/memory/domain/commitments.ts'
import {
  longitudinalEvents,
  longitudinalFacts as facts,
  type LongitudinalEvent,
  type LongitudinalVariant,
} from './runtime-longitudinal-fixture.ts'

type Json = Record<string, any>
export interface LongitudinalCounters {
  decision: number
  planner: number
  replyer: number
  learning: number
  sends: number
}
export interface SelectedEvidence {
  toolName: string
  memoryContents: string[]
  stateKinds: string[]
  confirmations: string[]
  qualifications: string[]
  timeQualifications: string[]
}
export interface ReadObservation {
  evidence: SelectedEvidence[]
  coverageReasons: string[]
  bytes: number
}
type Mode =
  | { kind: 'read'; query: 'person' | 'plan'; disabled?: boolean }
  | { kind: 'command'; event: LongitudinalEvent }
  | { kind: 'idle' }
const toolCall = (name: string, args: Json): ToolCall => ({ id: name, name, arguments: args })
const empty = (toolCalls: ToolCall[] = []) => ({
  text: '',
  toolCalls: toolCalls.map((call, index) => ({ ...call, id: `${call.id}-${index}` })),
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
})
const textOf = (message: Json) =>
  (message.parts ?? [])
    .filter((p: Json) => p.type === 'text')
    .map((p: Json) => p.text)
    .join('\n')
const terms = (hour: string) => ({
  kind: 'plan',
  title: 'SYNTHETIC_JOINT_ARRANGEMENT',
  participantIds: ['alice', 'bob'],
  temporalScope: 'future',
  polarity: 'positive',
  modality: 'asserted',
  conditions: [],
  timeQualification: `${hour}; date and timezone unknown`,
})
const candidate = (id: string, content: string, commitment?: Json) => ({
  content,
  kind: 'claim',
  epistemicKind: 'explicit',
  subject: { kind: 'self', sourceMessageId: id },
  sourceMessageIds: [id],
  ...(commitment ? { commitment } : {}),
})
function eventFrom(message: Json) {
  return longitudinalEvents.find((event) => textOf(message).startsWith(`[fixture:${event.id}]`))
}
function data(messages: ChatMessage[]) {
  return JSON.parse(messages.find((m) => m.role === 'user')!.content) as Json
}
function receipts(messages: ChatMessage[]): Json[] {
  return messages.filter((m) => m.role === 'tool').map((m) => JSON.parse(m.content))
}
function makePlan(messages: ChatMessage[], patch: Json = {}) {
  const contract = data(messages).planContract
  if (!contract) throw Error('longitudinal_missing_plan_contract')
  return {
    action: 'reply',
    deliveryTool: 'messages.send',
    ...contract,
    strategy: 'Use only the selected qualified synthetic evidence.',
    constraints: [
      'Unknown occurrence dates stay unknown. Declarations are not verified real-world outcomes.',
    ],
    facts: [],
    assets: [],
    emoji: [],
    memorySelections: [],
    stateEvidenceIds: [],
    expressionGoals: ['Preserve qualifications.'],
    actions: [],
    ...patch,
  }
}
async function until(check: () => boolean, code: string, limitMs = 7000) {
  const deadline = performance.now() + limitMs
  while (!check()) {
    if (performance.now() >= deadline) throw Error(code)
    await delay(10)
  }
}

/** Real Engine/Runtime/SQLite and managed Router; the only protocol is a local scripted object. */
export function longitudinalHarness(variant: LongitudinalVariant) {
  const root = mkdtempSync(join(tmpdir(), 'he-longitudinal-')),
    path = join(root, 'fixture.sqlite')
  let store: Store,
    engine: Engine,
    adapter: AdapterContext,
    registration: ReturnType<typeof installAdapter>,
    protocolController: AbortController
  let scene: SceneRef,
    mode: Mode = { kind: 'idle' },
    querySequence = 0,
    lastRead: ReadObservation | undefined
  const originals = new Map<string, RuntimeMessage>(),
    errors: string[] = []
  const calls: LongitudinalCounters = { decision: 0, planner: 0, replyer: 0, learning: 0, sends: 0 }
  const ctx = () => ({ sceneId: scene.id, simulation: false, provenance: 'source-only' as const })
  const configureScene = (patch: Json) => {
    const updated = { ...store.scene(scene.id)!, ...patch }
    store.saveScene(updated)
    engine.sceneChanged(updated)
  }
  const configureLearning = (enabled: boolean, quiet = 100) => {
    store.set('settings', {
      ...store.settings,
      learningBatchMs: quiet,
      learningMaxWaitMs: quiet,
      learningMinIntervalMs: 0,
    })
    engine.settingsChanged()
    configureScene({ participationEnabled: false, learningEnabled: enabled && variant.memory })
  }
  function extract(messages: ChatMessage[], tools: ToolDefinition[]) {
    const input = data(messages)
    if (tools.some((tool) => tool.name === 'learning.reconcile')) {
      const correction = (input.candidates ?? []).find(
        (item: Json) => item.candidate?.content === facts.tea,
      )
      const old = (input.relatedMemories ?? []).find(
        (item: Json) =>
          item.content === facts.coffee &&
          item.canRevise &&
          item.candidateIndexes.includes(correction?.candidateIndex),
      )
      return empty(
        correction && old
          ? [
              toolCall('learning.reconcile', {
                candidateIndex: correction.candidateIndex,
                targetId: old.id,
                action: 'supersede',
              }),
            ]
          : [],
      )
    }
    if (!tools.some((tool) => tool.name === 'learning.propose')) return empty()
    return empty(
      (input.messages ?? []).flatMap((message: Json) => {
        const event = eventFrom(message)
        if (!event) return []
        if (event.kind === 'propose' && variant.qualified)
          return [
            toolCall(
              'learning.propose',
              candidate(message.id, 'Synthetic sourced plan proposal.', {
                kind: 'propose',
                terms: terms('18:00'),
              }),
            ),
          ]
        if (['confirm', 'amend'].includes(event.kind) && variant.qualified) return []
        const content =
          event.kind in facts
            ? facts[event.kind as keyof typeof facts]
            : `Source declaration: ${event.person} — ${event.text}`
        return [toolCall('learning.propose', candidate(message.id, content))]
      }),
    )
  }
  function plan(messages: ChatMessage[]) {
    const read = receipts(messages)
    if (mode.kind === 'command') {
      if (!read.length) return empty([toolCall('memory.commitments', {})])
      const states = read.at(-1)!.value.map((item: Json) => item.view) as CommitmentView[]
      const latest = states.toSorted((a, b) => b.ref.version - a.ref.version)[0]
      if (!latest) throw Error('longitudinal_missing_command_state')
      const event = mode.event,
        contract = makePlan(messages)
      const command =
        event.kind === 'amend'
          ? {
              kind: 'amend',
              target: latest.ref,
              terms: { ...latest.terms!, timeQualification: terms('19:00').timeQualification },
            }
          : { kind: 'confirm', target: latest.ref }
      return empty([
        toolCall('reply.plan', {
          ...contract,
          action: 'noReply',
          stateEvidenceIds: [read.at(-1)!.id],
          actions: [
            {
              toolName: 'learning.propose',
              arguments: candidate(
                contract.contextMessageIds.at(-1),
                'Synthetic exact-source command.',
                command,
              ),
            },
          ],
        }),
      ])
    }
    if (mode.kind !== 'read') throw Error('longitudinal_unexpected_planner')
    if (mode.disabled || !variant.memory) return empty([toolCall('reply.plan', makePlan(messages))])
    if (!read.length)
      return empty([
        toolCall(
          mode.query === 'plan' && variant.qualified
            ? 'memory.commitments'
            : variant.qualified
              ? 'memory.person'
              : 'memory.search',
          variant.qualified
            ? mode.query === 'plan'
              ? {}
              : { personId: 'alice' }
            : {
                query: mode.query === 'plan' ? 'arrangement' : 'preference coffee tea water',
                limit: 12,
              },
        ),
      ])
    if (mode.query === 'plan' && variant.qualified && read.length === 1) {
      const latest = read[0]!.value.toSorted(
        (a: Json, b: Json) => b.view.ref.version - a.view.ref.version,
      )[0]
      if (latest) return empty([toolCall('memory.commitmentTimeline', latest.view.ref)])
    }
    const structured = read.filter(
      (item) =>
        item.toolName === 'memory.commitments' || item.toolName === 'memory.commitmentTimeline',
    )
    const ordinary = read.filter(
      (item) => ['memory.person', 'memory.search'].includes(item.toolName) && item.value.length,
    )
    return empty([
      toolCall(
        'reply.plan',
        makePlan(messages, {
          stateEvidenceIds: structured.map((item) => item.id),
          memorySelections: ordinary.map((item) => ({
            evidenceId: item.id,
            memoryIds: item.value.slice(0, 12).map((record: Json) => record.id),
          })),
        }),
      ),
    ])
  }
  function capture(messages: ChatMessage[]) {
    const input = data(messages),
      wire = messages.find((m) => m.role === 'user')!.content
    const observations: SelectedEvidence[] = (input.evidence ?? []).map((item: Json) => {
      const views = (item.states ?? []).flatMap((state: Json) =>
        state.stateKind === 'commitment'
          ? [state.view]
          : (state.recordedStates ?? []).flatMap((recorded: Json) => recorded.states ?? []),
      )
      return {
        toolName: item.toolName,
        memoryContents: (item.memories ?? []).map((record: Json) => record.content),
        stateKinds: (item.states ?? []).map((state: Json) => state.stateKind),
        confirmations: views.map((view: Json) => view.confirmation),
        qualifications: views.map((view: Json) => view.qualification),
        timeQualifications: views.flatMap((view: Json) =>
          view.terms?.timeQualification ? [view.terms.timeQualification] : [],
        ),
      }
    })
    lastRead = {
      evidence: observations,
      coverageReasons: [
        ...new Set<string>([
          ...(input.memoryEvidenceCoverage?.omitted ?? []).map((item: Json) => item.reason),
          ...(input.evidence ?? []).flatMap((item: Json) =>
            (item.states ?? []).flatMap((state: Json) => [
              ...(state.reasons ?? []),
              ...(state.coverage === 'limited' ? ['limited_retained_coverage'] : []),
              ...((state.sourceBodyCoverage ?? state.view?.sourceBodyCoverage) === 'unavailable'
                ? ['source_body_unavailable']
                : []),
            ]),
          ),
        ]),
      ],
      bytes: new TextEncoder().encode(wire).length,
    }
  }
  function open() {
    store = new Store(path)
    if (!store.get('longitudinal_initialized', false)) {
      store.set('settings', {
        ...store.settings,
        sourceEventRevisionsEnabled: true,
        memoryCommitmentsEnabled: variant.qualified,
        memoryCommitmentsAnswerEnabled: variant.qualified,
        memoryHistoricalReadEnabled: variant.qualified,
        learningBatchMs: 100,
        learningMaxWaitMs: 100,
        learningMinIntervalMs: 0,
        debounceMs: 0,
        maxWaitMs: 100,
      })
      store.set('models', {
        providers: [
          {
            id: 'synthetic',
            name: 'Local script only',
            protocol: 'acceptance:scripted',
            baseUrl: 'http://127.0.0.1:1',
            credentialEnv: '',
            timeoutMs: 5000,
            retryCount: 0,
          },
        ],
        models: ['planner', 'replyer', 'learning'].map((role) => ({
          id: role,
          displayName: role,
          providerId: 'synthetic',
          apiModelName: role,
          capabilities: ['text', 'tools'],
        })),
        routes: ['planner', 'replyer', 'learning'].map((role) => ({
          role,
          orderedModelIds: [role],
          timeoutMs: 5000,
        })),
      })
      store.set('longitudinal_initialized', true)
    }
    engine = new Engine(store)
    protocolController = new AbortController()
    engine.protocols.register(
      'acceptance:scripted',
      'Synthetic contract script; not semantic scoring',
      {
        async complete(context, messages, tools) {
          try {
            if (tools.some((tool) => tool.name === 'participation.decide')) {
              calls.decision++
              return empty([
                toolCall('participation.decide', {
                  action: 'reply',
                  reasonCode: 'synthetic_acceptance',
                }),
              ])
            }
            const role = context.model?.apiModelName
            if (role === 'learning') {
              calls.learning++
              return extract(messages, tools)
            }
            if (tools.some((tool) => tool.name === 'reply.plan')) {
              calls.planner++
              return plan(messages)
            }
            if (role === 'replyer') {
              calls.replyer++
              capture(messages)
              return empty()
            }
            throw Error('longitudinal_unexpected_model_role')
          } catch (error) {
            errors.push(error instanceof Error ? error.message : 'script_error')
            throw error
          }
        },
      },
      protocolController.signal,
    )
    registration = installAdapter(
      'longitudinal',
      (context) => {
        adapter = context
        return {
          async send() {
            calls.sends++
            return {
              status: 'sent' as const,
              messageId: context.messageId(scene, `bot-${calls.sends}`),
            }
          },
          async history() {
            return { items: [], coverage: 'complete' as const }
          },
          async getMessage() {
            return { items: [], coverage: 'complete' as const }
          },
          dispose() {},
        }
      },
      engine.runtime,
      store,
      new AbortController().signal,
      {},
    )
    scene = adapter.scene('synthetic-account', 'group', 'longitudinal-group')
    if (!store.scene(scene.id))
      store.saveScene({
        ...scene,
        name: 'Synthetic longitudinal scope',
        participationEnabled: false,
        learningEnabled: variant.memory,
        proactiveEnabled: false,
        memoryReadEnabled: variant.memory,
        personalizationEnabled: true,
      })
  }
  function message(event: LongitudinalEvent): RuntimeMessage {
    return {
      id: adapter.messageId(scene, event.id),
      sceneId: scene.id,
      senderId: event.person,
      senderDisplayName: 'Same nickname',
      origin: 'live',
      status: 'available',
      sentAt: event.eventDate,
      receivedAt: new Date().toISOString(),
      parts: [
        { type: 'text', text: `[fixture:${event.id}] ${event.text}` },
        { type: 'mention', userId: event.person === 'alice' ? 'bob' : 'alice' },
      ],
    }
  }
  function receive(event: LongitudinalEvent) {
    const source = message(event)
    adapter.receive(source as Message, scene, false)
    const accepted = engine.runtime
      .preview(scene.id)
      .find((m) => m.id === source.id) as RuntimeMessage
    if (!accepted) throw Error('longitudinal_source_not_received')
    originals.set(event.id, structuredClone(accepted))
    return accepted
  }
  async function idleLearning() {
    await until(() => {
      const status = engine.learningStatus()
      return status.active === 0 && status.queued === 0
    }, 'longitudinal_learning_timeout')
    await engine.contextualLearning.queue.whenIdle()
    const status = engine.learningStatus()
    if (status.failed || status.paused || errors.length) throw Error('longitudinal_learning_failed')
  }
  async function stop() {
    registration.dispose()
    protocolController.abort()
    await engine.dispose()
    store.close()
  }
  open()
  return {
    calls,
    errors,
    originals,
    get store() {
      return store
    },
    get engine() {
      return engine
    },
    get scene() {
      return scene
    },
    records: () => engine.memory.list(ctx()),
    plans: () => engine.memory.commitments.list(ctx()).items,
    billingDay: () => engine.budget.coverage().windowStart,
    async learn(events: readonly LongitudinalEvent[]) {
      mode = { kind: 'idle' }
      configureLearning(true)
      for (const event of events) receive(event)
      await idleLearning()
    },
    async query(kind: 'person' | 'plan', disabled = false) {
      configureLearning(false)
      configureScene({ participationEnabled: true })
      mode = { kind: 'read', query: kind, disabled }
      lastRead = undefined
      const event = {
        id: `query-${++querySequence}`,
        person: 'alice',
        eventDate: '2026-10-06T12:00:00Z',
        kind: 'query',
        text: 'What does the currently authorized retained evidence say?',
      } as unknown as LongitudinalEvent
      const before = calls.replyer
      receive(event)
      await until(
        () => calls.replyer > before && engine.runtime.activeTasks === 0,
        'longitudinal_query_timeout',
      )
      const delivered = lastRead as ReadObservation | undefined
      if (errors.length || !delivered) throw Error('longitudinal_query_failed')
      return structuredClone(delivered)
    },
    async command(event: LongitudinalEvent) {
      if (!variant.qualified) {
        await this.learn([event])
        return
      }
      configureLearning(true, 300000)
      configureScene({ participationEnabled: true })
      mode = { kind: 'command', event }
      const completed = engine.contextualLearning.queue.stats().completed,
        decisions = calls.decision
      receive(event)
      await until(
        () =>
          calls.decision > decisions &&
          engine.runtime.activeTasks === 0 &&
          engine.contextualLearning.queue.stats().completed > completed,
        'longitudinal_command_timeout',
      )
      await engine.contextualLearning.queue.whenIdle()
      configureLearning(false)
      if (errors.length) throw Error('longitudinal_command_failed')
    },
    editOriginal() {
      configureLearning(false)
      const old = originals.get('alice-original')!
      return engine.runtime.edit(
        {
          ...old,
          parts: [
            {
              type: 'text',
              text: 'Edited synthetic source: the earlier declaration is no longer this message.',
            },
          ],
        },
        scene,
        { editId: 'edit-v2', order: { kind: 'observed_cas', expectedBase: old.sourceEventRef! } },
        false,
      )
    },
    recall(id: string) {
      engine.runtime.recall(scene.id, adapter.messageId(scene, id))
    },
    readEnabled(enabled: boolean) {
      configureScene({ memoryReadEnabled: enabled && variant.memory })
    },
    qualifiedEnabled(enabled: boolean) {
      store.set('settings', {
        ...store.settings,
        memoryCommitmentsEnabled: enabled && variant.qualified,
        memoryCommitmentsAnswerEnabled: enabled && variant.qualified,
        memoryHistoricalReadEnabled: enabled && variant.qualified,
      })
      engine.settingsChanged()
    },
    async reopen() {
      const before = JSON.stringify(calls)
      await stop()
      open()
      await delay(130)
      if (JSON.stringify(calls) !== before) throw Error('longitudinal_restart_replayed_model')
    },
    async close() {
      try {
        await stop()
      } finally {
        rmSync(root, { recursive: true, force: true })
      }
    },
  }
}
