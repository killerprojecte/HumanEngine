import {lookup} from 'node:dns/promises'
import {requestPublicResource} from '../../../src/network-request.ts'
import {validateMediaUrl} from '../../../src/media-fetch.ts'
import {strict as assert} from 'node:assert'
import {fstatSync} from 'node:fs'

const output = []
for (const unicode of [false, true]) for (const length of [255, 256, 257]) {
  // The oversized label prevents accidental useful public resolution. The
  // instrumented test returns EAI_FAIL before any actual libc/NSS work.
  const inputHost = unicode ? 'é.' + 'a'.repeat(length - 8) : 'a'.repeat(length)
  const url = `http://${inputHost}/`
  const hostname = validateMediaUrl(url).hostname
  assert.equal(hostname.length, length)
  const eventCount = () => process.env.HE_DNS_LENGTH_ONLY ? fstatSync(Number(process.env.HE_DNS_EVENTS)).size : null
  const before = eventCount()
  let code, message
  try { await lookup(hostname, {all:true}); assert.fail('unexpected DNS success') }
  catch (error) { code = error.code }
  if (length >= 256) assert.equal(code, 'EINVAL')
  try { await requestPublicResource(url); assert.fail('unexpected source success') }
  catch (error) { message = error.message }
  assert.equal(message, 'media_dns_failed')
  const nativeCalls = before === null ? null : eventCount() - before
  if (nativeCalls !== null) assert.equal(nativeCalls, length === 255 ? 2 : 0)
  output.push({unicode, length, code, message, nativeCalls})
}
console.log(JSON.stringify({node:process.version, uv:process.versions.uv, output}, null, 2))
