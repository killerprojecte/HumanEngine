/** Actual createApp + Engine + MemoryService deletion oracle, synthetic only. */
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import {syncBuiltinESMExports} from 'node:module'
import {readFileSync, writeFileSync} from 'node:fs'
import {resolve} from 'node:path'
import {pathToFileURL} from 'node:url'
const root = resolve(process.env.HE_SOURCE_ROOT ?? process.cwd())
assert.equal(process.version, 'v24.22.1')
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
let serial = 0, selectedID
crypto.randomUUID = () => selectedID ?? `00000000-0000-4000-8000-${String(++serial).padStart(12, '0')}`
syncBuiltinESMExports()
const NativeDate = Date
class FixedDate extends NativeDate {
  constructor(...args) { super(...(args.length ? args : ['2026-10-28T12:00:00.000Z'])) }
  static now() { return 1793188800000 }
}
globalThis.Date = FixedDate
const {Store} = await import(pathToFileURL(resolve(root, 'src/store.ts')))
const {Engine} = await import(pathToFileURL(resolve(root, 'src/engine.ts')))
const {createApp} = await import(pathToFileURL(resolve(root, 'src/api.ts')))
const prefix = '/api/memories'
const specs = [
  {name:'existing', path:prefix+'/m'},
  {name:'missing', path:prefix+'/absent'},
  {name:'repeated', path:prefix+'/m', repeat:true},
  {name:'ignored-query-body', path:prefix+'/m?sceneId=wrong&expectedVersion=-1&x=1&x=2', body:'{', contentType:'text/plain'},
  {name:'ignored-json-body', path:prefix+'/m', body:'{"confirmed":false,"id":"wrong"}', contentType:'application/json'},
  {name:'encoded-prefix', path:'/api/memor%69es/m'},
  {name:'encoded-id', path:prefix+'/%6d'},
  {name:'unicode', id:'記憶😀', path:prefix+'/'+encodeURIComponent('記憶😀')},
  {name:'slash', id:'a/b', path:prefix+'/a%2Fb'},
  {name:'percent', id:'%2F', path:prefix+'/%252F'},
  {name:'plus', id:'a+b', path:prefix+'/a+b'},
  {name:'question', id:'a?b', path:prefix+'/a%3Fb'},
  {name:'invalid-utf8', id:'%FF', path:prefix+'/%FF'},
  {name:'long-id', id:'x'.repeat(4097), path:prefix+'/'+'x'.repeat(4097)},
  {name:'missing-long-id', path:prefix+'/'+'z'.repeat(4097)},
  {name:'reserved-page-id', id:'page', path:prefix+'/page'},
  ...['GET','HEAD','POST','PUT','OPTIONS'].map(method=>({name:'wrong-method-'+method,method,path:prefix+'/m'})),
  {name:'no-id', path:prefix}, {name:'empty-id',path:prefix+'/'},
  {name:'trailing-slash',path:prefix+'/m/'}, {name:'extra-segment',path:prefix+'/m/extra'},
  {name:'missing-auth',path:prefix+'/m',authorization:null},
  {name:'wrong-auth',path:prefix+'/m',authorization:'Bearer wrong'},
  {name:'raw-token',path:prefix+'/m',authorization:'synthetic-token'},
  {name:'no-server-token',path:prefix+'/m',serverToken:''},
  {name:'corrupt-record',path:prefix+'/m',corrupt:true},
  {name:'unready',path:prefix+'/m',unready:true},
  {name:'unready-unknown-path',path:prefix+'/m/extra',unready:true},
]
const tables=['memories','memory_sources','memory_supports','memory_deleted','memory_assertion_state','memory_assertion_revisions','memory_assertion_revision_sources','memory_assertion_revision_parents','memory_command_receipts']
const schemas={}, cases=[]
for (const spec of specs) {
  const store=new Store(':memory:'), engine=new Engine(store)
  try {
    selectedID=spec.id??'m'
    engine.memory.create({scope:{kind:'sharedPublicKnowledge'},content:'Synthetic deletion fixture.',kind:'fact'})
    selectedID=undefined
    for (const row of store.db.prepare("SELECT name,sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL").all()) if(tables.includes(row.name)) schemas[row.name]=row.sql
    const before=Object.fromEntries(tables.filter(t=>schemas[t]).map(t=>[t,store.db.prepare(`SELECT * FROM ${t}`).all()]))
    if(spec.corrupt) store.db.prepare("UPDATE memories SET value='{' WHERE id=?").run(spec.id??'m')
    if(spec.unready) store.assertRecoveryReady=()=>{throw Error('synthetic_recovery_unready')}
    const app=createApp(store,{engine,token:spec.serverToken??'synthetic-token'})
    const headers={'X-Request-ID':'synthetic-memory-delete',...(spec.authorization===null?{}:{Authorization:spec.authorization??'Bearer synthetic-token'}),...(spec.contentType?{'Content-Type':spec.contentType}:{})}
    const init={method:spec.method??'DELETE',headers,...(spec.body===undefined?{}:{body:spec.body})}
    if(spec.repeat) { const first=await app.request('http://example.test'+spec.path,init);assert.equal(await first.text(),'{"ok":true}') }
    const response=await app.request('http://example.test'+spec.path,init)
    const requestID=response.headers.get('x-request-id')
    assert.match(requestID,/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    assert.notEqual(requestID,headers['X-Request-ID'])
    const responseText=await response.text()
    const result={...spec,method:init.method,before,status:response.status,text:responseText.replaceAll(requestID,'<request-id>'),headers:Object.fromEntries(['content-type','cache-control','x-content-type-options','x-request-id'].map(k=>[k,k==='x-request-id'?'<request-id>':response.headers.get(k)??'']))}
    cases.push(result)
  } finally { selectedID=undefined; await engine.dispose(); store.close() }
}
const files=['src/admin.ts','src/api.ts','src/diagnostics/errors.ts','src/memory.ts','src/memory/lifecycle/assertion-lifecycle.ts','src/memory/write/manual-commands.ts','src/memory/write/revision-coordinator.ts','node_modules/hono/dist/utils/url.js','node_modules/hono/dist/validator/validator.js','package-lock.json']
const out={node:process.version,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,sourceHashes:Object.fromEntries(files.map(f=>[f,crypto.createHash('sha256').update(readFileSync(resolve(root,f))).digest('hex')])),schemas,cases}
const dest=new URL('../internal/httpapi/testdata/memory-delete-source.json',import.meta.url), text=JSON.stringify(out,null,2)+'\n'
if(process.argv.includes('--check')) assert.equal(readFileSync(dest,'utf8'),text); else writeFileSync(dest,text)
console.log(`Verified ${cases.length} actual source memory-delete HTTP cases`)
