import assert from 'node:assert/strict'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {Runtime} from '../../src/runtime.ts'
import {Store} from '../../src/store.ts'
import {installAdapter} from '../../src/plugins/adapters.ts'

// Execute actual source methods with isolated collaborators, no provider or IO.
const runtime=()=>Object.assign(Object.create(Runtime.prototype),{
 adapters:new Map(),scenes:new Map(),botUserIds:new Map(),
 imageBytes:{clear(){}},store:{settings:{topicAdmissionEnabled:false}},
})
const cases=[]
{
 const r=runtime(), reads=[]
 const adapter=(name,status)=>({get status(){reads.push(name);return status},dispose(){}})
 r.addAdapter('a',adapter('old-a','connected'))
 r.addAdapter('b',adapter('old-b','connected'))
 r.addAdapter('a',adapter('a','connecting'))
 r.addAdapter('c',adapter('c','invalid'))
 r.removeAdapter('b');r.addAdapter('b',adapter('b','disposed'))
 const result=r.adapterStatuses
 assert.deepEqual(reads,['a','a','c','b','b'])
 cases.push({name:'ordered-replace-delete-readd',result,reads})
}
{
 const r=runtime();let reads=0
 r.addAdapter('a',{get status(){
  reads++;r.removeAdapter('b');r.addAdapter('b',{status:'connected',dispose(){}})
  return reads===1?'connected':'changed-after-membership'
 },dispose(){}})
 r.addAdapter('b',{status:'disconnected',dispose(){}})
 const result=r.adapterStatuses
 assert.deepEqual(result,[{id:'a',status:'changed-after-membership'},{id:'b',status:'disconnected'}])
 cases.push({name:'snapshot-mutation-second-read',result,reads})
}
{
 const r=runtime();let environment, factoryReads=0,disposed=0
 const installation=installAdapter('v1',env=>{
  environment=env
  return {get status(){factoryReads++;return 'connected'},dispose(){disposed++}}
 },r,{},new AbortController().signal,{})
 const before=r.adapterStatuses
 environment.status('connected')
 const connected=r.adapterStatuses
 environment.status('disposed')
 const invalidIgnored=r.adapterStatuses
 const retained=r.adapters.get('v1')
 installation.dispose()
 const afterDispose=retained.status
 environment.status('connecting')
 assert.equal(retained.status,'disconnected')
 assert.equal(factoryReads,0)
 cases.push({name:'v1-wrapper-owns-status',before,connected,invalidIgnored,afterDispose,factoryReads,disposed})
}
const makeStore=()=>Object.assign(Object.create(Store.prototype),{
 logs:[],logBytes:[],retainedLogBytes:0,oversizedLogs:0,evictedLogs:0,
})
const retention=[]
for(const [name,entries] of [
 ['record-limit',Array.from({length:501},(_,i)=>({level:i===0?'error':'info',bytes:100}))],
 ['byte-limit',[{level:'error',bytes:3*1024*1024},{level:'info',bytes:2*1024*1024}]],
 ['exact-limit',[{level:'error',bytes:4*1024*1024}]],
 ['oversized',[{level:'error',bytes:4*1024*1024+1}]],
]){
 const store=makeStore()
 entries.forEach((e,id)=>store.retainLog({id:String(id),level:e.level},e.bytes))
 retention.push({name,entries,result:{ids:store.logs.map(x=>x.id),bytes:store.retainedLogBytes,oversized:store.oversizedLogs,evicted:store.evictedLogs,recentErrors:store.logs.filter(x=>x.level==='error').length}})
}
const notices=[]
{
 const store=Object.assign(makeStore(),{loggingClosed:false,closedLogRejections:0,unserializableLogs:0,loggingNoticeAt:-Infinity,loggingInitializationFailure:false,stdoutLogs:{status:{state:'active',rejectedRecords:0,failedRecords:0},enqueue(){}}})
 let now=100000
 const realNow=Date.now
 Date.now=()=>now
 const snapshot=name=>notices.push({name,records:store.logs.length,recentErrors:store.logs.filter(x=>x.level==='error').length,warnings:store.logs.filter(x=>x.code==='log_delivery_degraded').length,closed:store.closedLogRejections,unserializable:store.unserializableLogs,oversized:store.oversizedLogs})
 try{
  store.log('error','synthetic','before');snapshot('ordinary-error')
  store.loggingClosed=true;store.log('error','synthetic','after');snapshot('closed-retained')
  store.log('error','synthetic','bad',{unsupported:1n});snapshot('unserializable-no-row-throttled')
  now+=60000;store.log('error','synthetic','oversized',{payload:'x'.repeat(4*1024*1024)});snapshot('oversized-no-row-notice-after-minute')
 }finally{Date.now=realNow}
}
const sourceHashes=Object.fromEntries(['src/runtime.ts','src/store.ts','src/plugins/adapters.ts','backend/internal/plugins/v1-adapters.mjs','backend/internal/plugins/adapter.go'].map(p=>[p,createHash('sha256').update(readFileSync(new URL('../../'+p,import.meta.url))).digest('hex')]))
const output=JSON.stringify({sourceHashes,cases,retention,notices},null,2)+'\n'
const file=new URL('../internal/botruntime/testdata/status-source.json',import.meta.url)
if(process.argv.includes('--check'))assert.equal(readFileSync(file,'utf8'),output);else writeFileSync(file,output)
console.log('PASS actual Runtime getter/order/mutation, V1 wrapper lifecycle, Store retention boundaries')
