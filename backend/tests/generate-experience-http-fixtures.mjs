/** Real createApp/Engine/ExperienceService route oracle; synthetic inputs only. */
import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {resolve} from 'node:path'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??'../humanengine-golang-recovered')
const {Store}=await import(pathToFileURL(resolve(root,'src/store.ts')))
const {Engine}=await import(pathToFileURL(resolve(root,'src/engine.ts')))
const {candidateFromOutcome}=await import(pathToFileURL(resolve(root,'src/memory/experience/candidates.ts')))
const {validateExperiencePolicy}=await import(pathToFileURL(resolve(root,'src/memory/experience/validation.ts')))
const {createApp}=await import(pathToFileURL(resolve(root,'src/api.ts')))
const base='/api/memory-experience'
const specs=[['list','GET',base+'?sceneId=s'],['head','HEAD',base+'?sceneId=s'],['missing-scene','GET',base+'?sceneId=absent'],['missing-query','GET',base],['unknown-query','GET',base+'?sceneId=s&x=1'],['duplicate-scene','GET',base+'?sceneId=s&sceneId=s'],['bad-status','GET',base+'?sceneId=s&status=no'],['encoded-prefix','GET','/api/memory-%65xperience?sceneId=s'],['wrong-method','POST',base,'{}'],['trailing-slash','GET',base+'/?sceneId=s']]
for(const limit of ['','0','1','40','41','1.5','0x2','0b10','0o2','%20%201%20','1e1','Infinity','NaN','+2','%2B2','1&limit=2'])specs.push(['limit-'+limit,'GET',base+'?sceneId=s&limit='+limit])
for(const [method,suffix] of [['POST','/validate'],['POST','/review'],['DELETE','']]){
 const p=base+'/p'+suffix;const good={sceneId:'s',expectedVersion:1,...(suffix==='/review'?{status:'active'}:{})}
 for(const [name,body,type] of [['missing',good],['unknown',{...good,x:1}],['zero',{...good,expectedVersion:0}],['fraction',{...good,expectedVersion:1.5}],['string',{...good,expectedVersion:'1'}],['absent-scene',{...good,sceneId:'absent'}],['malformed','{'],['no-mime',good,false],['null',null],['array',[]],['empty',{}],['large-version',{...good,expectedVersion:1e30}]])specs.push([suffix+'-'+name,method,p,typeof body==='string'?body:JSON.stringify(body),type])
 specs.push([suffix+'-encoded',method,base+'/p%2Fpart'+suffix,JSON.stringify(good)])
}
const initial={
 policy:{id:'p',version:1,hash:'a'.repeat(64),sceneId:'s',action:'exact_person_before_semantic',condition:{kind:'person_question',personId:'alice',hidden:'private-condition'},status:'candidate',supportIds:['outcome'],counterexampleIds:[],generationRoots:['private-root'],generationFamilies:['private-family'],createdAt:'2026-01-01T00:00:00.000Z',expiresAt:'2099-01-01T00:00:00.000Z',validationId:'receipt',secret:'private-policy'},
 outcome:{id:'outcome',version:1,sceneId:'s',rootId:'root',rootFamily:'family',source:{messageId:'instruction',senderId:'alice',secret:'private-source'},target:{messageId:'reply'},code:'request_exact_person_lookup',observedAt:'2026-01-01T00:00:00.000Z',secret:'private-outcome'},
 receipt:{candidateId:'p',candidateVersion:1,candidateHash:'a'.repeat(64),tier:'mechanical_only',checks:['synthetic'],failures:[],passed:true,secret:'private-receipt'},
 trial:{schemaVersion:1,policyId:'p',sceneId:'s',version:1,policyVersion:1,evidencePolicyVersion:1,policyHash:'a'.repeat(64),requesterId:'alice',settingsRevision:0,phase:'trial',createdAt:'2099-01-01T00:00:00.000Z',expiresAt:'2099-01-07T00:00:00.000Z',instruction:{id:'outcome',version:1},validationId:'receipt',positives:[{outcomeId:'private-positive',outcomeVersion:1,sourceRoot:'b'.repeat(64),targetRoot:'c'.repeat(64),turnId:'private-turn'}],reason:'explicit_instruction_trial'},
 identity:{senderDisplayName:'  A\u0000\u202e\u2066lice  '}
}
initial.cleanPolicy=candidateFromOutcome(initial.outcome,'p',Date.parse('2099-01-01T00:00:00.000Z'))
initial.cleanReceipt=validateExperiencePolicy(initial.cleanPolicy)
assert.equal(initial.cleanReceipt.passed,true)
initial.validatedPolicy={...initial.cleanPolicy,version:2,status:'validated',validationId:'receipt',reason:'mechanical_only'}
specs.push(['clean-validate','POST',base+'/p/validate','{"sceneId":"s","expectedVersion":1}'],['clean-review-active','POST',base+'/p/review','{"sceneId":"s","expectedVersion":2,"status":"active"}'],['rich-review-suspended','POST',base+'/p/review','{"sceneId":"s","expectedVersion":1,"status":"suspended"}'],['rich-review-retired','POST',base+'/p/review','{"sceneId":"s","expectedVersion":1,"status":"retired"}'])
for(const [name,method] of [['bad-auth','GET'],['bad-auth-head','HEAD']])specs.push([name,method,base+'?sceneId=s',undefined,true,'wrong'])
for(const [name,text] of [['2048','x'.repeat(2048)],['2049','x'.repeat(2049)],['astral1024','😀'.repeat(1024)],['astral1025','😀'.repeat(1025)],['astral2048','😀'.repeat(2048)],['astral2049','😀'.repeat(2049)]]){
 specs.push(['scene-'+name,'GET',base+'?sceneId='+encodeURIComponent(text)])
 specs.push(['id-'+name,'POST',base+'/'+encodeURIComponent(text)+'/validate','{"sceneId":"s","expectedVersion":1}'])
 specs.push(['body-scene-'+name,'DELETE',base+'/p',JSON.stringify({sceneId:text,expectedVersion:1})])
}
for(const n of [2048,2049])specs.push(['lone-surrogate-body-'+n,'DELETE',base+'/p',JSON.stringify({sceneId:'\ud800'.repeat(n),expectedVersion:1})])
for(const q of ['sceneId=s&limit=0x1p0','sceneId=s&__proto__=x','sceneId=s&&=ignored','sceneId=s&status=candidate&status=candidate','sceneId=%FF','sceneId=s&limit=%E3%80%801%E3%80%80'])specs.push(['query-'+q,'GET',base+'?'+q])
specs.push(['rich-list','GET',base+'?sceneId=s'],['rich-head','HEAD',base+'?sceneId=s'],['rich-filter','GET',base+'?sceneId=s&status=active'],['rich-conflict','POST',base+'/p/validate','{"sceneId":"s","expectedVersion":2}'],['rich-ineligible','POST',base+'/p/review','{"sceneId":"s","expectedVersion":1,"status":"active"}'],['rich-delete','DELETE',base+'/p','{"sceneId":"s","expectedVersion":1}'])
const cases=[]
for(const [name,method,path,body,jsonType=true,token='synthetic-http-test-token'] of specs){const store=new Store(':memory:');const engine=new Engine(store);try{
 store.db.prepare('INSERT INTO scenes(id,value) VALUES(?,?)').run('s',JSON.stringify({id:'s',kind:'group',learningEnabled:true}))
 if(name.startsWith('rich-')||name.startsWith('clean-')){
 store.db.prepare('INSERT OR REPLACE INTO config(key,value) VALUES(?,?)').run('settings',JSON.stringify({...store.settings,memoryExperienceEnabled:true}))
 store.db.prepare('INSERT INTO config(key,value) VALUES(?,?)').run('identity:s:alice',JSON.stringify(initial.identity))
 store.db.prepare('INSERT INTO memory_experience_policies VALUES(?,?,?,?)').run('p','s',1,JSON.stringify(initial.policy))
 store.db.prepare('INSERT INTO memory_experience_outcomes VALUES(?,?,?,?,?)').run('outcome','root','s','instruction',JSON.stringify(initial.outcome))
 store.db.prepare('INSERT INTO memory_experience_validations VALUES(?,?,?)').run('receipt','p',JSON.stringify(initial.receipt))
 store.db.exec('CREATE TABLE IF NOT EXISTS memory_experience_trials(policy TEXT PRIMARY KEY,scene TEXT NOT NULL,version INTEGER NOT NULL,value TEXT)')
 store.db.prepare('INSERT INTO memory_experience_trials VALUES(?,?,?,?)').run('p','s',1,JSON.stringify(initial.trial))
 }
 if(name.startsWith('clean-')){
 const policy=name==='clean-review-active'?initial.validatedPolicy:initial.cleanPolicy
 store.db.prepare('UPDATE memory_experience_policies SET version=?,value=? WHERE id=?').run(policy.version,JSON.stringify(policy),'p')
 store.db.prepare('UPDATE memory_experience_validations SET value=? WHERE id=?').run(JSON.stringify(initial.cleanReceipt),'receipt')
 store.db.exec('DELETE FROM memory_experience_trials')
 }
 const app=createApp(store,{engine,token:'synthetic-http-test-token'})
 const response=await app.request('http://example.test'+path,{method,headers:{Authorization:'Bearer '+token,...(jsonType?{'Content-Type':'application/json'}:{})},...(body!==undefined?{body}:{})})
 cases.push({name,method,path,body:body??null,jsonType,token,status:response.status,text:(await response.text()).replace(/"requestId":"[^"]+"/g,'"requestId":"<request-id>"')})
}finally{await engine.dispose();store.close()}}
const files=['src/memory/interfaces/experience-routes.ts','src/api.ts','src/admin.ts','src/plugins/builtin/snowluma/profiles.ts','node_modules/hono/dist/utils/url.js','node_modules/hono/dist/validator/validator.js','node_modules/zod/package.json','node_modules/zod/v4/core/checks.js','node_modules/zod/v4/core/util.js','package-lock.json']
const result={initial,node:process.version,sourceHashes:Object.fromEntries(files.map(p=>[p,createHash('sha256').update(readFileSync(resolve(root,p))).digest('hex')])),cases}
const path=new URL('../internal/httpapi/testdata/experience-source.json',import.meta.url);const text=JSON.stringify(result,null,2)+'\n';if(process.argv.includes('--check'))assert.equal(readFileSync(path,'utf8'),text);else writeFileSync(path,text)
console.log(cases.map(c=>[c.name,c.status,c.text]))
