import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {Store} from '../../src/store.ts'
import {withFlowContext} from '../../src/diagnostics/flow.ts'
const root=new URL('../../',import.meta.url)
for(const name of ['zh-CN','presentation-zh-CN','errors-zh-CN']){
 assert.deepEqual(readFileSync(new URL('src/diagnostics/catalogs/'+name+'.json',root)),readFileSync(new URL('backend/internal/observability/retained_catalog/'+name+'.json',root)))
}
const id='11111111-1111-4111-8111-111111111111',at='2026-11-09T00:00:00.123Z'
const sorted=v=>Array.isArray(v)?v.map(sorted):v&&typeof v==='object'?Object.fromEntries(Object.keys(v).sort().map(k=>[k,sorted(v[k])])):v
const makeStore=()=>Object.assign(Object.create(Store.prototype),{
 logs:[],logBytes:[],retainedLogBytes:0,oversizedLogs:0,evictedLogs:0,
 closedLogRejections:0,unserializableLogs:0,loggingNoticeAt:-Infinity,
 loggingClosed:false,loggingInitializationFailure:false,
 stdoutLogs:{status:{state:'active',rejectedRecords:0,failedRecords:0},enqueue(){}},
})
const specs=[
 {name:'plain'},
 {name:'inherited-catalog-labels',fields:{role:'__proto__',stage:'constructor',status:'toString',reason:'hasOwnProperty',outputKind:'__proto__',nextStep:'valueOf'}},
 {name:'inherited-error-key',fields:{errorCode:'constructor',adviceCode:'__proto__'}},

 {name:'multibyte-and-html',fields:{payload:'中文🙂<&>\u2028\u2029',label:'café'}},
 {name:'source-message-overwrites',message:'caller-sink-only',fields:{message:'ignored context message'}},
 {name:'context-overwrites-base',traceId:'flow-trace',stage:'flow-stage',fields:{id:'override',at:'override-at',level:'error',module:'override-module',code:'override-code',traceId:'context-trace',stage:'dispatch',durationMs:0}},
 {name:'all-context',requestId:'r',traceId:'t',sceneId:'s',turnId:'u',stage:'prepare',durationMs:2.5,fields:{role:'thinker',modelId:'m',apiModel:'api',providerId:'p',toolName:'tool',status:'completed',reason:'unmapped_reason',counts:{sentSegments:2,'10':10,'2':2,a:1},mode:'simulation',outputKind:'text',nextStep:'unmapped_next'}},
 {name:'failure-without-cause',level:'error'},
 {name:'failed-status',fields:{status:'failed'}},
 {name:'root-cause',fields:{rootCause:{errorType:'TypeError',errorCode:'provider_http_429',errorMessage:'synthetic detail',httpStatus:429,systemCode:'ELOCAL',adviceCode:'unknown_advice',errorContext:{attempt:2,maxAttempts:3,stageBudgetMs:80,timeoutMs:0,remainingCandidates:0,retryable:false}},errorContext:{operation:'request',stage:'model',role:'replyer',modelId:'flow-model',providerId:'flow-provider'},apiModel:'api'}},
 {name:'root-overrides-context',fields:{errorCode:'outer',errorContext:{operation:'outer',stage:'outer',role:'vision'},rootCause:{errorCode:'provider_http_503',errorContext:{operation:'inner',stage:null,role:'embedding',apiModel:'inner-model',retryable:true}}}},
 {name:'technical-is-code',fields:{errorCode:'provider_http_401',errorMessage:'provider_http_401'}},
 {name:'technical-is-summary',fields:{errorCode:'unknown',errorMessage:'synthetic'}},
 {name:'reason-http',fields:{reason:'provider_http_502',systemCode:'ELOCAL',httpStatus:0}},
 {name:'known-labels',fields:{stage:'reply',status:'sent',reason:'no_action',role:'planner',nextStep:'done',counts:{inputTokens:10,outputTokens:4}}},
 {name:'null-presence',fields:{durationMs:null,httpStatus:null,rootCause:{httpStatus:null,errorContext:{attempt:null,maxAttempts:null,stageBudgetMs:null,timeoutMs:null,remainingCandidates:null,retryable:null}}}},
 {name:'explicit-false-root',level:'error',fields:{rootCause:false,systemCode:'ELOCAL',httpStatus:204}},
 {name:'empty-api-nullish',fields:{apiModel:'',modelId:'m',role:'custom',counts:{number:-0,nullable:null,bool:true}}},
 {name:'null-api-nullish',fields:{apiModel:null,modelId:'m',reason:'provider_http_418'}},
 {name:'integer-key-order',fields:{counts:{'4294967295':4,'01':1,'2':2,'0':0,'4294967294':3}}},
 {name:'nested-json',fields:{metadata:{arr:[null,false,1e21,'\ud800'],html:'<tag>'}}},
]
const conversions=[
 ['undefined',undefined],['null',null],['empty',''],['text','中文🙂'],
 ['true',true],['false',false],['zero',0],['negative-zero',-0],['finite',1.25],
 ['nan',NaN],['positive-infinity',Infinity],['negative-infinity',-Infinity],
 ['object',{}],['array',[null,undefined,'x',false]],['native-function',Object],
].map(([name,value])=>({name,truthy:Boolean(value),text:String(value)}))
const cases=[]
for(const {name,...spec} of specs){
 const event={id,at,level:'info',module:'synthetic',code:'ordinary',...spec,fields:sorted(spec.fields??{})}
 const context={id:event.id,at:event.at}
 for(const key of ['requestId','traceId','sceneId','turnId','stage'])if(event[key])context[key]=event[key]
 if(event.durationMs)context.durationMs=event.durationMs
 Object.assign(context,event.fields)
 const store=makeStore()
 // Exercise actual flow override as well as Store.log context spreading.
 const row=withFlowContext({traceId:'ambient-flow',stage:'ambient-stage'},()=>store.log(event.level,event.module,event.code,{traceId:undefined,stage:undefined,...context}))
 // Native Event has no implicit thread-local flow beyond its correlation fields.
 if(row.traceId===undefined)delete row.traceId
 if(row.stage===undefined)delete row.stage
 const text=JSON.stringify(row)+'\n'
 cases.push({name,event,text,bytes:Buffer.byteLength(text),level:row.level})
}
const interactions=[]
{
 const store=makeStore(),savedNow=Date.now
 let now=100000;Date.now=()=>now
 const errorContext={id,at,extra:'🙂'.repeat(240)}
 const checkpoint=name=>interactions.push({name,records:store.logs.length,bytes:store.retainedLogBytes,recentErrors:store.logs.filter(x=>x.level==='error').length,evicted:store.evictedLogs,oversized:store.oversizedLogs,notices:store.logs.filter(x=>x.code==='log_delivery_degraded').length})
 try{
  for(let i=0;i<500;i++)store.log('error','synthetic','ordinary',errorContext)
  checkpoint('500-real-errors')
  store.loggingClosed=true;store.log('info','synthetic','closed',{id,at})
  checkpoint('closed-row-and-notice-evict-two-errors')
  store.log('info','synthetic','oversized',{id,at,extra:'🙂'.repeat(1024*1024)})
  checkpoint('oversized-throttled-notice')
  now+=60000;store.log('info','synthetic','closed',{id,at})
  checkpoint('notice-after-minute')
 }finally{Date.now=savedNow}
}
const failureStore=makeStore()
let sinkEnqueues=0
failureStore.stdoutLogs.enqueue=()=>{sinkEnqueues++}
failureStore.fileLogs={status:{state:'ready',rejectedRecords:0,failedRecords:0},enqueue(){sinkEnqueues++}}
failureStore.log('error','synthetic','sink-failed',{id,at})
failureStore.stdoutLogs.status={state:'degraded',rejectedRecords:0,failedRecords:1}
failureStore.fileLogs.status={state:'degraded',rejectedRecords:0,failedRecords:1}
const sinkFailure={before:{records:failureStore.logs.length,bytes:failureStore.retainedLogBytes},sinkEnqueues}
failureStore.loggingStatus
sinkFailure.after={records:failureStore.logs.length,bytes:failureStore.retainedLogBytes,recentErrors:failureStore.logs.filter(x=>x.level==='error').length,noticeCounts:failureStore.logs.at(-1).counts}
sinkFailure.afterEnqueues=sinkEnqueues
const boundaries=[]
for(const [name,delta,follow] of [['exact-byte-limit',0,false],['one-byte-over',1,false],['exact-then-small-evicts',0,true]]){
 const event={id,at,level:'error',module:'synthetic',code:'boundary',fields:{extra:''}}
 const measure=makeStore();measure.log(event.level,event.module,event.code,{id,at,extra:''})
 const fillBytes=4*1024*1024-measure.retainedLogBytes+delta
 const store=makeStore();let enqueued=0;store.stdoutLogs.enqueue=()=>{enqueued++}
 store.log(event.level,event.module,event.code,{id,at,extra:'x'.repeat(fillBytes)})
 if(follow)store.log('info','synthetic','small',{id,at})
 boundaries.push({name,event,fillBytes,follow,result:{records:store.logs.length,bytes:store.retainedLogBytes,recentErrors:store.logs.filter(x=>x.level==='error').length,evicted:store.evictedLogs,oversized:store.oversizedLogs,notices:store.logs.filter(x=>x.code==='log_delivery_degraded').length,enqueued}})
}
// Interaction bytes include random UUID timestamps of notices; their encoded
// lengths are invariant, so no nondeterministic content enters the oracle.
const hashes=Object.fromEntries(['src/store.ts','src/diagnostics/format.ts','src/model-roles.ts',...['zh-CN','presentation-zh-CN','errors-zh-CN'].map(n=>'src/diagnostics/catalogs/'+n+'.json')].map(p=>[p,createHash('sha256').update(readFileSync(new URL(p,root))).digest('hex')]))
const output=JSON.stringify({hashes,cases,interactions,boundaries,sinkFailure,conversions},null,2)+'\n',file=new URL('../internal/observability/retained-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),output);else writeFileSync(file,output)
console.log('PASS '+cases.length+' actual Store.log/flow/readableLog rows and '+interactions.length+' retention interactions plus 3 exact encoded byte boundaries')
