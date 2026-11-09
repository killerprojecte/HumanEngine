import {WebSocketServer} from 'ws'
import {readFileSync,writeFileSync} from 'node:fs'
import {once} from 'node:events'
import {createHash} from 'node:crypto'
import {OneBotAdapter} from '../../../../../src/plugins/builtin/snowluma/transport.ts'
const requests=[],connections=[],closes=[],sockets=new Set()
const server=new WebSocketServer({host:'127.0.0.1',port:0,perMessageDeflate:process.argv.includes("--window9")?{clientMaxWindowBits:9}:true})
await once(server,'listening')
server.on('connection',(socket,request)=>{
 sockets.add(socket);let lastAction;socket.on('close',(code,reason)=>{closes.push({action:lastAction,code,reason:reason.toString()});sockets.delete(socket)})
 connections.push({authorization:request.headers.authorization,origin:request.headers.origin??null,compression:socket.extensions.includes('permessage-deflate')})
 socket.on('message',raw=>{
  const frame=JSON.parse(raw.toString());lastAction=frame.action;requests.push({action:frame.action,params:frame.action==='large-request'?{length:frame.params.text.length,sha256:createHash('sha256').update(frame.params.text).digest('hex')}:frame.params,echo:typeof frame.echo==='string'&&/^[0-9a-f-]{36}$/.test(frame.echo)})
  const send=(status,retcode,data)=>socket.send(JSON.stringify({echo:frame.echo,status,retcode,data}))
  switch(frame.action){
   case 'get_login_info':send('ok',0,{user_id:42});break
   case 'ok':send('ok',0,{answer:1});break
   case 'large-request':send('ok',0,{length:frame.params.text.length,sha256:createHash('sha256').update(frame.params.text).digest('hex'),compressed:socket._receiver._compressed});break
   case 'compressed':send('ok',0,{text:'repeated-context-'.repeat(1024)});break
   case 'failed':send('failed',1,{});break
   case 'unsupported':send('failed',1404,{});break
   case 'invalid':send('failed',1400,{});break
   case 'string-code':send('ok','0',{});break
   case 'hold':break
   case 'disconnect':socket.close();break
   case 'invalid-utf8':socket.send(Buffer.from([255]),{binary:false});break
   case 'over-limit':socket.send('x'.repeat(8*1024*1024+1));break
   case 'binary-event':socket.send(Buffer.concat([Buffer.from('{"self_id":42,"post_type":"message","message_type":"group","group_id":99,"user_id":7,"message_id":10,"time":1700000000,"message":[{"type":"text","data":{"text":"'),Buffer.from([255]),Buffer.from('"}}]}')]),{binary:true});send('ok',0,{});break
   case 'inspect':send('ok',0,{requests,connections,closes});break
   default:send('failed',1404,{})
  }
 })
})
const url=`ws://127.0.0.1:${server.address().port}`
if(process.argv.includes('--peer')){
 console.log(JSON.stringify({url}))
 process.stdin.resume();process.stdin.on('end',()=>{for(const socket of sockets)socket.terminate();server.close()})
}else{
 const statuses=[],errors=[],messages=[]
 const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url,token:'synthetic-token',timeoutMs:1000,reconnectMs:100},{onStatus:s=>statuses.push(s),onError:code=>errors.push(code),onMessage:m=>messages.push(m.parts)})
 const actions=[]
 try{
  await adapter.connect()
  for(const action of ['ok','large-request','failed','unsupported','invalid','string-code','compressed','compressed','hold']){
   try {const data=await adapter.action(action,action==='large-request'?{text:'0123456789abcdef'.repeat(4096)}:{});actions.push({action,value:action==='compressed'?{length:data.text.length}:data})}
   catch(error){actions.push({action,error:error.code,transmitted:error.transmitted})}
  }
  const abort=new AbortController();abort.abort()
  try{await adapter.action('ok',{},abort.signal)}catch(error){actions.push({action:'cancel-before',error:error.code,transmitted:error.transmitted})}
  await adapter.action('binary-event',{})
  try{await adapter.action('disconnect',{})}catch(error){actions.push({action:'disconnect',error:error.code,transmitted:error.transmitted})}
  const deadline=Date.now()+3000
  while(statuses.filter(s=>s==='connected').length<2){if(Date.now()>deadline)throw Error('reconnect did not finish');await new Promise(r=>setTimeout(r,5))}
  const inspected=await adapter.action('inspect',{})
  const mainStatuses=statuses.slice(),wireCases=[]
  for(const action of ['invalid-utf8','over-limit']){
   const before=statuses.filter(s=>s==='connected').length,errorStart=errors.length
   let outcome
   try{await adapter.action(action,action==='large-request'?{text:'0123456789abcdef'.repeat(4096)}:{})}catch(error){outcome={error:error.code,transmitted:error.transmitted}}
   const until=Date.now()+3000
   while(statuses.filter(s=>s==='connected').length===before){if(Date.now()>until)throw Error('wire rejection reconnect missing');await new Promise(r=>setTimeout(r,5))}
   const closed=(await adapter.action('inspect',{})).closes.findLast(c=>c.action===action)
   if(!closed)throw Error('physical close frame missing')
   wireCases.push({action,...outcome,errors:errors.slice(errorStart),closeCode:closed.code,closeReason:closed.reason})
  }
  const output=JSON.stringify({actions,statuses:mainStatuses,messages,connections:inspected.connections,requests:inspected.requests,wireCases},null,2)+'\n'
  const target=new URL(process.argv.includes('--window9')?'./testdata/ws-window9-source.json':'./testdata/ws-source.json',import.meta.url)
  if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==output)throw Error('WS source changed')}else writeFileSync(target,output)
 }finally{adapter.dispose();for(const socket of sockets)socket.terminate();await new Promise(r=>server.close(r))}
}
