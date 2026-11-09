// Real TypeScript adapter + local synthetic peers. No action monkey patch.
import {createServer} from 'node:http'
import {once} from 'node:events'
import {mkdtempSync,writeFileSync,readFileSync,rmSync,mkdirSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import {createHash} from 'node:crypto'
import {WebSocketServer} from 'ws'
import {OneBotAdapter} from '../../../../../src/plugins/builtin/snowluma/transport.ts'
const root=mkdtempSync(join(tmpdir(),'snow-send-peer-'))
const file=join(root,'image.bin'),bytes=Buffer.from([0,1,2,253,254,255]);writeFileSync(file,bytes)
const rows=[]
try{
for(const mode of ['http','ws'])for(const scenario of ['sent','fallback','resolver-throws','missing-receipt','reject','source-revoked','missing-validator','missing-validator-bad-part','cancel-before','cancel-after-transmit']){
 const trace=[],requests=[];const controller=new AbortController()
 const response=()=>scenario==='reject'?{status:'failed',retcode:1404,data:null}:{status:'ok',retcode:0,data:scenario==='missing-receipt'?{}:{message_id:11}}
 const handler=(action,params,reply)=>{
  if(action==='get_login_info'){reply({status:'ok',retcode:0,data:{user_id:42}});return}
  requests.push(params)
  if(scenario==='cancel-after-transmit'){controller.abort();return}
  reply(response())
 }
 const server=createServer(async(req,res)=>{
  const chunks=[];for await(const c of req)chunks.push(c)
  handler(req.url.slice(1),JSON.parse(Buffer.concat(chunks)),reply=>{res.setHeader('content-type','application/json');res.end(JSON.stringify(reply))})
 })
 const sockets=new WebSocketServer({server})
 sockets.on('connection',socket=>socket.on('message',raw=>{const request=JSON.parse(raw.toString());handler(request.action,request.params,reply=>socket.send(JSON.stringify({...reply,echo:request.echo})))}))
 server.listen(0,'127.0.0.1');await once(server,'listening')
 const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',transport:mode==='http'?'http':'ws',url:`${mode==='http'?'http':'ws'}://127.0.0.1:${server.address().port}`,token:'synthetic',eventToken:'synthetic',eventPort:0,timeoutMs:1000},{
  resolveMediaSource:()=>{trace.push('source');if(scenario==='fallback')return;if(scenario==='resolver-throws')throw Error('synthetic_resolver');return {type:'image',path:file,bytes:bytes.length,hash:createHash('sha256').update(bytes).digest('hex'),validate:scenario.startsWith('missing-validator')?undefined:()=>{trace.push('validate');if(scenario==='source-revoked')throw Error('synthetic_revoked')}}},
  resolveMedia:()=>{trace.push('media');return {type:'image',file:'https://example.invalid/synthetic.png'}}
 })
 try{
  await adapter.connect()
  if(scenario==='cancel-before')controller.abort()
  const scene=adapter.scene('group','99')
  const parts=[{type:'text',text:'hello'},{type:'media',assetId:'managed'},{type:'text',text:'tail'}];if(scenario==='missing-validator-bad-part')parts.push({type:'unsupported'})
  const receipt=await adapter.send(scene,{parts},controller.signal,()=>trace.push('before'))
  rows.push({mode,scenario,trace,requests,receipt})
 }finally{
  adapter.dispose()
  for(const client of sockets.clients)client.terminate()
  await new Promise(resolve=>sockets.close(resolve))
  server.closeAllConnections();await new Promise(resolve=>server.close(resolve))
 }
}
}finally{rmSync(root,{recursive:true,force:true})}
const target=new URL('./testdata/send-peers-source.json',import.meta.url)
mkdirSync(new URL('./testdata/',import.meta.url),{recursive:true})
const text=JSON.stringify(rows,null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('send peer source changed')}else writeFileSync(target,text)
