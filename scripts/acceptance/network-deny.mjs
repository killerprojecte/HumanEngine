import net from 'node:net'
import http from 'node:http'
import https from 'node:https'
import tls from 'node:tls'
import dgram from 'node:dgram'
import dns from 'node:dns'
import { syncBuiltinESMExports } from 'node:module'
const allowed = (host) =>
  ['127.0.0.1', '::1', '[::1]', 'localhost'].includes(String(host).toLowerCase())
const check = (host) => {
  if (!allowed(host)) throw new Error('acceptance_external_network_denied')
}
const originalConnect = net.Socket.prototype.connect
net.Socket.prototype.connect = function (...args) {
  let first = args[0]
  if (Array.isArray(first)) first = first[0]
  if (typeof first === 'object' && first !== null) {
    if (first.path) throw new Error('acceptance_unix_network_denied')
    check(first.host ?? 'localhost')
  } else if (typeof first === 'number') check(typeof args[1] === 'string' ? args[1] : 'localhost')
  else throw new Error('acceptance_network_shape_denied')
  return originalConnect.apply(this, args)
}
for (const module of [http, https])
  for (const method of ['request', 'get']) {
    const original = module[method]
    module[method] = function (...args) {
      const input = args[0]
      if (typeof input === 'string' || input instanceof URL) check(new URL(input).hostname)
      else {
        if (input?.socketPath) throw new Error('acceptance_unix_network_denied')
        check(input?.hostname ?? input?.host ?? 'localhost')
      }
      return original.apply(this, args)
    }
  }
const originalFetch = globalThis.fetch
globalThis.fetch = function (input, ...args) {
  check(new URL(typeof input === 'string' || input instanceof URL ? input : input.url).hostname)
  return originalFetch(input, ...args)
}
const originalLookup = dns.lookup
dns.lookup = function (hostname, ...args) {
  check(hostname)
  return originalLookup.call(this, hostname, ...args)
}
// No selected fixture requires datagram traffic. TLS still reaches the guarded TCP socket.
dgram.createSocket = () => {
  throw new Error('acceptance_datagram_denied')
}
const originalTLS = tls.connect
tls.connect = function (...args) {
  const first = args[0]
  check(
    typeof first === 'object'
      ? (first.host ?? 'localhost')
      : typeof args[1] === 'string'
        ? args[1]
        : 'localhost',
  )
  return originalTLS.apply(this, args)
}
syncBuiltinESMExports()
