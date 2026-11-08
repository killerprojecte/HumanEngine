/** Synthetic only. Executes actual source capture, request and admission implementations. */
import { writeFileSync, readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { z } from 'zod'
import { SemanticCoordinator } from '../../src/memory/semantic/coordinator.ts'
import { LearningAdmission } from '../../src/memory/learning/admission.ts'
import { captureSemanticInput, semanticInputUnchanged } from '../../src/memory/semantic/input.ts'
import { requestSemanticProposals } from '../../src/memory/semantic/request.ts'
import { SemanticProposalSchema } from '../../src/memory/semantic/contracts.ts'
const h=(x:string)=>createHash('sha256').update(x).digest('hex')
const entry=(id:string,personId:any='person')=>({selection:{ref:{id,version:1,deletionEpoch:0},dependencyKey:h(id)},assertion:{record:{id,scope:{kind:'scene',sceneId:'scene',...(personId===null?{}:{personId})},content:'Synthetic '+id,kind:'fact',epistemicKind:'reported'},derived:false,dependencyKey:h(id),rootCoverage:'complete',proofs:[{proofId:'proof-'+id,sources:[{sceneId:'scene',messageId:'msg-'+id,senderId:'person'}],parents:[]}],roots:[h('root-'+id)]}})
const base={coverage:'complete',partitionKey:h('partition'),entries:[entry('b'),entry('a')]}
const captures:any[]=[]
function capture(name:string,r:any){const result=captureSemanticInput('scene',r);captures.push({name,resolved:r,expected:result??null,unchanged:result?semanticInputUnchanged(result,r):false});return result}
const snapshot=capture('same-subject',base)!
capture('limited',{...base,coverage:'limited'})
capture('one',{...base,entries:[entry('a')]})
capture('duplicate',{...base,entries:[entry('a'),entry('a')]})
capture('cross-person',{...base,entries:[entry('a'),entry('b','other')]})
capture('derived',{...base,entries:[entry('a'),{...entry('b'),assertion:{...entry('b').assertion,derived:true}}]})
capture('no-partition',{...base,partitionKey:undefined})
capture('same-root',{...base,entries:[entry('a'),{...entry('b'),assertion:{...entry('b').assertion,roots:entry('a').assertion.roots}}]})
capture('unicode-id-bound',{...base,entries:[entry('😀'.repeat(257)),entry('b')]})
capture('unicode-order',{...base,entries:[entry('\ue000'),entry('😀')]})
capture('episode',{...base,entries:[entry('a'),entry('b','other')],episodeAnchor:{id:'anchor',version:1},episodeMembershipKey:h('member'),anchorDependencies:[{ref:{id:'anchor',version:1,deletionEpoch:0},fingerprint:h('anchor'),proofs:entry('a').assertion.proofs}],anchorQualifications:[{synthetic:'capture intentionally trusts resolved metadata'}]})
const requests:any[]=[]
const realDate=Date;globalThis.Date=class extends realDate{constructor(...args:any[]){super(...(args.length?args:['2026-01-02T03:04:05.006Z']) as [any])}} as any
for(const [name,toolCalls] of Object.entries({silent:[],summary:[{name:'memory.semantic_propose',arguments:{kind:'summary_candidate',selectedParentIds:['a','b'],text:'Attributed synthetic summary',hypothesis:false}}],unexpected:[{name:'other',arguments:{}}],unknown:[{name:'memory.semantic_propose',arguments:{kind:'summary_candidate',selectedParentIds:['a','missing'],text:'x',hypothesis:false}}],duplicate:[{name:'memory.semantic_propose',arguments:{kind:'summary_candidate',selectedParentIds:['a','a'],text:'x',hypothesis:false}}],tooMany:Array.from({length:4},()=>({name:'x',arguments:{}}))})){
 let wire:any;let guards=0;try{const result=await requestSemanticProposals({snapshot,router:{complete:async(role,messages,tools)=>{wire={role,messages,tools};return {toolCalls} as any}},signal:new AbortController().signal,assertCurrent:()=>{guards++},timezone:'UTC'});requests.push({name,toolCalls,wire,result,guards})}catch(error){requests.push({name,toolCalls,wire,error:(error as Error).message,guards})}
}
globalThis.Date=realDate
let now=0;const admission=new LearningAdmission({now:()=>now,quietMs:10,maxWaitMs:20,minIntervalMs:30,maxSourcesPerScene:3,maxSourcesTotal:5,maxScenes:3});const trace:any[]=[]
const step=(op:string,args:any[],f:()=>any)=>trace.push({op,args,result:f()??null})
step('admit',['a','1','background'],()=>admission.admit('a','1','background'));step('admit',['a','1','background'],()=>admission.admit('a','1','background'));now=10;step('now',[now],()=>null);step('inspect',['a'],()=>admission.inspect('a'));step('take',['a',1],()=>admission.take('a',1));step('admit',['a','1','background'],()=>admission.admit('a','1','background'));step('admit',['a','2','background'],()=>admission.admit('a','2','background'));step('finish',['a',1,5],()=>admission.finish('a',1,5));step('inspect',['a'],()=>admission.inspect('a'));now=40;step('now',[now],()=>null);step('take',['a',1],()=>admission.take('a',1));step('recall',['a','1',true],()=>admission.recall('a','1',{requeueSurvivors:true}));step('inspect',['a'],()=>admission.inspect('a'));step('cancelScene',['a'],()=>admission.cancelScene('a'));step('setEnabled',[false],()=>admission.setEnabled(false));step('admit',['a','3','background'],()=>admission.admit('a','3','background'))
const coordinators:any[]=[]
for(const scenario of ['silent','request-failed','revision-revoked','unchanged','no-input','receipt-failed']){
 let revision=0;const events:any[]=[];const receipts:string[]=[];let coordinator:SemanticCoordinator
 const ticket={personalizationEnabled:true};
 coordinator=new SemanticCoordinator({readPolicy:{require:()=>ticket,valid:(t:any)=>t===ticket} as any,enabled:()=>true,policyRevision:()=>revision,foregroundBusy:()=>false,timezone:()=> 'UTC',resolveDirty:()=>scenario==='no-input'?undefined:base as any,valid:()=>true,router:(guard)=>({complete:async()=>{guard();if(scenario==='request-failed')throw new Error('synthetic failure');if(scenario==='revision-revoked'){revision++;coordinator.configure()}return {toolCalls:[]} as any}}),beginAttempt:()=>{if(scenario==='unchanged')return false;receipts.push('started');return true},finishAttempt:(_snapshot,status)=>{receipts.push(status);if(scenario==='receipt-failed')throw new Error('synthetic receipt failure')},commit:()=>({created:0,rejected:0}),event:(event)=>events.push(event)}, {quietMs:0,maxWaitMs:0,minIntervalMs:0,pollMs:10})
 const accepted=coordinator.notify({sceneId:'scene',personId:'person',trigger:base.entries[0]!.selection,reason:'assertion_committed'});await coordinator.whenIdle();coordinators.push({scenario,accepted,stats:coordinator.stats(),events,receipts});coordinator.dispose()
}
const paths=['src/memory/semantic/input.ts','src/memory/semantic/request.ts','src/memory/semantic/coordinator.ts','src/memory/semantic/contracts.ts','src/memory/learning/admission.ts'];const sources=Object.fromEntries(paths.map(path=>[path,h(readFileSync(path,'utf8'))]));const outputPath='backend/internal/semantic/testdata/runtime-source.json'
const output=JSON.stringify({sources,schema:JSON.stringify(z.toJSONSchema(SemanticProposalSchema)),captures,requests,trace,coordinators},null,2)+'\n'
if(process.argv.includes('--check')) {
  if(readFileSync(outputPath,'utf8')!==output) throw new Error('semantic runtime source fixture differs')
} else writeFileSync(outputPath,output)
