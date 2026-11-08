// Rebuilt after environment replacement. Executes the actual Router.request over
// loopback HTTP with synthetic credentials and bodies; never contacts providers.
import {createServer} from 'node:http'
import {createHash} from 'node:crypto'
import {readFileSync,writeFileSync,mkdirSync} from 'node:fs'
import {resolve,dirname} from 'node:path'
import {pathToFileURL} from 'node:url'
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT||'.')
const {Router}=await import(pathToFileURL(resolve(root,'src/models.ts')))
const cases=[]
const scenarios=[
 {name:'json',path:'chat/completions',body:{model:'synthetic',stream:false},status:200,type:'application/json',response:'{"choices":[{"finish_reason":"stop","message":{"content":"synthetic"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}'},
 {name:'json-fallback',path:'chat/completions',body:{model:'synthetic',stream:true},status:200,type:'application/json',response:'{"choices":[{"finish_reason":"stop","message":{"content":"synthetic"}}]}'},
 {name:'list',path:'models',status:200,type:'application/json',response:'{"data":[{"id":"synthetic-model"}]}'},
 {name:'embedding',path:'embeddings',body:{model:'synthetic',input:['synthetic'],encoding_format:'float'},status:200,type:'application/json',response:'{"data":[{"index":0,"embedding":[0.5,0.25]}],"usage":{"prompt_tokens":2,"total_tokens":2}}'},
 {name:'invalid-json',path:'chat/completions',body:{model:'synthetic'},status:200,type:'application/json',response:'synthetic invalid'},
 {name:'wrong-content-type',path:'chat/completions',body:{model:'synthetic'},status:200,type:'text/plain',response:'synthetic text'},
 {name:'array-root',path:'chat/completions',body:{model:'synthetic'},status:200,type:'application/json',response:'[]'},
 {name:'http-429',path:'chat/completions',body:{model:'synthetic'},status:429,type:'application/json',response:'{"error":"synthetic private"}',retryAfter:'2'},
 {name:'http-503',path:'chat/completions',body:{model:'synthetic'},status:503,type:'application/json',response:'{"error":"synthetic private"}'},
 {name:'http-400',path:'chat/completions',body:{model:'synthetic'},status:400,type:'application/json',response:'{"error":"synthetic private"}'},
 {name:'stream-text',path:'chat/completions',body:{model:'synthetic',stream:true},status:200,type:'text/event-stream',response:'data: {"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}\n\ndata: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'},
 {name:'stream-partial',path:'chat/completions',body:{model:'synthetic',stream:true},status:200,type:'text/event-stream',response:'data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}\n\n'},
 {name:'stream-empty-partial',path:'chat/completions',body:{model:'synthetic',stream:true},status:200,type:'text/event-stream',response:'data: {"choices":[{"index":0,"delta":{"content":" "}}]}\n\n'},
 {name:'stream-json-error',path:'chat/completions',body:{model:'synthetic',stream:true},status:200,type:'text/event-stream',response:'data: not-json\n\n'},
 {name:'responses-stream',protocol:'openai-responses',path:'responses',body:{model:'synthetic',stream:true},status:200,type:'text/event-stream',response:'event: response.completed\ndata: {"type":"response.completed","response":{"id":"synthetic","status":"completed","output":[]}}\n\n'},
]
for(const scenario of scenarios){
 let received
 const server=createServer(async(req,res)=>{let body='';for await(const chunk of req)body+=chunk;received={method:req.method,path:req.url,accept:req.headers.accept,contentType:req.headers['content-type'],authorization:req.headers.authorization,...(body?{body:JSON.parse(body)}:{})};res.statusCode=scenario.status;res.setHeader('Content-Type',scenario.type);if(scenario.retryAfter)res.setHeader('Retry-After',scenario.retryAfter);res.end(scenario.response)})
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
 const baseUrl=`http://127.0.0.1:${server.address().port}`
 const provider={id:'synthetic-provider',protocol:scenario.protocol||'openai-chat',baseUrl,credentialConfigured:true}
 const router=new Router({providers:[provider],models:[],routes:[]},undefined,undefined,undefined,()=> 'synthetic-key')
 const row={...scenario}
 try{row.output=await router.request(provider,scenario.path,scenario.body,30000)}catch(e){row.error={code:e.code||e.message,retryable:e.retryable,...(e.status===undefined?{}:{status:e.status}),...(e.retryAfterMs===undefined?{}:{retryAfterMs:e.retryAfterMs})}}
 row.received=received;cases.push(row);server.closeAllConnections();await new Promise(resolve=>server.close(resolve))
}
const output=resolve(process.env.HUMANENGINE_FIXTURE_OUTPUT||'backend/internal/providertransport/testdata/source-http.json')
mkdirSync(dirname(output),{recursive:true})
writeFileSync(output,JSON.stringify({sources:Object.fromEntries(['src/models.ts','src/models/stream-response.ts'].map(p=>[p,createHash('sha256').update(readFileSync(resolve(root,p))).digest('hex')])),cases},null,2)+'\n')
console.log(`${cases.length} actual-source loopback HTTP cases -> ${output}`)
