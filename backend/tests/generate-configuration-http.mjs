/** Actual createApp + Engine + Store, synthetic disk-backed SQLite only. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs'
import {resolve,join} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import {tmpdir} from 'node:os'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../',import.meta.url)))
const hashes=JSON.parse(readFileSync(new URL('./configuration-source-hashes.json',import.meta.url),'utf8'))
for(const [file,hash] of Object.entries(hashes))assert.equal(createHash('sha256').update(readFileSync(join(root,file))).digest('hex'),hash,file)
assert.equal(process.version,'v24.22.1')
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(join(root,'src/engine.ts')))
const {metadata}=await import(pathToFileURL(join(root,'src/memory/recovery/ledger.ts')))
const {createApp}=await import(pathToFileURL(join(root,'src/api.ts')))
const empty={providers:[],models:[],routes:[]}
const provider={id:'p',name:'Synthetic',protocol:'openai-chat',baseUrl:'https://example.invalid',credentialEnv:''}
const base={providers:[provider],models:[],routes:[]}
const cases=[]
const specs=[
 {name:'empty-models',body:empty},
 {name:'provider-defaults',body:base},
 {name:'credential-set',body:{...base,credentials:{p:{action:'set',value:'synthetic-secret'}}}},
 {name:'credential-surrogate',body:{...base,credentials:{p:{action:'set',value:'synthetic-\ud800-value'}}}},
 {name:'credential-clear',initial:base,secret:true,body:{...base,credentials:{p:{action:'clear'}}}},
 {name:'credential-preserve',initial:base,secret:true,body:{...base,credentials:{p:{action:'preserve'}}}},
 {name:'removed-provider',initial:base,secret:true,body:empty},
 {name:'unknown-provider',body:{...base,credentials:{missing:{action:'clear'}}}},
 {name:'null-credentials',body:{...base,credentials:null}},
 {name:'credential-unknown',body:{...base,credentials:{p:{action:'preserve',value:'x'}}}},
 {name:'credential-blank',body:{...base,credentials:{p:{action:'set',value:' \t\ufeff'}}}},
 {name:'null-body',body:null},{name:'missing-fields',body:{}},{name:'unknown-field',body:{...empty,x:1}},
 {name:'malformed',raw:'{'},{name:'missing-body',raw:''},{name:'wrong-type',body:empty,type:'text/plain'},
 {name:'bad-auth',body:empty,token:'bad'}, {name:'wrong-method',body:empty,method:'POST'},
 {name:'read-models',initial:base,secret:true,method:'GET'},{name:'head-models',initial:base,secret:true,method:'HEAD'},
 {name:'rollback-models',initial:base,secret:true,body:{...base,credentials:{p:{action:'set',value:'synthetic-new'}}},rollback:true},
 {name:'first-secret-failure',initial:base,secret:true,body:{providers:[provider,{...provider,id:'q'}],models:[],routes:[],credentials:{p:{action:'set',value:'synthetic-first'},q:{action:'set',value:'synthetic-second'}}},rollback:true,secretRollback:true},
 {name:'trailing-model-path',path:'/api/models/',method:'GET'},
 {name:'settings-null',settings:true,raw:'null'},
 {name:'settings-unknown',settings:true,patch:{unknown:true}},
 {name:'settings-same',settings:true},{name:'settings-persona',settings:true,patch:{persona:'Synthetic new persona'}},
 {name:'settings-policy',settings:true,patch:{memoryExperienceFeedbackEnabled:true}},
 {name:'settings-rollback',settings:true,patch:{persona:'Synthetic durable history'},rollback:true},
]
for(const spec of specs){
 const dir=mkdtempSync(join(tmpdir(),'he-configuration-oracle-'));const cwd=process.cwd();let store,engine
 try{
  process.chdir(dir);store=new Store(join(dir,'live','main.sqlite'));engine=new Engine(store)
  const app=createApp(store,{engine,token:'synthetic-config-token'})
  if(spec.initial)store.saveModels(spec.initial,spec.secret?{p:{action:'set',value:'synthetic-original'}}:{})
  const initialSecretRows=store.db.prepare('SELECT namespace,name,ciphertext FROM secrets').all().map(r=>({...r,value:store.secrets.get(r.namespace,r.name)}))
  const initialSettings=store.settings
  const path=spec.path??(spec.settings?'/api/settings':'/api/models'),method=spec.method??'PUT'
  const body=spec.raw??JSON.stringify(spec.settings?{...initialSettings,...spec.patch}:spec.body)
  if(spec.secretRollback)store.db.exec("CREATE TRIGGER synthetic_secret_failure BEFORE INSERT ON secrets BEGIN SELECT RAISE(ABORT,'synthetic_secret_failure'); END")
  if(spec.rollback&&!spec.secretRollback)store.db.exec(`CREATE TRIGGER synthetic_config_failure BEFORE UPDATE ON config WHEN NEW.key='${spec.settings?'settings':'models'}' BEGIN SELECT RAISE(ABORT,'synthetic_config_failure'); END; CREATE TRIGGER synthetic_config_insert_failure BEFORE INSERT ON config WHEN NEW.key='${spec.settings?'settings':'models'}' BEGIN SELECT RAISE(ABORT,'synthetic_config_failure'); END`)
  const headers={'Authorization':'Bearer '+(spec.token??'synthetic-config-token'),'Content-Type':spec.type??'application/json','X-Request-ID':'12345678-1234-4123-8123-123456789012'}
  const beforeHead=store.recoveryAuthority.head().sequence
  const response=await app.request('http://example.test'+path,{method,headers,...(!['GET','HEAD'].includes(method)?{body}: {})})
  const text=(await response.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"')
  const rows=store.db.prepare("SELECT key,value FROM config WHERE key IN ('settings','models','personaVersions','memory-experience-feedback-revision') ORDER BY key").all().map(r=>({...r,value:r.key==='personaVersions'?JSON.stringify(JSON.parse(r.value).map(v=>({...v,id:'<uuid>',createdAt:'<time>'}))):r.value}))
  const secrets=store.db.prepare('SELECT namespace,name,ciphertext FROM secrets ORDER BY namespace,name').all().map(r=>({namespace:r.namespace,name:r.name,encrypted:!r.ciphertext.includes('synthetic-'),value:spec.rollback?(assert.equal(r.ciphertext,initialSecretRows.find(x=>x.namespace===r.namespace&&x.name===r.name)?.ciphertext),initialSecretRows.find(x=>x.namespace===r.namespace&&x.name===r.name)?.value):store.secrets.get(r.namespace,r.name)}))
  cases.push({name:spec.name,path,method,body:body??'',headers,initial:spec.initial??null,secret:spec.secret??false,rollback:spec.rollback??false,secretRollback:spec.secretRollback??false,initialSettings,durablePayloads:store.recoveryAuthority.db.prepare('SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence').all(beforeHead).map(r=>{const v=JSON.parse(r.payload);if(v.kind==='security_state')v.revision='<uuid>';return JSON.stringify(v)}),headDelta:store.recoveryAuthority.head().sequence-beforeHead,gateDelta:metadata(store.db).head.sequence-beforeHead,status:response.status,text,responseHeaders:Object.fromEntries(["content-type","cache-control","x-content-type-options"].map(k=>[k,response.headers.get(k)])),rows,secrets})
 }finally{if(engine)await engine.dispose();store?.close();process.chdir(cwd);rmSync(dir,{recursive:true,force:true})}
}
const dest=new URL('../internal/httpapi/testdata/configuration-source.json',import.meta.url)
const out=JSON.stringify({node:process.version,sourceHashes:hashes,normalization:'Only generated request IDs, persona version UUID and timestamp are normalized. Secret plaintext is synthetic, decrypted for oracle comparison only.',cases},null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(readFileSync(dest,'utf8'),out);else writeFileSync(dest,out)
console.log(`PASS ${cases.length} actual-source configuration HTTP/SQL cases`)
