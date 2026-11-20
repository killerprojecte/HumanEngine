import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createApp } from '../../src/api.ts'
import { Engine } from '../../src/engine.ts'
import { Store } from '../../src/store.ts'

assert.equal(process.version, 'v24.22.1')
const paths = ['src/api.ts','src/admin.ts','src/embedding-index.ts','src/schema.ts']
const sourceHashes = Object.fromEntries(paths.map(path=>[path,createHash('sha256').update(readFileSync(path)).digest('hex')]))
const cwd = process.cwd()
const root = mkdtempSync(join(tmpdir(),'he-embedding-action-boundary-'))
const suppliedID = '12345678-1234-4123-8123-123456789012'
const known = '{"expectedJobId":null}'
const start = '{"expectedJobId":null,"expectedEncoderRevision":"r"}'
const json = 'application/json'
const requestCases = [
  ['unauthenticated-invalid-action', 'made-up', '{', {auth:false}],
  ['unauthenticated-known-action', 'resume', '{', {auth:false}],
  ['unauthenticated-base', '', undefined, {auth:false}],
  ['unauthenticated-method', 'start', undefined, {auth:false,method:'HEAD'}],
  ['unknown-valid-json', 'made-up', known], ['unknown-invalid-json', 'made-up', '{'],
  ['unknown-no-content-type', 'made-up', known, {type:null}],
  ['uppercase-action', 'Start', start], ['encoded-start', '%73tart', start],
  ['encoded-slash', 'start%2Fpause', start], ['trailing-slash', 'start/', start],
  ['surplus-path-component', 'start/extra', start], ['base-post', '', known],
  ['get-action', 'start', undefined, {method:'GET'}], ['head-action', 'start', undefined, {method:'HEAD'}],
  ['put-action', 'start', start, {method:'PUT'}],
  ['missing-content-type-with-body', 'pause', known, {type:null}],
  ['missing-body-and-content-type', 'pause', undefined, {type:null}],
  ['missing-body-json-content-type', 'pause'], ['empty-body-json-content-type', 'pause', ''],
  ['malformed-json', 'pause', '{'], ['trailing-json', 'pause', known+'{}'],
  ['wrong-content-type', 'pause', known, {type:'text/plain'}],
  ['charset-json', 'pause', known, {type:'application/json; charset=UTF-8'}],
  ['uppercase-content-type', 'pause', known, {type:'Application/JSON'}],
  ['suffix-json-content-type', 'pause', known, {type:'application/vnd.test+json'}],
  ['non-json-prefix', 'pause', known, {type:'x-application/json'}],
  ['utf8-bom-json', 'pause', '\uFEFF'+known], ['utf8-bom-only', 'pause', '\uFEFF'],
  ['primitive-null', 'pause', 'null'], ['array-json', 'pause', '[null]'], ['strict-extra', 'pause','{"expectedJobId":null,"extra":1}'],
  ['missing-expected-job', 'pause', '{}'], ['explicit-null-expected-job', 'pause', known],
  ['empty-expected-job', 'pause', '{"expectedJobId":""}'], ['nonstring-expected-job', 'pause', '{"expectedJobId":1}'],
  ['start-missing-encoder', 'start', known],
  ['start-empty-encoder', 'start', '{"expectedJobId":null,"expectedEncoderRevision":""}'],
  ['start-whitespace-encoder', 'start', '{"expectedJobId":null,"expectedEncoderRevision":" "}'],
  ['start-null-encoder', 'start', '{"expectedJobId":null,"expectedEncoderRevision":null}'],
  ['start-missing-job-before-semantic-check', 'start', '{}'],
  ['start-controls-last-declaration', 'start', '{"expectedJobId":false,"expectedJobId":null,"expectedEncoderRevision":"r"}'],
  ['resume-confirm-default', 'resume', '{"expectedJobId":"job"}'],
  ['resume-confirm-false', 'resume', '{"expectedJobId":"job","confirmUnknownRequest":false}'],
  ['resume-confirm-true', 'resume', '{"expectedJobId":"job","confirmUnknownRequest":true}'],
  ['resume-confirm-invalid-string', 'resume', '{"expectedJobId":"job","confirmUnknownRequest":"true"}'],
  ['resume-confirm-invalid-null', 'resume', '{"expectedJobId":"job","confirmUnknownRequest":null}'],
  ['start-irrelevant-confirm-invalid', 'start', '{"expectedJobId":null,"expectedEncoderRevision":"r","confirmUnknownRequest":null}'],
  ['cancel-irrelevant-encoder-valid', 'cancel', '{"expectedJobId":"job","expectedEncoderRevision":""}'],
  ['pause-private-owner-error', 'pause', '{"expectedJobId":"@fail"}'],
  ['resume-private-owner-error', 'resume', '{"expectedJobId":"@fail","confirmUnknownRequest":true}'],
  ['start-private-owner-error', 'start', '{"expectedJobId":"@fail","expectedEncoderRevision":"r"}'],
  ['cancel-private-owner-error', 'cancel', '{"expectedJobId":"@fail"}'],
  ['post-status-private-error', 'cancel', '{"expectedJobId":"@status-fail"}'],
]
let store, engine
const calls = []
let providerCalls = 0
let cases
const fetch = globalThis.fetch
try {
  process.chdir(root)
  globalThis.fetch = () => { providerCalls++; throw new Error('outbound network is disabled for this fixture') }
  store = new Store(join(root,'live/main.sqlite'))
  engine = new Engine(store)
  let statusFail = false
  const status = engine.embeddingIndex.status.bind(engine.embeddingIndex)
  const privateText = 'path https://fixture.invalid/never, raw-fixture-body, secret-fixture-redact'
  engine.embeddingIndex.status = () => {
    calls.push(['status'])
    if (statusFail) throw new Error(privateText)
    return status()
  }
  for (const op of ['start','resume','pause','cancel']) {
    engine.embeddingIndex[op] = (...args) => {
      calls.push([op,...args])
      if (args.includes('@fail')) throw new Error(privateText)
      if (args.includes('@status-fail')) statusFail = true
    }
  }
  const app = createApp(store,{engine,token:'synthetic-token'})
  cases = []
  for (const [name, action, body, options] of requestCases) {
    statusFail = false
    const {auth=true,method='POST',type=json} = options ?? {}
    const header = {'X-Request-ID':suppliedID}
    if (auth) header.Authorization = 'Bearer synthetic-token'
    if (type !== null) header['Content-Type'] = type
    const path = '/api/embedding-index'+(action ? '/'+action : '')
    const response = await app.request(path,{method,headers:header,body:body===undefined?undefined:Buffer.from(body)})
    const raw = await response.text()
    assert.ok(!raw.includes('fixture.invalid') && !raw.includes('raw-fixture-body') && !raw.includes('secret-fixture-redact'))
    const responseID = response.headers.get('x-request-id')
    if (responseID) {
      assert.match(responseID,/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
      assert.notEqual(responseID,suppliedID)
    }
    const keys = ['content-type','cache-control','x-content-type-options']
    cases.push({name,method,path,auth,type,requestBody:body??null,status:response.status,
      headers:Object.fromEntries(keys.map(k=>[k,response.headers.get(k)??''])),
      hasServerRequestId:responseID!==null,
      responseBody:responseID ? raw.replaceAll(responseID,'<request-id>') : raw,
      calls:calls.splice(0)})
  }
  assert.equal(providerCalls,0)
} finally {
  globalThis.fetch=fetch
  await engine?.dispose()
  store?.close()
  process.chdir(cwd)
  rmSync(root,{recursive:true,force:true})
}
const output = JSON.stringify({node:process.version,sourceHashes,providerCalls,cases},null,2)+'\n'
const dest = 'backend/internal/app/testdata/embedding-action-boundary-source.json'
if (process.argv.includes('--check')) assert.equal(readFileSync(dest,'utf8'),output)
else writeFileSync(dest,output)
console.log('PASS',cases.length,'actual Hono boundary observations, zero outbound requests')
