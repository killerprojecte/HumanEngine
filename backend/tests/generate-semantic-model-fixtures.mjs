import Database from 'better-sqlite3'
import fs from 'node:fs'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import {SemanticService} from '../../src/memory/semantic/candidates.ts'
import {semanticHash} from '../../src/memory/semantic/contracts.ts'
import {SemanticExposure,semanticModelPage,SemanticStateEvidenceSchema} from '../../src/memory/read/semantic-exposure.ts'
import {definition} from '../../src/memory/learning/contracts.ts'
import {z} from 'zod'
const engine=fs.readFileSync('src/engine.ts','utf8'), at=engine.indexOf("'memory.summaries',"),start=engine.lastIndexOf('definition(',at),end=engine.indexOf('\n                ),',at)+18
const tool=new Function('definition','z',`return ${engine.slice(start,end).trim().replace(/,$/,'')}`)(definition,z)
const flagsStart=engine.lastIndexOf('...(settings.memorySemanticEnabled',at), flagsEnd=engine.indexOf('\n          ...(settings.memoryWorkingSetEnabled',at)
const expression=engine.slice(flagsStart,flagsEnd).trim().replace(/,$/,'')
const flagCases=[]
for(const memorySemanticEnabled of [false,true])for(const memorySemanticAnswerEnabled of [false,true]){
 const settings={memorySemanticEnabled,memorySemanticAnswerEnabled}
 const tools=new Function('settings','definition','z',`return [${expression}]`)(settings,definition,z)
 assert.equal(tools.length,memorySemanticEnabled&&memorySemanticAnswerEnabled?1:0)
 flagCases.push({settings,tools})
}
const sourceOrder={advertisement:['learning.propose','memory.summaries','memory.workingSet'],capture:['this.workspaceExposure.captureGuard(context)','this.knowledgeExposure.captureGuard(context)','this.semanticExposure.captureGuard(context)']}
const capture=engine.slice(engine.indexOf('captureToolResultGuards:'))
assert(capture.indexOf(sourceOrder.capture[0])<capture.indexOf(sourceOrder.capture[1]));assert(capture.indexOf(sourceOrder.capture[1])<capture.indexOf(sourceOrder.capture[2]))
const execute=engine.slice(engine.indexOf('execute: async (name, input, context)'))
assert(execute.indexOf('semantic_read_not_learning_authority')<execute.indexOf('identity_read_not_learning_authority'))
const ctx={sceneId:'s',simulation:false}, now=Date.parse('2026-01-02T03:04:05.006Z')
const ref=id=>({id,version:1,deletionEpoch:0})
const original=id=>({record:{id,scope:{kind:'scene',sceneId:'s'},kind:'fact',epistemicKind:'explicit',status:'active',origin:'learned',learnedAt:'2026-01-01T00:00:00.000Z',validUntil:'2026-02-01T00:00:00.000Z',content:'Original '+id,sources:[{sceneId:'s',messageId:'msg-'+id,senderId:'p'}]},proofs:[{proofId:'proof-'+id,sources:[{sceneId:'s',messageId:'msg-'+id,senderId:'p'}],parents:[]}],dependencyKey:semanticHash(id),roots:[semanticHash('root-'+id)],rootCoverage:'complete',derived:false})
const cases=[]
for(const scenario of ['current','expired','disabled','simulation','partition-drift','proof-drift','missing-parent','root-subset','limited']){
 const db=new Database(':memory:');let clock=now,enabled=true,partition=semanticHash('partition'),ids=0;const views={a:original('a'),b:original('b')}
 const service=new SemanticService(db,{assertReady(){},enabled:()=>enabled,learningEnabled:()=>true,autoSummary:()=>true,current:(_c,r)=>views[r.id],anchorCurrent:()=>undefined,episodeAnchor:()=>false,partitionKey:()=>partition},{now:()=>clock,id:()=>`candidate-${++ids}`})
 const selections=['a','b'].map(id=>({ref:ref(id),dependencyKey:semanticHash(id)}))
 const admitted=db.transaction(()=>service.admit(ctx,selections,[{kind:'summary_candidate',selectedParentIds:['a','b'],text:'Attributed summary',hypothesis:false},...(scenario==='limited'?[{kind:'duplicate_candidate',selectedParentIds:['a','b'],text:'Possible duplicate',hypothesis:true}]:[])]))()
 assert.equal(admitted.rejected.length,0);const stored=service.storage.rows()
 if(scenario==='expired')clock=Date.parse('2099-01-01T00:00:00Z')
 if(scenario==='disabled')enabled=false
 if(scenario==='partition-drift')partition=semanticHash('changed')
 if(scenario==='proof-drift')views.b.proofs[0].sources[0].messageId='changed'
 if(scenario==='missing-parent')delete views.b
 if(scenario==='root-subset')views.b.roots=[semanticHash('other-root')]
 const page=service.list({...ctx,simulation:scenario==='simulation'},scenario==='limited'?1:12)
 cases.push({scenario,stored,views,enabled,partition,now:clock,limit:scenario==='limited'?1:12,simulation:scenario==='simulation',page,model:semanticModelPage({...page,items:page.items.filter(v=>v.candidate.kind!=='summary_candidate'||v.candidate.status==='admitted')})})
 db.close()
}
const raw=cases[0].page,receipts=[]
for(const scenario of ['original','copy','foreign','policy','flag','changed','abort-before-deliver','detached','out-of-order','reentry']){
 let enabled=true,revision=0,page=structuredClone(raw),reenter=false,receipt;const abort=new AbortController(), context=()=>({mode:'real',scene:{id:'s'},signal:abort.signal,dependencies:new Set(),validations:[]}),c=context();const trace=[];let exposure
 exposure=new SemanticExposure({allowed:()=>{trace.push('allowed');return enabled},policyIdentity:()=>{trace.push('policy');return revision},query:()=>{trace.push('query');if(reenter){reenter=false;exposure.deliver(c,receipt)};return page}})
 trace.push('prepare A');receipt=exposure.query(c);assert.equal(receipt.length,1);assert.equal(exposure.captureGuard(c),undefined)
 let sibling;if(scenario==='out-of-order'){trace.push('prepare B');sibling=exposure.query(c)}
 if(scenario==='policy')revision++;if(scenario==='flag')enabled=false;if(scenario==='changed')page.coverage='limited';if(scenario==='abort-before-deliver')abort.abort();if(scenario==='reentry')reenter=true
 if(scenario==='out-of-order')trace.push('deliver B')
 const delivered=exposure.deliver(scenario==='foreign'?context():c,scenario==='copy'?structuredClone(receipt):scenario==='out-of-order'?sibling:receipt)
 const accepted=['original','detached','out-of-order'].includes(scenario);assert.equal(delivered.length,accepted?1:0)
 if(accepted)assert.equal(delivered[0],scenario==='out-of-order'?sibling[0]:receipt[0])
 let leafProbe
 if(scenario==='out-of-order'){
  // Instrument only first-delivered B's captured leaf. All original checks still
  // run; this proves the A tail keeps B, independently of shared policy drift.
  const bGuard=exposure.captureGuard(c);let bAllowed=true
  exposure.tail.set(c,()=>bAllowed&&bGuard())
  trace.push('deliver A');assert.equal(exposure.deliver(c,receipt)[0],receipt[0])
  const aTail=exposure.captureGuard(c);assert.equal(aTail(),true)
  bAllowed=false;trace.push('deny B test probe');assert.equal(aTail(),false);bAllowed=true;trace.push('restore B test probe');assert.equal(aTail(),true)
  leafProbe={prepared:['A','B'],delivered:['B','A'],revoked:'B',latestTailValid:false,restoredTailValid:true,kind:'controlled leaf denial; actual owner query retained'}
 }
 let detached;if(scenario==='detached'){abort.abort();detached=exposure.captureGuard(c)();assert.equal(detached,true)}
 receipts.push({scenario,delivered:delivered.length,dependencies:[...c.dependencies],guardPresent:!!exposure.captureGuard(c),detached,leafProbe,trace})
}
const modelCases=[]
for(const scenario of ['valid','empty','missing-record-field','record-extra-stripped','proof-extra-rejected','bad-interpretation','summary-not-admitted','missing-parent','bad-ref','no-qualifications','wrong-kind-qualifications','internal-bool','null-person','astral-text-3001','astral-text-boundary','astral-text-overflow']){
 const input=structuredClone(cases[0].model),item=input.page.items[0]
 if(scenario==='empty')input.page.items=[]
 if(scenario==='missing-record-field')delete item.declarations[0].record.learnedAt
 if(scenario==='record-extra-stripped')item.declarations[0].record.extra='ignored'
 if(scenario==='proof-extra-rejected')item.parents[0].proofs[0].extra='bad'
 if(scenario==='bad-interpretation')item.interpretation='possible_duplicate_not_merged'
 if(scenario==='summary-not-admitted')item.status='candidate'
 if(scenario==='missing-parent')item.parents.pop()
 if(scenario==='bad-ref')item.declarations[0].ref.version++
 if(scenario==='no-qualifications')item.kind='episode_candidate'
 if(scenario==='wrong-kind-qualifications')item.anchorQualifications=[{}]
 if(scenario==='internal-bool')item.declarations[0].record.internalUse=true
 if(scenario==='null-person')item.personId=null
 if(scenario==='astral-text-3001')item.text='😀'.repeat(3001)
 if(scenario==='astral-text-boundary')item.text='😀'.repeat(6000)
 if(scenario==='astral-text-overflow')item.text='😀'.repeat(6001)
 const result=SemanticStateEvidenceSchema.safeParse(input);modelCases.push({scenario,input,success:result.success,...(result.success?{data:result.data}:{issues:result.error.issues})})
}
const files=['src/engine.ts','src/memory/semantic/candidates.ts','src/memory/semantic/storage.ts','src/memory/read/semantic-exposure.ts','src/memory/read/state-exposure.ts']
const out={sources:Object.fromEntries(files.map(f=>[f,crypto.createHash('sha256').update(fs.readFileSync(f)).digest('hex')])),tool,flagCases,sourceOrder,cases,receipts,modelCases,scope:'Real TS SemanticService admit/storage/list and SemanticExposure; synthetic current-authority fixture, not native persisted application proof'}
const target='backend/internal/semantic/testdata/model-source.json',encoded=JSON.stringify(out,null,2)+'\n'
const toolPath='backend/internal/app/testdata/semantic-tool-source.json', toolEncoded=JSON.stringify(tool,null,2)+'\n'
if(process.argv.includes('--check'))assert.equal(fs.readFileSync(toolPath,'utf8'),toolEncoded);else fs.writeFileSync(toolPath,toolEncoded)
if(process.argv.includes('--check'))assert.equal(fs.readFileSync(target,'utf8'),encoded);else fs.writeFileSync(target,encoded)
console.log(JSON.stringify({cases:cases.length,receipts:receipts.length,tool:tool.name}))
