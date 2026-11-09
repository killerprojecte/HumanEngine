/** Actual Store, Engine and Hono routes with exclusively in-memory SQLite. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {Store} from '../../src/store.ts'
import {Engine} from '../../src/engine.ts'
import {createApp} from '../../src/api.ts'
const root=new URL('../../',import.meta.url)
const paths=['src/api.ts','src/store.ts','src/schema.ts','src/diagnostics/log-writer.ts','src/diagnostics/format.ts','src/memory/recovery/ledger.ts']
const sourceHashes=Object.fromEntries(paths.map(p=>[p,createHash('sha256').update(readFileSync(new URL(p,root))).digest('hex')]))
const specs=[{name:'empty',empty:true},{name:'retained'},{name:'head',method:'HEAD'},{name:'query-ignored',path:'/api/logs?limit=1&offset=1&level=error&module=none&search=absent'},{name:'bad-auth',badAuth:true},{name:'no-auth',noAuth:true},{name:'head-auth',badAuth:true,method:'HEAD'},{name:'missing-token',noToken:true},{name:'post',method:'POST'},{name:'put',method:'PUT'},{name:'delete-not-clear',method:'DELETE'},{name:'options',method:'OPTIONS'},{name:'trailing',path:'/api/logs/'},{name:'child',path:'/api/logs/clear'},{name:'encoded',path:'/api/%6cogs'},{name:'many',count:503},{name:'unavailable',failure:true},{name:'head-unavailable',failure:true,method:'HEAD'}]
// Fault injection is only at the readiness boundary; actual Hono middleware/onError remain unchanged.
class ReadinessStore extends Store { fail=false; assertRecoveryReady(){if(this.fail)throw new Error('synthetic-readiness-failure');return super.assertRecoveryReady()} }
const cases=[]
for(const spec of specs){
 const channel={enqueue(){},get status(){return {state:'ready',rejectedRecords:0,failedRecords:0}},close:async()=>({complete:true,durable:true})}
 const store=new ReadinessStore(':memory:',undefined,{stdout:channel})
 const engine=new Engine(store)
 try {
  const count=spec.empty?0:spec.count??2
  for(let i=0;i<count;i++)store.log(i%2?'error':'info','synthetic','retained-'+i,{id:'row-'+i,at:'2026-11-09T00:00:00.123Z',counts:{items:i},payload:'中文<&>'})
  const before=JSON.stringify(store.logs)
  const app=createApp(store,{engine,token:spec.noToken?'':'synthetic-token'})
  const headers={'X-Request-ID':'synthetic-request'}
  if(!spec.noAuth)headers.Authorization='Bearer '+(spec.badAuth?'wrong':'synthetic-token')
  const path=spec.path??'/api/logs',method=spec.method??'GET'
  store.fail=!!spec.failure
  const response=await app.request(path,{method,headers})
  const text=(await response.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"')
  if(!spec.failure)assert.equal(JSON.stringify(store.logs),before,'reads and unsupported verbs must not mutate retention')
  cases.push({...spec,path,method,status:response.status,text,responseHeaders:Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k=>[k,response.headers.get(k)]))})
 } finally {store.fail=false;await engine.dispose();store.close()}
}
const out=JSON.stringify({sourceHashes,cases},null,2)+'\n'
const file=new URL('../internal/httpapi/testdata/retained-logs-http-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),out);else writeFileSync(file,out)
console.log(`PASS ${cases.length} actual in-memory source retained-log HTTP cases`)
