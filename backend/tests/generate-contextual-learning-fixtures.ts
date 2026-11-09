/** Executes the actual source queue/proposals; fixture ports are synthetic. */
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { ContextualLearningQueue } from '../../src/memory/learning/contextual-queue.ts'
import { ContextualProposals } from '../../src/memory/learning/contextual-proposals.ts'
import { currentBudgetPriority } from '../../src/budget/native-attempt.ts'
import { effectiveModelPriority } from '../../src/scheduler.ts'
const queues:any[]=[]
for(const mode of ['completed','failed','discarded','committed','cancelled']){
 let busy=true,valid=true,release!:()=>void,started!:()=>void
 const ready=new Promise<void>(r=>started=r), gate=new Promise<void>(r=>release=r),events:any[]=[]
 const q=new ContextualLearningQueue({foregroundBusy:()=>busy,onEvent:e=>events.push(e)})
 const job={sceneId:'s',key:mode,sourceIds:['b','a','a'],validate:()=>valid,committed:(r:any)=>r==='saved',run:async()=>{started();await gate;if(mode==='failed')throw Error('PRIVATE_CANARY');if(mode==='discarded'||mode==='committed')valid=false;return mode==='committed'||mode==='cancelled'?'saved':undefined}}
 const result=q.enqueue(job),duplicate=q.enqueue({...job,sourceIds:['a','b']});busy=false;q.resume();await ready;if(mode==='cancelled')q.recall('s','a');release();await q.whenIdle();queues.push({mode,result,duplicate,events,stats:q.stats()});q.dispose()
}
const proposals:any[]=[]
for(const mode of ['basic','simulation','cancelled','disabled','missing-source','preview-registered','preview-ref-registered-error','registration-limited','entity-missing','entity-invalid','parent-invalid','read-invalid','knowledge-freeform','commitment-freeform','immutable','own-write','source-changed']){
 let busy=true,valid=true,committed=false
 const settings={memoryGraphEnabled:true,memoryGraphAnswerEnabled:true,memoryCommitmentsEnabled:true,memoryKnowledgeEnabled:true}
 const message:any={id:'a',sceneId:'s',senderId:'person',origin:'live',status:'available',sentAt:'2026-01-01T00:00:00.000Z',receivedAt:'2026-01-01T00:00:00.000Z',parts:[{type:'text',text:'Synthetic durable fact'}]}
 const store:any={settings,scene:()=>({id:'s',adapterId:'adapter',accountId:'account',learningEnabled:mode!=='disabled'}),recalled:()=>mode==='source-changed'&&!valid,sourceEvents:{registered:()=>{if(mode==='preview-ref-registered-error')throw Error('REGISTERED_MUST_NOT_RUN');return mode==='preview-registered'},hasEdits:()=>false,capture:()=>true},log:()=>{}}
 const writes:any[]=[]
 const p=new ContextualProposals(store,{memory:{list:()=>[{id:'parent'}],revisionState:()=>({version:1,deletionEpoch:0,deleted:mode==='parent-invalid'})},preview:()=>[message],foregroundBusy:()=>busy,commit:async(scene,candidate,evidence,parents,signal,entities,commitments,knowledge,edges)=>{writes.push({scene,candidate,evidence,parents,entities,commitments,knowledge,edges,priority:effectiveModelPriority(0),budget:currentBudgetPriority()});if(mode==='own-write')valid=false;committed=true;return {status:'saved',memory:{id:'saved'}}}} as any)
 const signal=new AbortController();if(mode==='cancelled')signal.abort()
 const context:any={scene:{id:'s'},mode:mode==='simulation'?'simulation':'live',signal:signal.signal,sourceSnapshots:mode.startsWith('preview-')?undefined:mode==='missing-source'?[]:[message]}
 if(mode==='preview-ref-registered-error')message.sourceEventRef={eventId:'event',revision:1,deletionEpoch:0}
 if(mode==='registration-limited')message.sourceEventRegistrationLimited=true
 const input:any={sourceMessageIds:['a'],kind:'fact',content:'Synthetic durable fact'}
 const parents:any[]=mode==='parent-invalid'||mode==='immutable'?[{id:'parent',version:1,deletionEpoch:0,deleted:false}]:[]
 const entities:any[]=mode.startsWith('entity-')?[{entityId:'entity',backingRef:{id:'parent',version:1,deletionEpoch:0}}]:[]
 const entityValid=mode==='entity-invalid'?()=>false:undefined
 const read=mode==='read-invalid'||mode==='own-write'?{identity:'read',valid:()=>mode==='read-invalid'?false:valid}:undefined
 const result=p.submit(context,input,parents,entities,entityValid,[],mode==='commitment-freeform'?()=>true:undefined,read,[],mode==='knowledge-freeform'?()=>true:undefined)
 const key=(p.queue as any).pending[0]?.job.key??null
 if(mode==='immutable'){input.content='MUTATED';parents[0].version=999;message.parts[0].text='MUTATED'}
 if(mode==='source-changed')valid=false
 busy=false;p.queue.resume();await p.queue.whenIdle();proposals.push({mode,result,key,writes,stats:p.queue.stats()});p.queue.dispose()
}
const paths=['src/memory/learning/contextual-queue.ts','src/memory/learning/contextual-proposals.ts','src/memory/learning/contracts.ts','src/runtime/source-events.ts','src/engine.ts']
const sources=Object.fromEntries(paths.map(p=>[p,createHash('sha256').update(readFileSync(p)).digest('hex')]))
const output=JSON.stringify({sources,queues,proposals},null,2)+'\n',path='backend/internal/contextuallearning/testdata/source.json'
if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==output)throw Error('contextual source fixture changed')}else writeFileSync(path,output)
