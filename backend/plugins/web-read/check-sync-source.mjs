// Source policy callbacks are synchronous. A due AbortSignal timer cannot run
// inside one; these fixtures exercise that ordering, not production slow getters.
import { WebReadService } from '../../../src/plugins/builtin/web-read.ts'
import { Readable } from 'node:stream'
import { readFileSync } from 'node:fs'
import { deepStrictEqual } from 'node:assert/strict'
const block=()=>Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,600)
const config={timeoutMs:500,maxChars:4000,maxConcurrency:1}
const signal=new AbortController().signal
const error=new Error();Object.defineProperty(error,'message',{get(){block();return 'synthetic_error'}})
const fail=new WebReadService(config,undefined,{lookup:async()=>[{address:'8.8.8.8',family:4}],request(){throw error}})
const errorResult=await fail.request({url:'http://fixture.invalid/'},signal)
const transport={lookup:async()=>[{address:'8.8.8.8',family:4}],request(options,callback){const response=Readable.from(['x']);Object.assign(response,{statusCode:200,headers:{'content-type':'text/plain'},rawHeaders:['Content-Type','text/plain']});const req=new Readable({read(){}});req.end=()=>{queueMicrotask(()=>callback(response));return req};return req}}
const reader=new WebReadService(config,undefined,transport)
const body={contentType:'text/plain',encoding:'utf8',truncated:false};Object.defineProperty(body,'content',{get(){block();return 'source text'}})
const readResult=await reader.read({url:'http://fixture.invalid/'},signal,async()=>body)
const output=[{operation:'error',status:errorResult.status,activeAfterOutcome:fail.active},{operation:'response',status:readResult.status,text:readResult.text,activeAfterOutcome:reader.active}]
await fail.dispose();await reader.dispose()
if(process.argv[2]==='--print')console.log(JSON.stringify(output,null,2))
else{deepStrictEqual(output,JSON.parse(readFileSync(new URL('./core/testdata/node-sync-policy.json',import.meta.url),'utf8')));console.log('PASS source synchronous policy does not execute timer callback mid-call')}
