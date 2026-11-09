import {WebSocketServer} from 'ws'
import {once} from 'node:events'
import {readFileSync,writeFileSync} from 'node:fs'
import {OneBotAdapter} from '../../../../../src/plugins/builtin/snowluma/transport.ts'
const sockets=new Set()
let closeFrame,physicalClose
const frameSeen=new Promise(r=>closeFrame=r),closed=new Promise(r=>physicalClose=r)
const server=new WebSocketServer({host:'127.0.0.1',port:0})
await once(server,'listening')
server.on('connection',socket=>{
 sockets.add(socket)
 socket.on('close',()=>{sockets.delete(socket);physicalClose();if(process.argv.includes('--peer'))console.log(JSON.stringify({event:'physical-close'}))})
 // Decode a real masked close frame, but deliberately never echo it.
 socket._receiver.removeAllListeners('conclude')
 socket._receiver.on('conclude',(code,reason)=>{const frame={code,reason:reason.toString()};closeFrame(frame);if(process.argv.includes('--peer'))console.log(JSON.stringify({event:'close-frame',...frame}))})
 socket.on('message',raw=>{const f=JSON.parse(raw.toString());if(f.action==='get_login_info')socket.send(JSON.stringify({echo:f.echo,status:'ok',retcode:0,data:{user_id:42}}));else if(f.action==='invalid-utf8')socket.send(Buffer.from([255]),{binary:false})})
})
const url=`ws://127.0.0.1:${server.address().port}`
if(process.argv.includes('--peer')){
 console.log(JSON.stringify({url}));process.stdin.resume();process.stdin.on('end',()=>{for(const s of sockets)s.terminate();server.close()})
}else{
 const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url,timeoutMs:1000,reconnectMs:100},{})
 try{
  await adapter.connect()
  const request=adapter.action('invalid-utf8',{}).then(()=>({unexpectedSuccess:true}),e=>({error:e.code,transmitted:e.transmitted}))
  const frame=await frameSeen
  const configuredCloseTimeout=adapter.socket._closeTimeout
  const openBeforeDispose=sockets.size===1
  adapter.dispose()
  const action=await request
  await closed
  const data={frame,configuredCloseTimeout,openBeforeDispose,closedAfterDispose:sockets.size===0,action}
  const output=JSON.stringify(data,null,2)+'\n',target=new URL('./testdata/ws-close-source.json',import.meta.url)
  if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==output)throw Error('close source changed')}else writeFileSync(target,output)
 }finally{adapter.dispose();for(const s of sockets)s.terminate();await new Promise(r=>server.close(r))}
}
