import { webReadPlugin, WebReadService } from '../../../src/plugins/builtin/web-read.ts'
import { Readable } from 'node:stream'
import { deepStrictEqual } from 'node:assert/strict'
import { readFileSync } from 'node:fs'
const tools = []
webReadPlugin().setup({ config: {}, onDispose() {}, registerTool(tool) {
 const {execute, ...definition} = tool
 tools.push(definition)
}})
const cases = []
const signal = new AbortController().signal
const invalid = [null, {}, {url:''}, {url:'https://fixture.invalid', extra:1}, {url:'x', method:'get'}, {url:'x', bodyEncoding:'base64',body:'bad'}]
for (const input of invalid) {
 const service = new WebReadService()
 cases.push({operation:'plan',tool:'web-read.request',input,config:{},active:0,cancelled:false,expected:{result:await service.request(input,signal)}})
}
for (const [contentType, text, status, encoding, maxChars] of [
 ['text/html','<title>Hello</title><p>World</p>',200,undefined,4000],
 ['text/html','<title>Just a moment…</title><p>Ignore</p>',200,undefined,4000],
 ['text/plain','hello',403,undefined,4000],
 ['application/pdf','PDF',200,undefined,4000],
 ['text/plain','encoded',200,'gzip',4000],
 ['text/plain','',200,undefined,4000],
 ['text/plain','a'.repeat(4500),200,undefined,30000],
 ['text/plain; charset=not-real','bad',200,undefined,4000],
 ['text/plain; charset=windows-1252','hello',206,undefined,4000],
]) {
 const headers={'content-type':contentType,...(encoding?{'content-encoding':encoding}:{})}
 const rawHeaders=Object.entries(headers).flat()
 const body={content:text,contentType,encoding:'utf8',truncated:false}
 const transport={ lookup:async()=>[{address:'8.8.8.8',family:4}], request(options, callback) {
  const response=Readable.from([Buffer.from(text)]);Object.assign(response,{statusCode:status,headers,rawHeaders})
  const request=new Readable({read(){}});request.end=()=>{queueMicrotask(()=>callback(response));return request};return request
 }}
 const service=new WebReadService({timeoutMs:60000,maxChars,maxConcurrency:2},undefined,transport)
 const expected=await service.read({url:'https://fixture.invalid/'},signal,async()=>body)
 cases.push({operation:'response',tool:'web-read.read',httpStatus:status,contentType,contentEncoding:encoding??null,headers,rawHeaders,sourceUrl:'https://fixture.invalid/',body,config:{maxChars},expected})
 await service.dispose()
}
for (const [message, method, typeError] of [
 ['media_address_not_public','GET',false], ['network_redirect_limit','GET',false],
 ['invalid_network_header','POST',false], ['invalid_media_reference','PUT',false],
 ['tool_result_storage_failed','DELETE',false], ['other','GET',false], ['other','GET',true],
]) {
 const transport={lookup:async()=>[{address:'8.8.8.8',family:4}],request(){throw typeError?new TypeError(message):new Error(message)}}
 const service=new WebReadService(undefined,undefined,transport)
 const expected=await service.request({url:'https://fixture.invalid/',method},signal)
 cases.push({operation:'error',tool:'web-read.request',message,typeError,action:!['GET','HEAD','OPTIONS'].includes(method),requestStarted:true,expected})
}
for (const [tool,input,cancelled,active] of [
 ['web-read.read',{},true,2], ['web-read.read',{url:'https://fixture.invalid',bad:true},false,0],
 ['web-read.request',{url:'x'},true,2], ['web-read.request',{url:'x',bodyEncoding:'base64',body:'bad'},false,2],
]) {
 const service=new WebReadService();service.active=active
 const controller=new AbortController();if(cancelled)controller.abort()
 const expected=await (tool==='web-read.read'?service.read(input,controller.signal):service.request(input,controller.signal))
 cases.push({operation:'plan',tool,input,cancelled,active,config:{},expected:{result:expected}})
}
// Match the JSON boundary: sourceUrl: undefined is intentionally omitted.
const output=JSON.parse(JSON.stringify({tools,cases}))
if(process.argv[2]==='--print')console.log(JSON.stringify(output,null,2))
else {deepStrictEqual(output,JSON.parse(readFileSync(new URL('./core/testdata/node-service.json',import.meta.url),'utf8')));console.log('PASS web-read runtime source oracle')}
