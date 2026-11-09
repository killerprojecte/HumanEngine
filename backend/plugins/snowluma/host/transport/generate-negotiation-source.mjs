import http from 'node:http'
import {createHash} from 'node:crypto'
import {once} from 'node:events'
import {readFileSync,writeFileSync} from 'node:fs'
import {OneBotAdapter} from '../../../../../src/plugins/builtin/snowluma/transport.ts'
const extensions=[
 'permessage-deflate; client_max_window_bits=8',
 'permessage-deflate; client_max_window_bits=9',
 'permessage-deflate; client_max_window_bits=15',
 'permessage-deflate; client_max_window_bits=0x9',
 'permessage-deflate; client_max_window_bits=9.0',
 'permessage-deflate; client_max_window_bits=9e0',
 'permessage-deflate; client_max_window_bits=7',
 'permessage-deflate; client_max_window_bits=16',
 'permessage-deflate; client_max_window_bits=9.5',
 'permessage-deflate; client_max_window_bits=foo',
 'permessage-deflate; client_max_window_bits',
 'permessage-deflate; client_max_window_bits=9; client_max_window_bits=9',
 'permessage-deflate; server_max_window_bits=9',
 'permessage-deflate; server_max_window_bits=foo',
 'permessage-deflate; server_max_window_bits=16',
 'permessage-deflate; server_no_context_takeover; server_no_context_takeover',
 'permessage-deflate; unknown_parameter=9',
 'permessage-deflate; client_no_context_takeover=true',
]
const server=http.createServer(),sockets=new Set(),received=new Set()
server.on('upgrade',(request,socket)=>{
 const index=Number(request.url.slice(1));sockets.add(socket);socket.on('close',()=>sockets.delete(socket));socket.on('error',()=>{});socket.on('data',()=>received.add(index))
 const accept=createHash('sha1').update(request.headers['sec-websocket-key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64')
 socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\nSec-WebSocket-Extensions: ${extensions[index]}\r\n\r\n`)
})
server.listen(0,'127.0.0.1');await once(server,'listening')
const url=`ws://127.0.0.1:${server.address().port}`
if(process.argv.includes('--peer')){console.log(JSON.stringify({url}));process.stdin.resume();process.stdin.on('end',()=>{for(const s of sockets)s.destroy();server.close()})}
else{
 const output=[]
 try{
  for(let i=0;i<extensions.length;i++){
   const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url:`${url}/${i}`,timeoutMs:50})
   let code
   try{await adapter.connect()}catch(error){code=error.code}
   finally{adapter.dispose()}
   output.push({extension:extensions[i],accepted:code==='request_timeout',sourceCode:code})
  }
  const target=new URL('./testdata/ws-negotiation-source.json',import.meta.url),text=JSON.stringify(output,null,2)+'\n'
  if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('negotiation source changed')}else writeFileSync(target,text)
 }finally{for(const socket of sockets)socket.destroy();await new Promise(r=>server.close(r))}
}
