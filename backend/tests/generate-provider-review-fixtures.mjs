// Actual source regression oracles for review corrections. Synthetic loopback only.
import {createServer} from 'node:http'
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {resolve} from 'node:path'
import {pathToFileURL} from 'node:url'
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT),out=resolve(process.env.HUMANENGINE_FIXTURE_OUTPUT)
const {Router}=await import(pathToFileURL(root+'/src/models.ts'))
const {ModelProtocolRegistry}=await import(pathToFileURL(root+'/src/model-protocols.ts'))
const tools=[{name:'lookup',description:'Synthetic',inputSchema:{type:'object',properties:{z:{type:'number'},a:{type:'number'}}}}]
const usage={inputTokens:1,outputTokens:1,totalTokens:2}
const config=provider=>({providers:[provider],models:[{id:'m',providerId:'p',apiModelName:'synthetic',capabilities:['text','tools'],streaming:false}],routes:[{role:'replyer',orderedModelIds:['m']}]})
const roundtrip=[]
for(const protocol of ['openai-chat','openai-responses']){
 let count=0;const requests=[]
 const response=protocol==='openai-chat'?{choices:[{finish_reason:'tool_calls',message:{content:'',tool_calls:[{id:'c',type:'function',function:{name:'lookup',arguments:'{"z":1,"a":2}'}}]}}]}:{status:'completed',output:[{type:'function_call',call_id:'c',name:'lookup',arguments:'{"z":1,"a":2}'}]}
 const server=createServer(async(req,res)=>{let raw='';for await(const chunk of req)raw+=chunk;requests.push(JSON.parse(raw));count++;res.setHeader('Content-Type','application/json');res.end(JSON.stringify(response))})
 await new Promise(r=>server.listen(0,'127.0.0.1',r));const provider={id:'p',protocol,baseUrl:`http://127.0.0.1:${server.address().port}`,retryCount:0}
 const router=new Router(config(provider));const first=await router.complete('replyer',[{role:'user',content:'synthetic'}],tools)
 // Native state is deliberately copied for Responses to exercise its builder,
 // rather than bypass serialization via the same-object continuation snapshot.
 const copied={...first.assistant};await router.complete('replyer',[copied],tools)
 roundtrip.push({protocol,response,assistantArgumentJSON:JSON.stringify(first.toolCalls[0].arguments),secondRequest:requests[1]});server.closeAllConnections();await new Promise(r=>server.close(r))
}
const custom=[]
for(const [name,call] of [['ordered',{id:'c',name:'lookup',arguments:{z:1,a:2}}],['missing',{id:'c',name:'lookup'}],['null',{id:'c',name:'lookup',arguments:null}],['unknown-missing',{id:'c',name:'unknown'}],['empty-id-missing',{id:'',name:'lookup'}],['array',{id:'c',name:'lookup',arguments:[]}]]){
 const registry=new ModelProtocolRegistry();const controller=new AbortController();registry.register('fixture:custom','Synthetic',{async complete(){return{text:'',toolCalls:[call],usage}}},controller.signal)
 const provider={id:'p',protocol:'fixture:custom',baseUrl:'https://synthetic.invalid',retryCount:0};const cfg=config(provider);const router=new Router(cfg,undefined,undefined,registry)
 const value={text:'',toolCalls:[call],usage};const row={name,raw:JSON.stringify(value)}
 try{const first=await router.completeProtocol(provider,cfg.models[0],'replyer',[{role:'user',content:'synthetic'}],tools,30000);row.argumentJSON=JSON.stringify(first.toolCalls[0].arguments)}catch(e){row.error=e.code||e.message}
 custom.push(row);controller.abort()
}
let customRoundtrip
{
 const registry=new ModelProtocolRegistry();const controller=new AbortController();let seen
 registry.register('fixture:custom','Synthetic',{async complete(context,messages){if(messages[0].role==='assistant')seen=JSON.stringify(messages[0].toolCalls[0].arguments);return{text:'',toolCalls:[{id:'c',name:'lookup',arguments:{z:1,a:2}}],usage}}},controller.signal)
 const provider={id:'p',protocol:'fixture:custom',baseUrl:'https://synthetic.invalid',retryCount:0};const router=new Router(config(provider),undefined,undefined,registry)
 const first=await router.complete('replyer',[{role:'user',content:'synthetic'}],tools);await router.complete('replyer',[first.assistant],tools)
 customRoundtrip={assistantArgumentJSON:JSON.stringify(first.toolCalls[0].arguments),secondArgumentJSON:seen};controller.abort()
}
const urls=[]
for(const suffix of ['/a/../v1','/v1?','/v1#','/a/%2e%2e/v1','/v1?x=1','/v1#x']){
 let path;const server=createServer((req,res)=>{path=req.url;res.setHeader('Content-Type','application/json');res.end('{"data":[]}')});await new Promise(r=>server.listen(0,'127.0.0.1',r));const provider={id:'p',protocol:'openai-chat',baseUrl:`http://127.0.0.1:${server.address().port}${suffix}`};const router=new Router(config(provider));const row={suffix};try{await router.request(provider,'models',undefined,30000);row.path=path}catch(e){row.error=e.code||e.message};urls.push(row);server.closeAllConnections();await new Promise(r=>server.close(r))
}
const sources=Object.fromEntries(['src/models.ts','src/model-protocols.ts'].map(p=>[p,createHash('sha256').update(readFileSync(root+'/'+p)).digest('hex')]))
writeFileSync(out,JSON.stringify({sources,roundtrip,custom,customRoundtrip,urls},null,2)+'\n');console.log(JSON.stringify({roundtrip:roundtrip.length,custom:custom.length,urls:urls.length}))
