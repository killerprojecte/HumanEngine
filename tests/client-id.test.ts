import assert from 'node:assert/strict'
import test from 'node:test'
import { createClientId } from '../app/utils/create-id.ts'

const pattern = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
test('provider/model/rule IDs work when insecure HTTP omits crypto.randomUUID', () => {
  const httpCrypto = {
    getRandomValues: (bytes: Uint8Array) => globalThis.crypto.getRandomValues(bytes),
  }
  assert.equal('randomUUID' in httpCrypto, false)
  const ids = new Set(Array.from({ length: 100 }, () => createClientId(httpCrypto)))
  assert.equal(ids.size, 100)
  for (const id of ids) assert.match(id, pattern)
})
test('fallback applies UUID version and variant bits without Math.random', () => {
  assert.equal(
    createClientId({ getRandomValues: (bytes) => bytes.fill(255) }),
    'ffffffff-ffff-4fff-bfff-ffffffffffff',
  )
})
test('native UUID is preferred and missing entropy fails with a fixed safe code', () => {
  const id = createClientId()
  assert.match(id, pattern)
  assert.throws(
    () => createClientId({} as Parameters<typeof createClientId>[0]),
    /^Error: random_source_unavailable$/,
  )
})
