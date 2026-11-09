// Actual source OneBot HTTP transport, actual loopback HTTP actions/events.
// No replacement manager, transport or decoder is injected.
import { createServer } from 'node:http'
import { createHmac } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { OneBotHttpTransport } from '../../../../../src/plugins/builtin/snowluma/http.ts'
import { OutboundEncoder } from '../../../../../src/plugins/builtin/snowluma/outbound.ts'
const responseCases=[
 {name:'ok',raw:'{"status":"ok","retcode":0,"data":{"value":7}}'},
 {name:'unsupported',raw:'{"status":"failed","retcode":1404}'},
 {name:'parameters',raw:'{"status":"failed","retcode":1400}'},
 {name:'platform',raw:'{"status":"failed","retcode":12}'},
 {name:'invalid-status',raw:'{"status":"other","retcode":12}'},
 {name:'invalid-code',raw:'{"status":"failed","retcode":0}'},
 {name:'missing-code',raw:'{"status":"ok"}'},
 {name:'array',raw:'[]'},
 {name:'invalid-json',raw:'{'},
]
const requests=[]
const server=createServer(async(req,res)=>{
 let body='';for await(const chunk of req)body+=chunk
 requests.push({method:req.method,authorization:req.headers.authorization,body})
 res.setHeader('Content-Type','application/json')
 res.end(req.url==='/get_login_info'?'{"status":"ok","retcode":0,"data":{"user_id":42}}':responseCases.find(x=>'/'+x.name===req.url)?.raw??'{}')
})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
const events=[],statuses=[]
const config={id:'snowluma',accountId:'42',transport:'http',url:'http://127.0.0.1:'+server.address().port,token:'synthetic-action-token',eventToken:'synthetic-event-token',eventHost:'127.0.0.1',eventPort:0,eventPath:'/onebot/events',timeoutMs:15000}
const transport=new OneBotHttpTransport(config,raw=>events.push(raw),status=>statuses.push(status),()=>{},new OutboundEncoder())
try{
 await transport.connect()
 const actions=[]
 for(const item of responseCases){
  try{actions.push({...item,value:await transport.action(item.name,{})})}
  catch(error){actions.push({...item,error:error.code,transmitted:error.transmitted})}
 }
 const eventCases=[
  {name:'valid',raw:'{"self_id":42,"post_type":"meta_event"}',status:204},
  {name:'other-account',raw:'{"self_id":43}',status:403},
  {name:'invalid-account',raw:'{"self_id":{}}',status:400},
  {name:'array',raw:'[]',status:400},
  {name:'invalid-json',raw:'{',status:400},
  {name:'bad-signature',raw:'{"self_id":42}',badSignature:true,status:401},
  {name:'other-header-account',raw:'{"self_id":42}',headerAccount:'43',status:401},
 ]
 const eventURL='http://127.0.0.1:'+transport.eventServer.address().port+'/onebot/events'
 for(const item of eventCases){
  const signature=item.badSignature?'0'.repeat(40):createHmac('sha1',config.eventToken).update(item.raw).digest('hex')
  const response=await fetch(eventURL,{method:'POST',headers:{'Content-Type':'application/json','X-Signature':'sha1='+signature,'X-Self-Id':item.headerAccount??'42'},body:item.raw})
  await response.arrayBuffer()
  if(response.status!==item.status)throw Error(item.name+': '+response.status)
 }
 const output={actions,eventCases,events,requestAuthentication:requests.every(x=>x.method==='POST'&&x.authorization==='Bearer synthetic-action-token'&&x.body==='{}'),statuses}
 const target=fileURLToPath(new URL('./testdata/http-source.json',import.meta.url))
 const encoded=JSON.stringify(output,null,2)+'\n'
 if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==encoded)throw Error('source HTTP fixtures changed')}else writeFileSync(target,encoded)
}finally{transport.dispose();await new Promise(resolve=>server.close(resolve));server.closeAllConnections()}
