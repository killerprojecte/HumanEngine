import test from 'node:test'
import assert from 'node:assert/strict'
import type { Dispatcher } from 'undici'
import { nativeStreamDispatcher } from '../src/models/stream-dispatcher.ts'

test('stream timeout override borrows the current dispatcher without changing global proxy or ownership', async () => {
  new Headers()
  const { getGlobalDispatcher, setGlobalDispatcher } = await import('undici')
  const original = getGlobalDispatcher()
  const calls: Array<[unknown, unknown]> = []
  const custom = {
    dispatch(options: Dispatcher.DispatchOptions, handler: Dispatcher.DispatchHandler) {
      assert.equal(this, custom)
      calls.push([options, handler])
      return true
    },
  } as Dispatcher
  try {
    setGlobalDispatcher(custom)
    const wrapper = await nativeStreamDispatcher()
    assert.equal(getGlobalDispatcher(), custom)
    const handler = {} as Dispatcher.DispatchHandler
    const options = {
      origin: 'https://example.invalid',
      path: '/v1/responses',
      method: 'POST' as const,
      bodyTimeout: 300000,
      headersTimeout: 123,
      headers: { 'x-test': 'fixture' },
    }
    assert.equal(wrapper.dispatch(options, handler), true)
    assert.deepEqual(calls[0]![0], { ...options, bodyTimeout: 0 })
    assert.equal(calls[0]![1], handler)
    assert.equal(options.bodyTimeout, 300000)
    assert.equal('close' in wrapper, false)
    assert.equal('destroy' in wrapper, false)
    assert.equal(getGlobalDispatcher(), custom)
  } finally {
    setGlobalDispatcher(original)
  }
})
