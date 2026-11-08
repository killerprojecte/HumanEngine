/** Actual source stages; synthetic model/writer responses only. */
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {runLearningBatch} from '../../src/memory/learning/batch-runner.ts'
const date=Date;globalThis.Date=class extends date{constructor(...args:any[]){super(...(args.length?args:['2026-01-02T03:04:05.006Z']) as [any])}static now(){return 1767323045006}} as any
const call=(name:string,args:any)=>({id:name,name,arguments:args})
const base={content:'Synthetic durable fact',kind:'fact',sourceMessageIds:['m']}
const target={id:'memory',content:'Synthetic durable fact',kind:'fact',scope:{kind:'scene',sceneId:'s'},origin:'learned',status:'active',lineageStatus:'verified',locked:false,sources:[],learnedAt:'2026-01-01T00:00:00.000Z'}
const plans=[
 {name:'empty',responses:[[]]},
 {name:'empty-all-options',flags:7,responses:[[]]},
 {name:'invalid-reference-keeps-position',responses:[[call('learning.propose',{...base,sourceMessageIds:['missing']}),call('learning.propose',{...base,mergeIntoId:'missing',supersedesId:'missing'})]]},
 {name:'review',text:'Correction: this durable fact has changed.',responses:[[],[call('learning.review_empty',base)]]},
 {name:'repair',responses:[[call('learning.propose',{...base,content:'She uses tools.'})],[call('learning.repair',{candidateIndex:0,content:base.content})]]},
 {name:'reconcile',target:true,responses:[[call('learning.propose',base)],[call('learning.reconcile',{candidateIndex:0,targetId:'memory',action:'merge'})]]},
 {name:'habit-first',responses:[[call('learning.propose',base),call('habits.propose',{kind:'botExpression',preference:'concise',direction:1,sourceMessageIds:['m']})]]},
]
const cases:any[]=[]
try{for(const p of plans){const plan:any=p,flags=plan.flags??0,requests:any[]=[],writes:any[]=[],logs:any[]=[],flow:any[]=[];let index=0
 const messages=[{id:'m',sceneId:'s',senderId:'p',origin:'live',status:'available',parts:[{type:'text',text:plan.text??'I prefer concise replies.'}],sentAt:'2026-01-01T00:00:00.000Z',receivedAt:'2026-01-01T00:00:00.000Z'}]
 const store:any={settings:{timezone:'UTC',memoryGraphEnabled:!!(flags&1),memoryCommitmentsEnabled:!!(flags&2),memoryKnowledgeEnabled:!!(flags&4)},scene:(id:string)=>({id,learningEnabled:true}),recalled:()=>false,log:(_l:string,_m:string,code:string,context:any)=>{logs.push(code);if(_m==='flow')flow.push({stage:context.stage,status:context.status,...(context.reason?{reason:context.reason}:{}),...(context.nextStep?{nextStep:context.nextStep}:{}),...(context.role?{role:context.role}:{}),...(context.counts?{counts:context.counts}:{})})}}
 const ports:any={foregroundBusy:()=>false,router:()=>({complete:async(role:string,messages:any[],tools:any[])=>{requests.push({role,messages,tools});return {text:'',toolCalls:structuredClone(plan.responses[index++]??[]),modelId:'synthetic'}}}),searchMemory:async()=>[],unsupportedSourcePronouns:(_s:string,v:any)=>v.content.startsWith('She '),memory:{adminList:()=>plan.target?[target]:[],reconciliationTargets:()=>({targets:plan.target?[{memory:target,candidateIndexes:[0]}]:[],coverage:'complete',limits:{perCandidate:4,perGroup:8,total:16,semantic:8}})},proposeSource:async(scene:string,input:any,evidence:any[])=>{writes.push({kind:'memory',scene,input,evidence});return {status:'saved'}},proposeHabit:(scene:string,simulation:boolean,input:any,evidence:any[])=>{writes.push({kind:'habit',scene,input,evidence});return {status:'saved'}}}
 const progress={applying:false,writePending:false,totals:{candidateCount:0,savedCount:0,rejectedCount:0,ignoredCount:0}}
 const completed=await runLearningBatch({sceneId:'s',messages:messages as any,signal:new AbortController().signal,store,ports,isDisposed:()=>false,progress})
 cases.push({plan,messages,requests,writes,flow,progress,completed})
}}finally{globalThis.Date=date}
const sourcePaths=['src/memory/learning/batch-runner.ts','src/memory/learning/extraction.ts','src/memory/learning/repair.ts','src/memory/learning/reconciliation.ts','src/memory/learning/persistence.ts','src/memory/learning/contracts.ts','src/memory/learning/subject-input.ts','src/runtime/context-projection.ts']
const sources=Object.fromEntries(sourcePaths.map(path=>[path,createHash('sha256').update(readFileSync(path)).digest('hex')]))
const path='backend/internal/learningbatch/testdata/stages.json',output=JSON.stringify({sources,cases},null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==output)throw Error('source stage fixture differs')}else writeFileSync(path,output)
// Tool contracts below are taken from actual stage calls, not copied schemas.
const tools=Object.fromEntries(cases.flatMap(c=>c.requests.flatMap((r:any)=>r.tools.filter((t:any)=>['learning.repair','learning.reconcile'].includes(t.name)).map((t:any)=>[t.name,t.inputSchema]))))
const schemaPath='backend/internal/learningbatch/stage-schemas.json',schema=JSON.stringify(tools,null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(schemaPath,'utf8')!==schema)throw Error('source stage schemas differ')}else writeFileSync(schemaPath,schema)
