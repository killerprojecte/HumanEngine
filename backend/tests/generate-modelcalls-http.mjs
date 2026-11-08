/** Actual source createApp/Store/Engine. No HTTP fetch or real user storage. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdtempSync,rmSync,mkdirSync} from 'node:fs'
import {resolve,join,dirname} from 'node:path'
import {pathToFileURL,fileURLToPath} from 'node:url'
import {tmpdir} from 'node:os'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../',import.meta.url)))
const hashes=JSON.parse(readFileSync(new URL('./modelcalls-http-hashes.json',import.meta.url),'utf8'))
for(const [p,h] of Object.entries(hashes))assert.equal(createHash('sha256').update(readFileSync(join(root,p))).digest('hex'),h,p)
assert.equal(process.version,'v24.22.1')
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const {createApp}=await import(pathToFileURL(join(root,'src/api.ts')))
const specs=[{name:'default'},{name:'limit',query:'?limit=1'},{name:'cursor',query:'?beforeId=2'},{name:'role',query:'?role=replyer'},{name:'status',query:'?status=failed'},{name:'model',query:'?modelId=missing'},{name:'empty-limit',query:'?limit='},{name:'zero-limit',query:'?limit=0'},{name:'high-limit',query:'?limit=101'},{name:'nan-limit',query:'?limit=no'},{name:'fraction',query:'?limit=1.5'},{name:'hex-limit',query:'?limit=0x2'},{name:'duplicate-limit',query:'?limit=1&limit=2'},{name:'invalid-role',query:'?role=thinker'},{name:'bad-cursor',query:'?beforeId=0'},{name:'invalid-status',query:'?status=x'},{name:'head',method:'HEAD'},{name:'bad-auth',badAuth:true},{name:'wrong-method',method:'PUT'},{name:'trailing-path',path:'/api/model-calls/'}]
const cases=[]
for(const spec of specs){
 const dir=mkdtempSync(join(tmpdir(),'he-diagnostic-read-'));const cwd=process.cwd();let store,engine
 try{
  process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store)
  for(const [index,role,event] of [[1,'thinker','model.completed'],[2,'vision','model.failed']])store.recordModelCall({at:'2099-01-01T00:00:00.000Z',modelId:'synthetic',providerId:'provider',role,event,attempt:index,durationMs:100,usage:{inputTokens:2,outputTokens:4},transportMode:'non_stream'})
  const app=createApp(store,{engine,token:'synthetic-token'})
  const headers={};if(!spec.noAuth)headers.Authorization='Bearer '+(spec.badAuth?'bad':'synthetic-token')
  const path=spec.path??('/api/model-calls'+(spec.query??'')),method=spec.method??'GET'
  const response=await app.request(path,{method,headers})
  const text=(await response.text()).replace(/"id":"[^"]+"/g,'"id":"<id>"').replace(/"at":"[^"]+"/g,'"at":"<time>"').replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"')
  cases.push({...spec,path,method,status:response.status,text,responseHeaders:Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k=>[k,response.headers.get(k)]))})
 }finally{if(engine)await engine.dispose();store?.close();process.chdir(cwd);rmSync(dir,{recursive:true,force:true})}
}
const file=new URL('../internal/httpapi/testdata/modelcalls-http-source.json',import.meta.url)
mkdirSync(dirname(fileURLToPath(file)),{recursive:true})
const out=JSON.stringify({node:process.version,sourceHashes:hashes,cases},null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),out);else writeFileSync(file,out)
console.log(`PASS ${cases.length} actual source model-calls HTTP cases`)
