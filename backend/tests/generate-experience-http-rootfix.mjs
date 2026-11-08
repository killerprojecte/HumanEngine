/** Actual persisted route projections, including post-expiry error ordering. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs'
import {resolve,join} from 'node:path'
import {tmpdir} from 'node:os'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??'../humanengine-golang-recovered')
const load=path=>import(pathToFileURL(resolve(root,path)))
const {Store}=await load('src/store.ts'),{Engine}=await load('src/engine.ts'),{createApp}=await load('src/api.ts'),{metadata}=await load('src/memory/recovery/ledger.ts')
const {initial}=JSON.parse(readFileSync(new URL('../internal/httpapi/testdata/experience-source.json',import.meta.url)))
const specs=[]
for(const [kind,value] of [['missing',undefined],['null',null],['boolean',false],['number',42],['string','synthetic'],['array',[]]])for(const operation of ['get','expiry','active','suspended','retired'])specs.push({name:kind+'-'+operation,kind,value,operation,expiry:operation==='expiry',noTrial:true})
const cases=[]
for(const spec of specs){
 const cwd=process.cwd(),dir=mkdtempSync(join(tmpdir(),'he-experience-presence-'));let store,engine
 try{
 process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store)
 const data=structuredClone(initial)
 engine.experience.id=()=>"synthetic-reviewfix-receipt"
 if(spec.clean){data.policy=structuredClone(initial.cleanPolicy);data.receipt=structuredClone(initial.cleanReceipt)}
 if(Object.hasOwn(spec,"identity"))data.identity=spec.identity
 if(spec.expiry){data.policy.status='active';data.policy.expiresAt='1970-01-02T00:00:00.000Z'}
 data.receipt=spec.value
 if(spec.operation==='active'){data.policy=structuredClone(initial.validatedPolicy)}
 store.db.prepare('INSERT INTO scenes VALUES(?,?)').run('s',JSON.stringify({id:'s',kind:'group',learningEnabled:true}))
 store.db.prepare('INSERT OR REPLACE INTO config VALUES(?,?)').run('settings',JSON.stringify({...store.settings,memoryExperienceEnabled:true}))
 store.db.prepare('INSERT INTO config VALUES(?,?)').run('identity:s:alice',JSON.stringify(data.identity))
 store.db.prepare('INSERT INTO memory_experience_policies VALUES(?,?,?,?)').run('p','s',1,JSON.stringify(data.policy))
 store.db.prepare('INSERT INTO memory_experience_outcomes VALUES(?,?,?,?,?)').run('outcome','root','s','instruction',JSON.stringify(data.outcome))
 if(spec.kind!=='missing')store.db.prepare('INSERT INTO memory_experience_validations VALUES(?,?,?)').run('receipt','p',JSON.stringify(data.receipt))
 store.db.exec('CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT);CREATE TABLE synthetic_updates(n);CREATE TRIGGER synthetic_count AFTER UPDATE ON memory_experience_policies BEGIN INSERT INTO synthetic_updates VALUES(1);END')
 if(!spec.noTrial&&!spec.clean)store.db.prepare('INSERT INTO memory_experience_trials VALUES(?,?,?,?)').run('p','s',1,JSON.stringify(data.trial))
 const app=createApp(store,{engine,token:'synthetic-http-test-token'}),before=store.recoveryAuthority.head().sequence,responses=[]
 assert.equal(before,0);assert.equal(store.recoveryAuthority.head().hash,'0'.repeat(64))
 for(let i=0;i<(spec.expiry?2:1);i++){
  const method=['validate','active','suspended','retired'].includes(spec.operation)?'POST':'GET'
  const path=method==='GET'?'/api/memory-experience?sceneId=s':'/api/memory-experience/p/'+(spec.operation==='validate'?'validate':'review')
  const body=method==='POST'?JSON.stringify({sceneId:'s',expectedVersion:spec.operation==='active'?2:1,...(spec.operation==='validate'?{}:{status:spec.operation})}):undefined
  const r=await app.request('http://example.test'+path,{method,headers:{Authorization:'Bearer synthetic-http-test-token','Content-Type':'application/json'},...(body?{body}:{})})
  const events=store.recoveryAuthority.db.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(before).map(r=>r.payload)
  let normalizedHeadHash='0'.repeat(64),normalizedGateHash='0'.repeat(64)
  for(let n=0;n<events.length;n++){normalizedHeadHash=createHash('sha256').update(`12345678-1234-4123-8123-123456789012|${n+1}|${normalizedHeadHash}|${events[n]}`).digest('hex');if(n+1===metadata(store.db).head.sequence)normalizedGateHash=normalizedHeadHash}
  responses.push({events,normalizedHeadHash,normalizedGateHash,status:r.status,text:(await r.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"'),headDelta:store.recoveryAuthority.head().sequence-before,gateDelta:metadata(store.db).head.sequence-before,updates:store.db.prepare('SELECT count(*) n FROM synthetic_updates').get().n,policy:JSON.parse(store.db.prepare("SELECT value FROM memory_experience_policies WHERE id='p'").get().value)})
 }
 cases.push({...spec,noTrial:!!(spec.noTrial||spec.clean),initial:{policy:data.policy,outcome:data.outcome,receipt:data.receipt,trial:data.trial,identity:data.identity},responses})
 }finally{if(engine)await engine.dispose();store?.close();process.chdir(cwd);rmSync(dir,{recursive:true,force:true})}
}
const files=['src/memory/interfaces/experience-routes.ts','src/memory/experience/service.ts','src/memory/experience/store.ts','src/memory/experience/promotion.ts','src/api.ts','src/memory/recovery/ledger.ts','node_modules/zod/v4/core/checks.js','package-lock.json']
const result={normalization:'Only generated requestId is replaced; event payload bytes are unchanged. Head/gate hashes are recomputed with the fixed synthetic native-fixture instance ID, starting from the asserted empty source chain.',node:process.version,sourceHashes:Object.fromEntries(files.map(p=>[p,createHash('sha256').update(readFileSync(resolve(root,p))).digest('hex')])),cases}
const dest=new URL('../internal/httpapi/testdata/experience-rootfix-source.json',import.meta.url),text=JSON.stringify(result,null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(dest,'utf8'),text);else writeFileSync(dest,text)
console.log(cases.map(c=>[c.name,...c.responses.map(r=>({status:r.status,head:r.headDelta,gate:r.gateDelta,updates:r.updates}))]))
