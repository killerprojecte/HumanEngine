import { EventEmitter, once } from 'node:events'
import { readFile, writeFile } from 'node:fs/promises'
import { createServer, get, maxHeaderSize } from 'node:http'
import { PassThrough } from 'node:stream'
import { fetchQqMedia } from '../../src/media-fetch.ts'

const publicDNS = ['2606:4700:4700::1111', '8.8.8.8']
const cases = [
  ['http', 'http://example.com:8080/image%2Fhere?fixture=1', [{ bytes: 'original' }]],
  ['https-chain', 'https://example.com/a/b', [
    { status: 302, location: '../middle' },
    { status: 307, location: 'https://cdn.example.com/image?fixture=1' },
    { bytes: 'original' },
  ]],
  ['ipv6-only', 'https://example.com/ipv6', [{ bytes: 'v6' }], [publicDNS[0]]],
  ['mixed-private', 'https://example.com/', [], [...publicDNS, '127.0.0.1']],
  ['mixed-mapped', 'https://example.com/', [], [...publicDNS, '::ffff:8.8.8.8']],
  ['mixed-wire-server', 'https://example.com/', [], [...publicDNS, '168.63.129.16']],
  ['empty-answer', 'https://example.com/', [], []],
  ['private-literal', 'http://127.0.0.1/', []],
  ['blocked-after-redirect', 'https://example.com/a', [
    { status: 302, location: 'https://192.0.2.77/never' },
  ]],
  ['credentialed', 'https://secret@example.com/', []],
  ['fragment', 'https://example.com/#never', []],
  ['local-file', 'file:///never', []],
  ['downgrade', 'https://example.com/', [{ status: 301, location: 'http://example.com/never' }]],
  ['empty-location', 'https://example.com/', [{ status: 302 }]],
  ['redirect-limit', 'https://example.com/', Array.from({ length: 4 }, () => ({ status: 308, location: '/next' }))],
  ['wrong-status', 'http://example.com/', [{ status: 206 }]],
  ['oversized-length', 'http://example.com/', [{ length: String(64 * 1024 * 1024 + 1) }]],
]
const results = []
for (const [name, url, responses, addresses = publicDNS] of cases) {
  const dns = []
  const requests = []
  const transport = {
    lookup: async (hostname) => {
      dns.push(hostname)
      return addresses.map((address) => ({ address, family: address.includes(':') ? 6 : 4 }))
    },
    get(options, callback) {
      const { hostname: address, port, family, servername, path, headers } = options
      const next = responses[requests.length]
      if (!next) throw Error('unexpected synthetic request')
      requests.push({ address, port: String(port), family, servername: servername ?? '', path, host: headers.Host })
      const req = new EventEmitter()
      req.destroy = () => {}
      const resp = Object.assign(new PassThrough(), {
        statusCode: next.status ?? 200,
        headers: {
          ...(next.location ? { location: next.location } : {}),
          ...(next.length ? { 'content-length': next.length } : {}),
        },
      })
      queueMicrotask(() => {
        callback(resp)
        resp.end(next.bytes ?? 'original')
      })
      return req
    },
  }
  let bytes = ''
  let error = ''
  try {
    bytes = (await fetchQqMedia(url, undefined, transport)).toString()
  } catch (e) {
    error = e.message
  }
  results.push({ name, url, responses, addresses, dns, requests, bytes, error })
}
// Exercise Node's actual parser and request framing against a loopback-only
// server. The production transport is not invoked for the synthetic host.
const liveRequests = []
const local = createServer((request, response) => {
  liveRequests.push({ method: request.method, url: request.url, host: request.headers.host })
  if (liveRequests.length === 1) {
    response.writeHead(302, { Location: '/final' }).end()
  } else {
    response.writeHead(200).end('loopback original')
  }
})
local.listen(0, '127.0.0.1')
await once(local, 'listening')
let liveBytes
try {
  liveBytes = await fetchQqMedia('http://example.com/start', undefined, {
    lookup: async () => [{ address: '8.8.8.8', family: 4 }],
    get: (options, callback) => get({
      ...options,
      hostname: '127.0.0.1',
      port: local.address().port,
      agent: false,
    }, callback),
  })
} finally {
  await new Promise((resolve, reject) => local.close((error) => error ? reject(error) : resolve()))
}
if (liveBytes.toString() !== 'loopback original' || liveRequests.length !== 2 ||
    liveRequests[0].url !== '/start' || liveRequests[1].url !== '/final' ||
    liveRequests.some(({ method, host }) => method !== 'GET' || host !== 'example.com')) {
  throw Error('actual Node media loopback framing changed')
}
const output = new URL('../plugins/snowluma/host/runtime/testdata/public-image-source.json', import.meta.url)
const body = JSON.stringify({ source: 'src/media-fetch.ts:fetchQqMedia', maxHeaderSize, cases: results }, null, 2) + '\n'
if (process.argv.includes('--check')) {
  if ((await readFile(output, 'utf8')) !== body) throw Error('SnowLuma public-image source fixture changed')
} else {
  await writeFile(output, body)
}
