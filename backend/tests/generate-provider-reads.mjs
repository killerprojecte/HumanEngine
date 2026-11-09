/** Real source app, registry, router and loopback provider. Synthetic data only. */
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { tmpdir } from 'node:os'
import { createHash } from 'node:crypto'
import { createServer } from 'node:http'
import { pathToFileURL, fileURLToPath } from 'node:url'
const root=resolve(fileURLToPath(new URL('../../',import.meta.url)))
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const {createApp}=await import(pathToFileURL(join(root,'src/api.ts')))
const sourceHashes=Object.fromEntries(['src/api.ts','src/models.ts','src/model-protocols.ts'].map(p=>[p,createHash('sha256').update(readFileSync(join(root,p))).digest('hex')]))
const specs=[
 {name:'builtin',path:'/api/model-protocols'},
 {name:'custom',path:'/api/model-protocols',custom:true},
 {name:'disabled',path:'/api/model-protocols',custom:true,disabled:true},
 {name:'head',path:'/api/model-protocols',method:'HEAD'},
 {name:'auth',path:'/api/model-protocols',badAuth:true},
 {name:'method',path:'/api/model-protocols',method:'POST'},
 {name:'trailing',path:'/api/model-protocols/'},
 {name:'encoded',path:'/api/model-%70rotocols'},
 {name:'missing',path:'/api/providers/missing/models'},
 {name:'native',path:'/api/providers/p/models'},
 {name:'native-head',path:'/api/providers/p/models',method:'HEAD'},
 {name:'native-empty',path:'/api/providers/p/models',empty:true},
 {name:'native-error',path:'/api/providers/p/models',failure:true},
 {name:'custom-models',path:'/api/providers/p/models',custom:true},
 {name:'custom-disabled',path:'/api/providers/p/models',custom:true,disabled:true},
 {name:'provider-encoded',path:'/api/providers/%70/models'},
 {name:'provider-slash',path:'/api/providers/a%2Fb/models'},
 {name:'provider-trailing',path:'/api/providers/p/models/'},
 {name:'provider-method',path:'/api/providers/p/models',method:'POST'},
 {name:'provider-auth',path:'/api/providers/p/models',badAuth:true}
]
const cases=[]
for(const spec of specs){
 const dir=mkdtempSync(join(tmpdir(),'he-provider-read-')),cwd=process.cwd();let store,engine,calls=0
 const peer=createServer((req,res)=>{calls++;res.writeHead(spec.failure?500:200,{'content-type':'application/json'});res.end(JSON.stringify(spec.failure?{error:'synthetic-secret'}:{data:spec.empty?[]:[{id:'z'},{id:'a'},{id:'z'}]}))})
 await new Promise(r=>peer.listen(0,'127.0.0.1',r))
 try{
 process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store)
 const abort=new AbortController()
 if(spec.custom)engine.protocols.register('fixture:custom','Fixture',{complete:async()=>({text:'',toolCalls:[],usage:{}}),listModels:async()=>['z','a','z']},abort.signal)
 if(spec.disabled)abort.abort()
 store.saveModels({providers:[{id:'p',name:'Synthetic',protocol:spec.custom?'fixture:custom':'openai-chat',baseUrl:`http://127.0.0.1:${peer.address().port}`,credentialEnv:'',retryCount:0,timeoutMs:1000}],models:[],routes:[]},{})
 const app=createApp(store,{engine,token:'synthetic-token'})
 const method=spec.method??'GET',response=await app.request(spec.path,{method,headers:{Authorization:'Bearer '+(spec.badAuth?'bad':'synthetic-token')}})
 cases.push({...spec,method,status:response.status,text:(await response.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"'),calls,responseHeaders:Object.fromEntries(['content-type','cache-control','x-content-type-options'].map(k=>[k,response.headers.get(k)]))})
 }finally{await engine?.dispose();store?.close();process.chdir(cwd);await new Promise(r=>peer.close(r));rmSync(dir,{recursive:true,force:true})}
}
const out=JSON.stringify({node:process.version,sourceHashes,cases},null,2)+'\n',file=new URL('../internal/httpapi/testdata/provider-reads-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),out);else writeFileSync(file,out)
console.log(`PASS ${cases.length} actual provider HTTP source cases`)
