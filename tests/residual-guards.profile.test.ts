import test from 'node:test'
import assert from 'node:assert/strict'
import { Store } from '../src/store.ts'
import { Runtime, type RuntimeMessage } from '../src/runtime.ts'
import type { ChatMessage, Completion, Router, ToolDefinition } from '../src/models.ts'
import type { SceneRef } from '../src/onebot.ts'
import { fixtureReplyPlan } from './helpers/reply-plan.ts'

const result = (toolCalls: Completion['toolCalls']): Completion => ({
  text: '',
  toolCalls,
  modelId: 'fixture',
  usage: { inputTokens: 0, outputTokens: 0, totalTokens: 0 },
  assistant: { role: 'assistant', content: '', toolCalls },
})
const decision = (action: 'reply' | 'silent' = 'reply') =>
  result([
    {
      id: 'decision',
      name: 'participation.decide',
      arguments: { action, reasonCode: 'fixture', targetMessageIds: ['source'] },
    },
  ])
const plan = (messages: ChatMessage[], patch: Parameters<typeof fixtureReplyPlan>[1] = {}) =>
  result([{ id: 'plan', name: 'reply.plan', arguments: fixtureReplyPlan(messages, patch)! }])
const send = (operationId: string) => ({
  id: operationId,
  name: 'messages.send',
  arguments: {
    operationId,
    segments: [{ replyToId: 'source', parts: [{ type: 'text', text: 'A concise response.' }] }],
  },
})
const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
const scene: SceneRef = {
  id: 'planner-runtime',
  kind: 'group',
  adapterId: 'fixture',
  accountId: 'bot',
  platformSceneId: 'group',
}
function setup(
  complete: (
    role: string,
    messages: ChatMessage[],
    tools: ToolDefinition[],
    signal?: AbortSignal,
  ) => Promise<Completion>,
  store = new Store(':memory:'),
) {
  store.saveScene({
    ...scene,
    name: 'Fixture',
    participationEnabled: true,
    proactiveEnabled: true,
    learningEnabled: false,
  })
  const runtime = new Runtime(store, () => ({ complete }) as Router)
  let sends = 0
  runtime.addAdapter('fixture', {
    send: async () => {
      sends++
      return { status: 'sent', messageId: `sent-${sends}` }
    },
    getMessage: async () => ({ items: [], coverage: 'partial' }),
    history: async () => ({ items: [], coverage: 'partial' }),
    dispose() {},
  })
  const ingest = (id = 'source') => {
    const now = new Date().toISOString()
    const message: RuntimeMessage = {
      id,
      sceneId: scene.id,
      senderId: 'user',
      sentAt: now,
      receivedAt: now,
      origin: 'live',
      status: 'available',
      parts: [
        {
          type: 'text',
          text: id === 'source' ? 'Please respond.' : 'Correction: do not send that answer.',
        },
      ],
    }
    runtime.ingest(message, scene, false)
  }
  ingest()
  return {
    store,
    runtime,
    ingest,
    sends: () => sends,
    close: () => {
      runtime.dispose()
      store.close()
    },
  }
}

for (const chainLength of [100, 1000])
  test(
    `post-Replyer durable recovery benchmark chain=${chainLength}`,
    { timeout: 10000 },
    async () => {
      const { mkdtempSync, rmSync } = await import('node:fs')
      const { tmpdir } = await import('node:os')
      const { join } = await import('node:path')
      const { RecoveryAuthority, metadata, saveGate } =
        await import('../src/memory/recovery/ledger.ts')
      const { createHash } = await import('node:crypto')
      const hash = (text: string) => createHash('sha256').update(text).digest('hex')
      const root = mkdtempSync(join(tmpdir(), 'send-validation-recovery-'))
      const store = new Store(join(root, 'live', 'fixture.sqlite'))
      const originalEntries = RecoveryAuthority.prototype.entries
      const originalHead = RecoveryAuthority.prototype.head
      const originalLog = store.log.bind(store)
      let validationActive = false,
        headReads = 0,
        fullScans = 0,
        totalScans = 0,
        measuring = false,
        byStack: Record<string, number> = {},
        totalByStack: Record<string, number> = {},
        replyReturned = false
      let validationAt = 0,
        validationMs = 0,
        timerDelayMs = 0
      let pulse!: () => void
      const pulsed = new Promise<void>((done) => {
        pulse = done
      })
      RecoveryAuthority.prototype.entries = function () {
        if (measuring) {
          totalScans++
          const stack = new Error()
            .stack!.split('\n')
            .slice(3, 8)
            .map((s) => s.trim().replace(/ \(.*$/, ''))
            .join(' > ')
          totalByStack[stack] = (totalByStack[stack] ?? 0) + 1
        }
        if (validationActive) {
          fullScans++
          const stack = new Error()
            .stack!.split('\n')
            .slice(3, 8)
            .map((s) => s.trim().replace(/ \(.*$/, ''))
            .join(' > ')
          byStack[stack] = (byStack[stack] ?? 0) + 1
        }
        return originalEntries.call(this)
      }
      RecoveryAuthority.prototype.head = function () {
        if (validationActive) headReads++
        return originalHead.call(this)
      }
      store.log = (...args) => {
        const row = originalLog(...args)
        if (row.stage === 'send_validation') {
          if (row.status === 'started') {
            assert(replyReturned)
            // Seed only synthetic durable authority state, after prior pipeline work,
            // so this measurement isolates the reported post-Replyer boundary.
            const gate = metadata(store.db)!
            const authority = new RecoveryAuthority(gate.authority)
            let previous = gate.head.hash
            authority.db.transaction(() => {
              const insert = authority.db.prepare('INSERT INTO revocations VALUES(?,?,?,?)')
              for (let sequence = gate.head.sequence + 1; sequence <= chainLength; sequence++) {
                const payload = JSON.stringify({
                  kind: 'fingerprint',
                  value: hash(`fixture-${sequence}`),
                })
                const next = hash(`${gate.head.instanceId}|${sequence}|${previous}|${payload}`)
                insert.run(sequence, previous, payload, next)
                previous = next
              }
            })()
            saveGate(store.db, {
              ...gate,
              head: { ...gate.head, sequence: chainLength, hash: previous },
            })
            authority.close()
            validationAt = performance.now()
            setTimeout(() => {
              timerDelayMs = performance.now() - validationAt
              measuring = false
              pulse()
            }, 1)
            validationActive = true
            measuring = true
          } else if (row.status !== 'cancel_requested') {
            validationActive = false
            validationMs = performance.now() - validationAt
          }
        }
        return row
      }
      const fixture = setup(async (role, messages, tools) => {
        if (tools.some((tool) => tool.name === 'participation.decide')) return decision()
        if (role === 'planner') return plan(messages)
        if (messages.some((message) => message.role === 'tool')) return result([])
        replyReturned = true
        return result([send('durable-recovery-send')])
      }, store)
      try {
        for (let index = 0; index < 23; index++) fixture.ingest(`history-${index}`)
        await fixture.runtime.proactive(scene.id)
        assert.equal(fixture.sends(), 1)
        assert.equal(fullScans, 8, 'fixed source and dependency batches must remain bounded')
        await pulsed
        process.stdout.write(
          `SYNTHETIC_SEND_VALIDATION fullScans=${fullScans} totalScans=${totalScans} byStack=${JSON.stringify(byStack)} totalByStack=${JSON.stringify(totalByStack)} chain=${chainLength} headReads=${headReads} spanMs=${validationMs.toFixed(1)} timer1msFiredAfter=${timerDelayMs.toFixed(1)}ms\n`,
        )
      } finally {
        RecoveryAuthority.prototype.entries = originalEntries
        RecoveryAuthority.prototype.head = originalHead
        fixture.close()
        rmSync(root, { recursive: true, force: true })
      }
    },
  )
