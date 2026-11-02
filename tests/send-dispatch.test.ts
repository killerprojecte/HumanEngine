import test from 'node:test'
import assert from 'node:assert/strict'
import { dispatchSegment, awaitSendStage } from '../src/runtime/dispatch.ts'
import { Store } from '../src/store.ts'
import type { RuntimeAdapter, TurnContext, ReplyGrounding } from '../src/runtime.ts'
import { getEventListeners } from 'node:events'
import { preserveOutcomeError, outcomeError } from '../src/diagnostics/error-context.ts'
import { installAdapter } from '../src/plugins/adapters.ts'
import type { Runtime } from '../src/runtime.ts'
import type { SceneRef, SendResult } from '../src/onebot.ts'

const segment = { parts: [{ type: 'text' as const, text: 'fixture' }] }
function fixture() {
  const store = new Store(':memory:')
  const controller = new AbortController()
  const context: TurnContext = {
    scene: {
      id: 's',
      adapterId: 'a',
      accountId: 'b',
      kind: 'group',
      platformSceneId: 'g',
    },
    mode: 'real',
    turnId: 't',
    dependencies: new Set(),
    validations: [],
    signal: controller.signal,
  }
  let sends = 0
  const run = (
    validate: () => Promise<boolean>,
    send: RuntimeAdapter['send'] = async () => ({
      status: 'sent',
      messageId: 'receipt',
    }),
  ) =>
    dispatchSegment(
      {
        store,
        eligible: () => !controller.signal.aborted,
        isRecalled: () => false,
        adapter: () =>
          ({
            send: (...args) => {
              sends++
              return send(...args)
            },
          }) as RuntimeAdapter,
      },
      context,
      segment,
      'o',
      {
        grounding: { check: () => undefined } as unknown as ReplyGrounding,
        validate,
      },
      undefined,
      0,
      1,
    )
  return { store, controller, context, run, sends: () => sends }
}

test('blocked validation aborts without reaching adapter; late rejection is consumed and spans close', async () => {
  const f = fixture()
  let reject!: (error: Error) => void
  const gate = new Promise<boolean>((_, fail) => {
    reject = fail
  })
  try {
    const result = f.run(() => gate)
    await new Promise((resolve) => setImmediate(resolve))
    assert(f.store.logs.some((row) => row.code === 'flow.send_validation.started'))
    assert(!f.store.logs.some((row) => row.code === 'flow.adapter_send.started'))
    const error = new Error('fixture_abort')
    f.controller.abort(error)
    const receipt = await result
    assert.equal(receipt.status, 'cancelled')
    assert.equal(outcomeError(receipt), error)
    reject(new Error('late_fixture_rejection'))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(f.sends(), 0)
    assert(f.store.logs.some((row) => row.code === 'flow.send_validation.cancelled'))
    assert(f.store.logs.some((row) => row.code === 'flow.send_segment.cancelled'))
    assert.equal(getEventListeners(f.controller.signal, 'abort').length, 0)
  } finally {
    f.store.close()
  }
})

test('noncooperative adapter abort is uncertain, never retried, and late receipt cannot become success', async () => {
  const f = fixture()
  let resolve!: (receipt: SendResult) => void
  const gate = new Promise<SendResult>((done) => {
    resolve = done
  })
  try {
    const result = f.run(
      async () => true,
      () => gate,
    )
    await new Promise((done) => setImmediate(done))
    f.controller.abort(new DOMException('deadline', 'TimeoutError'))
    const receipt = await result
    assert.equal(receipt.status, 'unknown')
    assert.equal(receipt.detail, 'request_timeout')
    resolve({ status: 'sent', messageId: 'late' })
    await new Promise((done) => setImmediate(done))
    assert.equal(f.sends(), 1)
    assert.equal(f.store.logs.filter((row) => row.code === 'flow.adapter_send.partial').length, 1)
    assert(!f.store.logs.some((row) => row.code === 'flow.adapter_send.completed'))
    assert.equal(getEventListeners(f.controller.signal, 'abort').length, 0)
  } finally {
    f.store.close()
  }
})

for (const status of ['sent', 'failed', 'unknown', 'cancelled'] as const) {
  test(`adapter ${status} has exactly one matching terminal span`, async () => {
    const f = fixture()
    try {
      assert.equal(
        (
          await f.run(
            async () => true,
            async () => ({ status }),
          )
        ).status,
        status,
      )
      const rows = f.store.logs.filter(
        (row) => row.stage === 'adapter_send' && row.status !== 'started',
      )
      assert.equal(rows.length, 1)
      assert.equal(rows[0]?.receiptState, status)
      assert.equal(getEventListeners(f.controller.signal, 'abort').length, 0)
    } finally {
      f.store.close()
    }
  })
}

test('freshness rejection never sends and ends both validation and dispatch', async () => {
  const f = fixture()
  try {
    assert.equal((await f.run(async () => false)).status, 'cancelled')
    assert.equal(f.sends(), 0)
    assert(f.store.logs.some((row) => row.code === 'flow.send_validation.cancelled'))
    assert(f.store.logs.some((row) => row.code === 'flow.send_segment.cancelled'))
  } finally {
    f.store.close()
  }
})

test('plugin boundary preserves allowlisted send reason and private error without exposing arbitrary detail', async () => {
  const store = new Store(':memory:')
  let adapter!: RuntimeAdapter, scope!: SceneRef
  const error = new Error('request_timeout')
  let receipt: SendResult = preserveOutcomeError(
    { status: 'unknown', detail: 'request_timeout' },
    error,
  )
  const runtime = {
    addAdapter: (_id: string, value: RuntimeAdapter) => {
      adapter = value
    },
  } as unknown as Runtime
  installAdapter(
    'fixture',
    (context) => {
      scope = context.scene('bot', 'group', 'group')
      return {
        send: async () => receipt,
        history: async () => ({ items: [], coverage: 'complete' }),
        getMessage: async () => ({ items: [], coverage: 'complete' }),
        dispose() {},
      }
    },
    runtime,
    store,
    new AbortController().signal,
    {},
  )
  try {
    const result = await adapter.send(scope, segment)
    assert.equal(result.status, 'unknown')
    assert.equal(result.detail, 'request_timeout')
    assert.equal(outcomeError(result), error)
    assert(!JSON.stringify(result).includes('stack'))
    receipt = { status: 'failed', detail: 'private-content-canary' }
    assert.equal((await adapter.send(scope, segment)).detail, undefined)
  } finally {
    store.close()
  }
})

test('plugin boundary preserves same-tick authoritative receipts but not late receipts after cancellation', async () => {
  const store = new Store(':memory:')
  let adapter!: RuntimeAdapter, scope!: SceneRef
  const caller = new AbortController()
  let sends = 0
  let implementation: () => Promise<SendResult> = async () => {
    caller.abort(new Error('same_tick_cancel'))
    return { status: 'sent', messageId: `${scope.id}:confirmed` }
  }
  installAdapter(
    'fixture',
    (context) => {
      scope = context.scene('bot', 'group', 'group')
      return {
        send: () => {
          sends++
          return implementation()
        },
        history: async () => ({ items: [], coverage: 'complete' }),
        getMessage: async () => ({ items: [], coverage: 'complete' }),
        dispose() {},
      }
    },
    {
      addAdapter: (_id: string, value: RuntimeAdapter) => {
        adapter = value
      },
    } as unknown as Runtime,
    store,
    new AbortController().signal,
    {},
  )
  try {
    const confirmed = await adapter.send(scope, segment, caller.signal)
    assert.equal(confirmed.status, 'sent')
    assert.equal(confirmed.messageId, `${scope.id}:confirmed`)
    assert.equal(getEventListeners(caller.signal, 'abort').length, 0)

    const stopped = await adapter.send(scope, segment, caller.signal)
    assert.equal(stopped.status, 'cancelled')
    assert.equal(sends, 1, 'an already cancelled call never invokes the adapter')

    const lateCaller = new AbortController()
    let finish!: (receipt: SendResult) => void
    implementation = () =>
      new Promise((resolve) => {
        finish = resolve
      })
    const pending = adapter.send(scope, segment, lateCaller.signal)
    await new Promise((resolve) => setImmediate(resolve))
    lateCaller.abort()
    const uncertain = await pending
    assert.equal(uncertain.status, 'unknown')
    finish({ status: 'sent', messageId: `${scope.id}:late` })
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(uncertain.status, 'unknown')
    assert.equal(sends, 2, 'cancellation does not resend')
    assert.equal(getEventListeners(lateCaller.signal, 'abort').length, 0)
  } finally {
    store.close()
  }
})

test('direct dispatch preserves a receipt settled during synchronous cancellation', async () => {
  const f = fixture()
  try {
    const result = await f.run(
      async () => true,
      async () => {
        f.controller.abort()
        return { status: 'sent', messageId: 'authoritative' }
      },
    )
    assert.equal(result.status, 'sent')
    assert.equal(f.sends(), 1)
  } finally {
    f.store.close()
  }
})

test('core-installed wrapper owns cancellation once across physical dispatch', async () => {
  const f = fixture()
  let installed!: RuntimeAdapter
  let sends = 0
  installAdapter(
    'fixture',
    (context) => {
      f.context.scene = context.scene('bot', 'group', 'group')
      return {
        send: async (scope) => {
          sends++
          f.controller.abort()
          return { status: 'sent', messageId: `${scope.id}:confirmed` }
        },
        history: async () => ({ items: [], coverage: 'complete' }),
        getMessage: async () => ({ items: [], coverage: 'complete' }),
        dispose() {},
      }
    },
    {
      addAdapter: (_id: string, adapter: RuntimeAdapter) => {
        installed = adapter
      },
    } as unknown as Runtime,
    f.store,
    new AbortController().signal,
    {},
  )
  try {
    const result = await dispatchSegment(
      {
        store: f.store,
        eligible: () => !f.controller.signal.aborted,
        isRecalled: () => false,
        adapter: () => installed,
      },
      f.context,
      segment,
      'o',
      {
        grounding: { check: () => undefined } as unknown as ReplyGrounding,
        validate: async () => true,
      },
      undefined,
      0,
      1,
    )
    assert.equal(result.status, 'sent')
    assert.equal(sends, 1)
    assert.equal(getEventListeners(f.controller.signal, 'abort').length, 0)
  } finally {
    f.store.close()
  }
})

test('stage arbitration rejects after abort before a later microtask settlement and cleans exceptional actions', async () => {
  const controller = new AbortController()
  let finish!: (value: string) => void
  const pending = awaitSendStage(
    () =>
      new Promise<string>((resolve) => {
        finish = resolve
      }),
    controller.signal,
  )
  const rejected = assert.rejects(pending, /cancelled/)
  controller.abort(new Error('cancelled'))
  await Promise.resolve()
  finish('too late')
  await rejected
  assert.equal(getEventListeners(controller.signal, 'abort').length, 0)
  let calls = 0
  await assert.rejects(
    awaitSendStage(async () => {
      calls++
      return 'never'
    }, controller.signal),
    /cancelled/,
  )
  assert.equal(calls, 0)
  for (const action of [
    () => {
      throw new Error('sync_failure')
    },
    () =>
      ({
        get then() {
          throw new Error('getter_failure')
        },
      }) as unknown as Promise<string>,
  ]) {
    const c = new AbortController()
    await assert.rejects(awaitSendStage(action, c.signal), /failure/)
    assert.equal(getEventListeners(c.signal, 'abort').length, 0)
  }
})

test('asynchronous validation self-abort records known cancellation, never a send attempt', async () => {
  const f = fixture()
  try {
    const result = await f.run(async () => {
      await Promise.resolve()
      f.controller.abort('context_changed')
      return false
    })
    assert.equal(result.status, 'cancelled')
    assert.equal(outcomeError(result), 'context_changed')
    assert.equal(f.sends(), 0)
    assert.equal(f.store.logs.filter((row) => row.code === 'flow.send_segment.cancelled').length, 1)
    assert(!f.store.logs.some((row) => row.code === 'flow.adapter_send.started'))
  } finally {
    f.store.close()
  }
})

test('non-cancellation validation failure still throws without reaching the adapter', async () => {
  const f = fixture()
  try {
    const error = new Error('validator_failed')
    await assert.rejects(
      f.run(async () => {
        throw error
      }),
      (actual) => actual === error,
    )
    assert.equal(f.sends(), 0)
    assert.equal(f.store.logs.filter((row) => row.code === 'flow.send_validation.failed').length, 1)
    assert.equal(getEventListeners(f.controller.signal, 'abort').length, 0)
  } finally {
    f.store.close()
  }
})

test('first-party final transmit gate rejects held encoding after eligibility, adapter, reply, grounding or topic changes', async () => {
  for (const change of ['eligibility', 'adapter', 'reply', 'grounding', 'topic', 'none']) {
    const f = fixture()
    let finish!: () => void
    let entered!: () => void
    const held = new Promise<void>((resolve) => {
      finish = resolve
    })
    const started = new Promise<void>((resolve) => {
      entered = resolve
    })
    let eligible = true,
      recalled = false,
      groundingFailure = false,
      version = 1,
      sends = 0
    const adapter = {
      send: async (_scene, _segment, _signal, beforeTransmit) => {
        entered()
        await held
        try {
          beforeTransmit?.()
        } catch {
          return { status: 'failed', detail: 'send_invalidated' }
        }
        sends++
        return { status: 'sent', messageId: 'receipt' }
      },
    } as RuntimeAdapter
    let current = adapter
    try {
      const result = dispatchSegment(
        {
          store: f.store,
          eligible: () => eligible,
          isRecalled: () => recalled,
          adapter: () => current,
        },
        f.context,
        { ...segment, replyToId: 'reply' },
        'o',
        {
          grounding: {
            check: () => (groundingFailure ? 'changed' : undefined),
          } as unknown as ReplyGrounding,
          validate: async () => true,
          captureTransmitGuard: () => {
            const validated = version
            return () => version === validated
          },
        },
        undefined,
        0,
        1,
      )
      await started
      if (change === 'eligibility') eligible = false
      if (change === 'adapter') current = { ...adapter }
      if (change === 'reply') recalled = true
      if (change === 'grounding') groundingFailure = true
      if (change === 'topic') version++
      finish()
      assert.equal((await result).status, change === 'none' ? 'sent' : 'failed', change)
      assert.equal(sends, change === 'none' ? 1 : 0, change)
    } finally {
      f.store.close()
    }
  }
})
