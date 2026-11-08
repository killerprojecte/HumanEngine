// Actual source Runtime / ExperienceRuntime, synthetic services and bodies.
import assert from 'node:assert/strict'
import { readFileSync,writeFileSync } from 'node:fs'
import {resolve,join} from 'node:path'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT)
const {ExperienceRuntime,ExperienceProposalSchema}=await import(pathToFileURL(join(root,'src/memory/experience/runtime-hints.ts')))
const {Runtime}=await import(pathToFileURL(join(root,'src/runtime.ts')))
const now=1793232000000;let timeNow=now;Date.now=()=>timeNow
const hint={id:'p',version:1,hash:'h',action:'exact_person_before_semantic',condition:{kind:'person_question',personId:'alice'},tier:'mechanical_only',expiresAt:'2026-11-05T00:00:00.000Z',eligibilityBasis:'explicit_instruction_trial',trialExpiresAt:'2026-11-05T00:00:00.000Z',reportedHelpfulCount:0}
const outcome={id:'o',version:1,sceneId:'s',rootId:'r',rootFamily:'f',source:{messageId:'m',senderId:'alice',eventRef:null},target:{messageId:'b',turnId:'t',eventRef:null},code:'request_exact_person_lookup',observedAt:new Date(now).toISOString()}
const context=()=>({scene:{id:'s'},mode:'real',turnId:'t',signal:new AbortController().signal,dependencies:new Set(),validations:[],deferMemoryExposure:true})
const cases=[]
for(const name of ['valid','identity-copy','mutated-view','mutated-current','mixed','empty','aborted']){
 let current=structuredClone(outcome);const service={listOutcomes:()=>[current]},r=new ExperienceRuntime(service,()=>undefined),c=context();const values=r.outcomes(c)
 if(name==='identity-copy')values[0]=structuredClone(values[0]);if(name==='mutated-view')values[0].code='wrong';if(name==='mutated-current')current.code='wrong';if(name==='mixed')values.push(structuredClone(values[0]));if(name==='empty')values.length=0;if(name==='aborted')c.signal=AbortSignal.abort()
 const delivered=r.deliverOutcomes(c,values);cases.push({name,view:r.outcomes(context())[0],accepted:delivered.length,dependencies:[...c.dependencies],validations:c.validations.length})
}
const planners=[]
for(const text of ['我是谁？','what is my plan?','quote "who am I?"','hello']){
 const queries=[],uses=[];const r=new ExperienceRuntime({hints:(_,q)=>{queries.push(q);return q.personQuestion?[hint]:[]},recordUse:(_,u)=>uses.push(u),snapshotValid:()=>true},()=>undefined),c=context();const result=r.plannerContext(c,[{senderId:'alice',status:'available',parts:[{type:'text',text}]}]);const before=c.validations.map(f=>f());r.learningPaused('s');const after=c.validations.map(f=>f());planners.push({text,queries,uses,result:result??null,before,after})
}
const tools=[]
for(const name of ['person-first','semantic-first','ack-only']){
 const uses=[];const r=new ExperienceRuntime({hints:()=>[hint],recordUse:(_,u)=>{uses.push(u);return true},snapshotValid:()=>true},()=>undefined),c=context();r.plannerContext(c,[{senderId:'alice',status:'available',parts:[{type:'text',text:'我是谁？'}]}]);if(name==='semantic-first')r.deliveredTool(c,'memory.search',{},[]);if(name!=='ack-only')r.deliveredTool(c,'memory.person',{personId:'alice'},[{fact:'synthetic'}]);r.sent('s','t','b');tools.push({name,uses})
}
// Runtime.previewMessage executes its own real trim. Minimal store supports
// unchanged display projection for a non-OneBot scene.
const previews=[]
for(const spec of [{name:'tail500',n:502,index:0},{name:'boundary500',n:502,index:2},{name:'duplicate-first',n:3,index:0,duplicate:true},{name:'expired',n:1,index:0,expired:true},{name:'idle-disabled',n:1,index:0,disabled:true},{name:'timer-disabled',n:1,index:0,disabled:true,timer:true},{name:'simulation',n:502,index:0,disabled:true,simulation:true}]){
 const scene=spec.simulation?'sim:s':'s';const messages=Array.from({length:spec.n},(_,i)=>({id:spec.duplicate?'same':String(i),sceneId:scene,receivedAt:new Date(now-(spec.expired?3600001:0)).toISOString(),senderId:'alice',origin:spec.simulation?'simulation':'live',status:'available',parts:[{type:'text',text:String(i)}]}))
 const state={messages,tasks:new Map(),timers:new Map(spec.timer?[['x',{}]]:[]),routing:{metadata:new Map(),acknowledged:new Map()},recalled:new Set()}
 const self={states:new Map([[scene,state]]),scenes:new Map([[scene,{id:scene,kind:spec.simulation?'simulation':'group'}]]),store:{settings:{cacheEnabled:!spec.disabled,cacheHours:1},scene:()=>({kind:'group'}),recalled:()=>false,presentMessage:m=>m},trim:Runtime.prototype.trim,pruneTopicHistory:()=>{}}
 const value=Runtime.prototype.previewMessage.call(self,scene,spec.duplicate?'same':String(spec.index));previews.push({...spec,result:value??null})
}
const {Store}=await import(pathToFileURL(join(root,'src/store.ts')))
const retentionSequences=[]
for(const name of ['expiry-expand','idle-reenable','expired-edit-readmit']){
 timeNow=now;const store=new Store(':memory:');store.set('settings',{...store.settings,cacheEnabled:true,cacheHours:1,sourceEventRevisionsEnabled:true});const scene={id:'s',kind:'group',adapterId:'synthetic',accountId:'account',platformSceneId:'s',name:'Synthetic',participationEnabled:false,learningEnabled:true,proactiveEnabled:false};store.saveScene(scene)
 const runtime=new Runtime(store,()=>{throw Error('unexpected model')});let previous='not-called';runtime.addExtension({edited:(before)=>{previous=before?.id??null}})
 const m={id:'m',sceneId:'s',senderId:'alice',origin:'live',status:'available',sentAt:new Date(now).toISOString(),receivedAt:new Date(now).toISOString(),parts:[{type:'text',text:'old'}]};runtime.ingest(m,scene,false)
 if(name==='idle-reenable')store.set('settings',{...store.settings,cacheEnabled:false});else timeNow=now+3600001
 const removed=runtime.previewMessage('s','m')??null
 store.set('settings',{...store.settings,cacheEnabled:true,cacheHours:4});const expanded=runtime.previewMessage('s','m')??null
 let editStatus=null,newText=null;if(name==='expired-edit-readmit'){const result=runtime.edit({...m,sentAt:new Date(timeNow).toISOString(),receivedAt:new Date(timeNow).toISOString(),parts:[{type:'text',text:'new'}]},scene,{editId:'edit-1',order:{kind:'platform_sequence',sequence:1}},false);editStatus=result.status;newText=runtime.previewMessage('s','m')?.parts[0]?.text??null}
 retentionSequences.push({name,removed:removed!==null,expanded:expanded!==null,previous,editStatus,newText});runtime.dispose();store.close()
}
timeNow=now
const proposals=[]
for(const [name,id,person]of [['ascii512','a'.repeat(512),'alice'],['ascii513','a'.repeat(513),'alice'],['astral512','😀'.repeat(512),'alice'],['astral513','😀'.repeat(513),'alice'],['lone512','\ud800'.repeat(512),'alice'],['lone513','\ud800'.repeat(513),'alice'],['person-astral512','o','😀'.repeat(512)],['person-astral513','o','😀'.repeat(513)]]){const input={action:'exact_person_before_semantic',condition:{kind:'person_question',personId:person},outcomeRefs:[{id,version:1}]};proposals.push({name,input,accepted:ExperienceProposalSchema.safeParse(input).success})}
const hashes=Object.fromEntries(['src/runtime.ts','src/memory/experience/runtime-hints.ts','node_modules/zod/v4/core/checks.js','node_modules/zod/v4/core/util.js','node_modules/zod/package.json'].map(p=>[p,createHash('sha256').update(readFileSync(join(root,p))).digest('hex')]))
const output=new URL('../internal/experienceservice/testdata/runtime-source.json',import.meta.url);const text=JSON.stringify({node:process.version,hashes,cases,planners,tools,previews,proposals,retentionSequences},null,2)+'\n';if(process.argv.includes('--check'))assert.equal(readFileSync(output,'utf8'),text);else writeFileSync(output,text);console.log(`Verified ${cases.length} exposure, ${planners.length} planner, ${tools.length} use, ${previews.length} exact preview, ${proposals.length} proposal boundary source cases`)
