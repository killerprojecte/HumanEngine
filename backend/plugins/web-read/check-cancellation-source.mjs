// Actual source timers, not a reimplementation or shortened mock of DNS policy.
import { requestPublicResource } from '../../../src/network-request.ts'
import { WebReadService } from '../../../src/plugins/builtin/web-read.ts'
import { readFileSync } from 'node:fs'
import { deepStrictEqual } from 'node:assert/strict'
async function run(fail) {
 let callbacks=0, requestCalls=0
 const releases=[], physical=[]
 const transport={lookup(){let release;const promise=new Promise((resolve,reject)=>{release=()=>{callbacks++;if(fail)reject(new Error('synthetic late DNS failure'));else resolve([{address:'8.8.8.8',family:4}])}});releases.push(release);physical.push(promise);return promise},request(){requestCalls++;throw new Error('no socket expected')}}
 const service=new WebReadService({timeoutMs:60000,maxConcurrency:1,maxChars:4000},undefined,transport)
 const signal=new AbortController().signal
 const [network,first]=await Promise.all([
  requestPublicResource('http://fixture.invalid/',{},transport).then(()=>({unexpected:true}),e=>({error:e.message})),
  service.request({url:'http://fixture.invalid/'},signal),
 ])
 const activeAfterOutcome=service.active
 const second=await service.request({url:'x'},signal)
 const callbacksBeforeRelease=callbacks
 releases.forEach(release=>release());await Promise.allSettled(physical)
 await service.dispose()
 return {name:fail?'late DNS failure':'late DNS success',networkError:network.error,serviceStatus:first.status,activeAfterOutcome,secondStatus:second.status,callbacksBeforeRelease,callbacksAfterRelease:callbacks,requestCalls}
}
const result=await Promise.all([run(false),run(true)])
if(process.argv[2]==='--print')console.log(JSON.stringify(result,null,2))
else{deepStrictEqual(result,JSON.parse(readFileSync(new URL('./core/testdata/node-cancellation.json',import.meta.url),'utf8')));console.log('PASS actual source independent DNS deadline, late callbacks and active-slot release')}
