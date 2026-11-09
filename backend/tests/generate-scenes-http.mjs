import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs'
import {resolve,join} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import {tmpdir} from 'node:os'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../',import.meta.url)))
const hashes=JSON.parse(readFileSync(new URL('./scenes-source-hashes.json',import.meta.url),'utf8'))
for(const [file,hash] of Object.entries(hashes))assert.equal(createHash('sha256').update(readFileSync(join(root,file))).digest('hex'),hash,file)
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const {createApp}=await import(pathToFileURL(join(root,'src/api.ts')))
const scene={name:'Synthetic scene',id:'synthetic-scene',kind:'group',adapterId:'synthetic',accountId:'a',platformSceneId:'s',participationEnabled:true,learningEnabled:true,proactiveEnabled:false}
const specs=[{name:'enable',body:scene},{name:'pause',body:{...scene,learningEnabled:false}},{name:'defaults',body:{id:scene.id,kind:scene.kind,adapterId:scene.adapterId,accountId:scene.accountId,platformSceneId:scene.platformSceneId}},{name:'mismatch',body:{...scene,id:'other'}},{name:'invalid',body:{...scene,kind:'invalid'}},{name:'unauthorized',body:scene,token:'wrong'},{name:'malformed',raw:'{'},{name:'wrong-content-type',body:scene,type:'text/plain'}]
const cases=[]
for(const spec of specs){const dir=mkdtempSync(join(tmpdir(),'he-scene-source-'));const cwd=process.cwd();let store,engine
 try{process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store);const app=createApp(store,{engine,token:'synthetic-scenes-token'});const path='/api/scenes/'+scene.id;const body=spec.raw??JSON.stringify(spec.body);const headers={Authorization:'Bearer '+(spec.token??'synthetic-scenes-token'),'Content-Type':spec.type??'application/json','X-Request-ID':'12345678-1234-4123-8123-123456789012'};const response=await app.request('http://example.test'+path,{method:'PUT',headers,body});const text=(await response.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"');cases.push({name:spec.name,path,body,headers,status:response.status,text,responseHeaders:Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k=>[k,response.headers.get(k)])),rows:store.db.prepare('SELECT id,value FROM scenes ORDER BY id').all()})
 }finally{await engine?.dispose();store?.close();process.chdir(cwd);rmSync(dir,{recursive:true,force:true})}}
const dest=new URL('../internal/httpapi/testdata/scenes-source.json',import.meta.url);const out=JSON.stringify({node:process.version,sourceHashes:hashes,cases},null,2)+'\n';if(process.argv.includes('--check'))assert.equal(readFileSync(dest,'utf8'),out);else writeFileSync(dest,out);console.log(`PASS ${cases.length} actual-source scene HTTP cases`)
