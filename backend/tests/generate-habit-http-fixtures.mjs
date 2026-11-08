/** Actual createApp/Engine/HabitService routes with synthetic rows only. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdirSync,mkdtempSync,rmSync} from 'node:fs'
import {resolve,join} from 'node:path'
import {tmpdir} from 'node:os'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??'humanengine-golang-recovered')
const {Store}=await import(pathToFileURL(resolve(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(resolve(root,'src/engine.ts')))
const {createApp}=await import(pathToFileURL(resolve(root,'src/api.ts')))
const {metadata}=await import(pathToFileURL(resolve(root,'src/memory/recovery/ledger.ts')))
assert.equal(process.version,'v24.22.1')
const record={id:'h',kind:'sceneRule',sceneId:'s',content:'Synthetic',evidenceVersion:2,authorWatermarks:{'s:p':123},sources:[],weight:0,confidence:0,status:'invalidated',locked:false,learnedAt:'2026-11-01T00:00:00.000Z'}
const specs=[
 ['get','GET','/api/habits'],['head','HEAD','/api/habits'],
 ['missing-route-get','GET','/api/habits/missing'],['missing-route-head','HEAD','/api/habits/missing'],
 ['not-found-withdrawal','GET','/api/habits/missing',null,true,undefined,'h',false,false,true],
 ['patch','PATCH','/api/habits/h','{"locked":true}'],['delete','DELETE','/api/habits/h'],
 ['missing-delete','DELETE','/api/habits/absent'],['missing-patch','PATCH','/api/habits/absent','{"locked":true}'],
 ['unknown','PATCH','/api/habits/h','{"locked":true,"other":1}'],['empty','PATCH','/api/habits/h','{}'],
 ['wrong','PATCH','/api/habits/h','{"locked":1}'],['malformed','PATCH','/api/habits/h','{'],
 ['no-type','PATCH','/api/habits/h','{"locked":true}',false],['bad-auth','GET','/api/habits',null,true,'bad'],
 ['post','POST','/api/habits','{}'],['encoded-id','PATCH','/api/habits/%68','{"locked":true}'],
 ['encoded-slash-patch','PATCH','/api/habits/h%2Fpart','{"locked":true}',true,undefined,'h/part'],
 ['encoded-slash-delete','DELETE','/api/habits/h%2fpart',null,true,undefined,'h/part'],
 ['literal-slash-not-route','PATCH','/api/habits/h/part','{"locked":true}',true,undefined,'h/part'],
 ['double-encoded-slash','PATCH','/api/habits/h%252Fpart','{"locked":true}',true,undefined,'h%2Fpart'],
 ['encoded-static-prefix','GET','/api/%68abits'],
 ['invalid-utf8-contiguous','PATCH','/api/habits/h%2F%FF','{"locked":true}',true,undefined,'h%2F%FF'],
 ['invalid-utf8-separated','PATCH','/api/habits/h%2Fx%FF','{"locked":true}',true,undefined,'h/x%FF'],
 ['delete-main-rollback','DELETE','/api/habits/h',null,true,undefined,'h',false,true],
 ['invalid-schema-after-body-withdrawal','PATCH','/api/habits/h','{}',true,undefined,'h',true],
]
const cases=[]
for(const [name,method,path,body,jsonType=true,token='synthetic-http-test-token',recordID='h',withdrawDuringBody=false,deleteRollback=false,withdrawDuringNotFound=false] of specs){
 let dir,store,engine
 const cwd=process.cwd()
 try{
  if(withdrawDuringBody||deleteRollback||withdrawDuringNotFound){dir=mkdtempSync(join(tmpdir(),'he-habit-http-'));process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'))}
  else store=new Store(':memory:')
  engine=new Engine(store)
  const app=createApp(store,{engine,token:'synthetic-http-test-token'})
  if(withdrawDuringNotFound) app.notFound(c=>{
   store.recoveryAuthority.append({kind:'source',sceneId:'s',messageId:'not-found-withdrawal'},store.recoveryAuthority.head(),false)
   return c.text('404 Not Found',404)
  })
  const initial={...record,id:recordID}
  store.db.prepare('INSERT INTO habits VALUES(?,?,?)').run(recordID,'a'.repeat(64),JSON.stringify(initial))
  if(deleteRollback)store.db.exec("CREATE TRIGGER synthetic_delete_failure BEFORE DELETE ON habits BEGIN SELECT RAISE(ABORT,'synthetic_delete_failure'); END")
  const initialHead=store.recoveryAuthority?.head().sequence
  const headers={'Authorization':'Bearer '+token}
  if(jsonType)headers['Content-Type']='application/json'
  let response,headDelta=null,gateDelta=null
  if(withdrawDuringBody){
   const authority=store.recoveryAuthority;assert.ok(authority)
   const before=authority.head().sequence
   let pulls=0
   const stream=new ReadableStream({pull(controller){
    pulls++;assert.equal(pulls,1)
    authority.append({kind:'source',sceneId:'s',messageId:'body-withdrawal'},authority.head(),false)
    controller.enqueue(new TextEncoder().encode(body));controller.close()
   }},{highWaterMark:0})
   response=await app.fetch(new Request('http://example.test'+path,{method,headers,body:stream,duplex:'half'}))
   assert.equal(pulls,1);headDelta=authority.head().sequence-before;gateDelta=metadata(store.db).head.sequence-before
   assert.equal(headDelta,1);assert.equal(gateDelta,0);assert.equal(response.status,503)
  }else response=await app.request('http://example.test'+path,{method,headers,...(body!=null?{body}:{})})
  if(withdrawDuringNotFound){headDelta=store.recoveryAuthority.head().sequence-initialHead;gateDelta=metadata(store.db).head.sequence-initialHead;assert.equal(headDelta,1);assert.equal(gateDelta,0);assert.equal(response.status,503)}
  if(deleteRollback){headDelta=store.recoveryAuthority.head().sequence-initialHead;gateDelta=metadata(store.db).head.sequence-initialHead;assert.equal(headDelta,1);assert.equal(gateDelta,0);assert.equal(response.status,503);assert.equal(store.db.prepare('SELECT count(*) AS n FROM habit_deleted').get().n,0)}
  const durablePayloads=(withdrawDuringBody||deleteRollback||withdrawDuringNotFound)?store.recoveryAuthority.db.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(initialHead).map(r=>r.payload):[]
  cases.push({name,method,path,body:body??null,jsonType,token,initial,withdrawDuringBody,deleteRollback,withdrawDuringNotFound,headDelta,gateDelta,durablePayloads,status:response.status,text:(await response.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"'),rows:store.db.prepare('SELECT id,value FROM habits ORDER BY rowid').all()})
 }finally{if(engine)await engine.dispose();store?.close();process.chdir(cwd);if(dir)rmSync(dir,{recursive:true,force:true})}
}
const files=['src/api.ts','src/admin.ts','src/habits.ts','src/memory/recovery/ledger.ts','node_modules/hono/dist/utils/url.js','node_modules/hono/dist/validator/validator.js','package-lock.json']
const out={normalization:'Only generated requestId replaced with <request-id>; all status/body/rows retained',node:process.version,sourceHashes:Object.fromEntries(files.map(p=>[p,createHash('sha256').update(readFileSync(resolve(root,p))).digest('hex')])),record,cases}
const dest=new URL('../internal/httpapi/testdata/habits-source.json',import.meta.url)
const text=JSON.stringify(out,null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(dest,'utf8'),text)
else{mkdirSync(new URL('.',dest),{recursive:true});writeFileSync(dest,text)}
console.log(cases.map(x=>[x.name,x.status,x.text]))
