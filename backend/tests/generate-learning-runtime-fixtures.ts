/** Synthetic deterministic traces of the actual source coordinator and batch pipeline. */
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { LearningCoordinator } from '../../src/memory/learning/coordinator.ts'
import { ModelError } from '../../src/models.ts'
import { currentBudgetPriority } from '../../src/budget/native-attempt.ts'
import { effectiveModelPriority } from '../../src/scheduler.ts'
const realDate=Date, realTimeout=setTimeout, realClear=clearTimeout
let now=0, sequence=0
const timers=new Map<number,{at:number,fn:()=>void}>()
globalThis.Date=class extends realDate { constructor(...args:any[]){super(...(args.length?args:[now]) as [any])}static now(){return now} } as any
globalThis.setTimeout=((fn:()=>void,delay=0)=>{const id=++sequence;timers.set(id,{at:now+Math.max(0,delay),fn});return {id,unref(){return this}}}) as any
globalThis.clearTimeout=((t:any)=>{if(t)timers.delete(t.id)}) as any
const flush=()=>new Promise<void>(resolve=>setImmediate(resolve))
const plans:any[]=[
 {name:'quiet-maxwait',phases:['empty'],steps:[['enqueue','s','a'],['tick',5],['enqueue','s','b'],['tick',10],['tick',15]]},
 {name:'retry',phases:['retryable','empty'],steps:[['enqueue','s','a'],['tick',10],['tick',14],['tick',15]]},
 {name:'manual-retry',phases:['failure','empty'],steps:[['enqueue','s','a'],['tick',10],['retry'],['tick',39],['tick',40]]},
 {name:'discard',phases:['failure'],steps:[['enqueue','s','a'],['tick',10],['discard']]},
 {name:'recall-edit-survivors',phases:['hold','empty'],steps:[['enqueue','s','a'],['enqueue','s','b'],['tick',10],['recall','s','a'],['enqueue','s','a','replacement'],['release'],['tick',40]]},
 {name:'off-on-no-revival',phases:['hold','empty'],steps:[['enqueue','s','a'],['enqueue','s','b'],['tick',10],['recall','s','a'],['cancel','s'],['enqueue','s','c'],['release'],['tick',20]]},
 {name:'partial-unknown-zero-saved',phases:['partial'],steps:[['enqueue','s','a'],['tick',10],['retry']]},
 {name:'ignored-before-cancel',phases:['ignored-cancel'],steps:[['enqueue','s','a'],['enqueue','s','b'],['tick',10],['tick',40]]},
 {name:'saved-before-cancel',phases:['saved-cancel'],steps:[['enqueue','s','a'],['enqueue','s','b'],['tick',10],['tick',40]]},
 {name:'rejected-before-cancel',phases:['rejected-cancel'],steps:[['enqueue','s','a'],['enqueue','s','b'],['tick',10],['tick',40]]},
 {name:'foreground-fairness',busy:true,phases:['empty','empty'],steps:[['enqueue','b','x'],['enqueue','a','y'],['tick',999],['tick',1000],['tick',2000]]},
 {name:'capacity',capacity:2,phases:['hold'],steps:[['enqueue','s','a'],['enqueue','s','b'],['enqueue','s','c'],['tick',10],['recall','s','a'],['enqueue','s','a','replacement'],['release']]},
]
const cases:any[]=[]
try{for(const plan of plans){
 now=0;timers.clear();let busy=!!plan.busy;let queue:LearningCoordinator;let calls=0;let release:undefined|(()=>void);let phase='';let write=0
 const started:any[]=[],writes:any[]=[],events:any[]=[],trace:any[]=[],recalled=new Set<string>()
 const settings={learningBatchMs:10,learningMaxWaitMs:20,learningMinIntervalMs:30,learningQueueMaxMessages:plan.capacity??100,learningQueueMaxTotalMessages:plan.capacity??100,learningRetryAttempts:3,learningRetryBaseMs:5,timezone:'UTC',memoryGraphEnabled:false,memoryCommitmentsEnabled:false,memoryKnowledgeEnabled:false}
 const store:any={settings,scene:(id:string)=>({id,learningEnabled:true,memoryReadEnabled:false}),recalled:(s:string,id:string)=>recalled.has(s+'|'+id),log:(_level:string,_module:string,_code:string,d:any)=>{if(d?.stage==='learning.batch'&&!['started','cancel_requested'].includes(d.status))events.push({status:d.status,reason:d.reason,nextStep:d.nextStep,counts:d.counts})}}
 const empty={text:'',toolCalls:[],modelId:'synthetic'}
 const ports:any={foregroundBusy:()=>busy,router:()=>({complete:async(_role:string,messages:any[])=>{
 phase=plan.phases[calls++]??'empty';write=0;const payload=JSON.parse(messages[1].content);started.push({scene:(queue as any).overdueLearningScene??[...(queue as any).learningActiveMessages.keys()].at(-1),ids:payload.messages.map((m:any)=>m.id),texts:payload.messages.map((m:any)=>m.parts.filter((p:any)=>p.type==='text').map((p:any)=>p.text).join(' ')),priority:effectiveModelPriority(0),budget:currentBudgetPriority(0)})
 if(phase==='hold')await new Promise<void>(resolve=>{release=resolve})
 if(phase==='retryable')throw new ModelError('synthetic_retry',503,true)
 if(phase==='failure')throw new Error('synthetic_failure')
 if(phase==='partial'||phase.endsWith('-cancel'))return {...empty,toolCalls:[0,1].map(i=>({id:'c'+i,name:'learning.propose',arguments:{content:'Synthetic durable value '+i,kind:'fact',sourceMessageIds:[payload.messages[0].id]}}))}
 return empty
 }}),searchMemory:async()=>[],memory:{adminList:()=>[],reconciliationTargets:()=>({targets:[],coverage:'complete',limits:{}})},unsupportedSourcePronouns:()=>false,proposeHabit:()=>{throw Error('unexpected habit')},proposeSource:async(scene:string,input:any)=>{
 writes.push({scene,content:input.content});write++
 if(phase==='partial'&&write===2)throw new Error('synthetic_postcommit_failure')
 if(phase.endsWith('-cancel'))queue.recall(scene,'a')
 return {status:phase==='saved-cancel'?'saved':phase==='rejected-cancel'?'rejected':'ignored',reason:'synthetic'}
 }}
 queue=new LearningCoordinator(store,ports)
 const message=(scene:string,id:string,text='synthetic')=>({id,sceneId:scene,senderId:'person',origin:'live',status:'available',sentAt:'2026-01-01T00:00:00.000Z',receivedAt:'2026-01-01T00:00:00.000Z',parts:[{type:'text',text}]})
 const snapshot=()=>{const status=queue.learningStatus();status.failures=status.failures.map((f:any)=>({...f,id:'failure',createdAt:'clock'}));const internal:any=queue;const batches=(m:Map<string,any>,field?:string)=>[...m].sort(([a],[b])=>a.localeCompare(b)).map(([s,v])=>[s,(field?v[field]:v).map((m:any)=>m.id)]);return {status,queued:batches(internal.learningBatches),active:batches(internal.learningActiveMessages),survivors:batches(internal.learningSurvivors),retries:[...internal.learningRetries].map(([s,r]:any)=>[s,r.attempt,r.due,r.messages.map((m:any)=>m.id)]),due:[...internal.learningTimerDue].sort(([a],[b])=>a.localeCompare(b)),started:structuredClone(started),writes:structuredClone(writes)}}
 for(const step of plan.steps){let result:any=null
 switch(step[0]){
 case 'enqueue':queue.enqueueLearning(message(step[1],step[2],step[3]) as any);break
 case 'tick':now=step[1];for(;;){const due=[...timers].filter(([,t])=>t.at<=now).sort((a,b)=>a[1].at-b[1].at||a[0]-b[0]);if(!due.length)break;for(const [id,t]of due){if(timers.delete(id))t.fn()}await flush()}break
 case 'recall':queue.recall(step[1],step[2]);break
 case 'cancel':queue.cancelLearning(step[1]);break
 case 'release':release?.();release=undefined;break
 case 'retry':result=queue.retryLearning(queue.learningStatus().failures[0]?.id??'missing');break
 case 'discard':result=queue.discardLearning(queue.learningStatus().failures[0]?.id??'missing');break
 }
 await flush();trace.push({step,result,...snapshot()})
 }
 queue.dispose();release?.();await flush();cases.push({plan,trace,events})
}}
finally{globalThis.Date=realDate;globalThis.setTimeout=realTimeout;globalThis.clearTimeout=realClear}
const paths=['src/memory/learning/coordinator.ts','src/memory/learning/admission.ts','src/memory/learning/batch-runner.ts','src/memory/learning/persistence.ts','src/memory/learning/extraction.ts','src/memory/learning/repair.ts','src/memory/learning/reconciliation.ts']
const sources=Object.fromEntries(paths.map(p=>[p,createHash('sha256').update(readFileSync(p)).digest('hex')]))
const path='backend/internal/learningruntime/testdata/source.json',output=JSON.stringify({sources,cases},null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==output)throw Error('source coordinator fixture changed')}else writeFileSync(path,output)
