import { requestPublicResource } from '../../../src/network-request.ts'
import { request as httpRequest } from 'node:http'
import net from 'node:net'
import { readFileSync } from 'node:fs'
import { deepStrictEqual } from 'node:assert/strict'
const rows = [
 {name:'accept-encoding override keeps first position',url:'http://fixture.invalid/',options:{headers:{'X-First':'one','Accept-Encoding':'gzip','X-Last':'two'}},responses:['HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok']},
 {name:'Node default raw header pair limit',url:'http://fixture.invalid/',options:{},responses:['HTTP/1.1 200 OK\r\n'+ 'X: a\r\n'.repeat(1050) +'Content-Length: 2\r\nConnection: close\r\n\r\nok']},
 {name:'get binary and raw duplicate headers',url:'http://fixture.invalid/path?q=1#removed',options:{},responses:['HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nX-Repeat: first\r\nx-repeat: second\r\nSet-Cookie: a=1\r\nSet-Cookie: b=2\r\nContent-Length: 4\r\nConnection: close\r\n\r\n\x00\x80\xffZ']},
 {name:'head response',url:'http://fixture.invalid/',options:{method:'HEAD'},responses:['HTTP/1.1 200 OK\r\nContent-Length: 50\r\nConnection: close\r\n\r\n']},
 {name:'post redirects across origin',url:'http://fixture.invalid/start',options:{method:'POST',body:'hello',headers:{'X-Private':'test-only','Content-Type':'text/plain'}},responses:['HTTP/1.1 302 Found\r\nLocation: http://second.invalid/end#gone\r\nContent-Length: 0\r\nConnection: close\r\n\r\n','HTTP/1.1 201 Created\r\nContent-Type: text/plain\r\nContent-Length: 3\r\nConnection: close\r\n\r\nyes']},
 {name:'put 307 retains body',url:'http://fixture.invalid/start',options:{method:'PUT',body:'abc',headers:{'Content-Type':'text/plain'}},responses:['HTTP/1.1 307 Redirect\r\nLocation: /end\r\nContent-Length: 0\r\nConnection: close\r\n\r\n','HTTP/1.1 206 Partial\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n3\r\nabc\r\n0\r\n\r\n']},
 {name:'no follow exposes 302',url:'http://fixture.invalid/',options:{followRedirects:false},responses:['HTTP/1.1 302 Found\r\nLocation: /next\r\nContent-Length: 0\r\nConnection: close\r\n\r\n']},
 {name:'redirect budget zero',url:'http://fixture.invalid/',options:{maxRedirects:0},responses:['HTTP/1.1 302 Found\r\nLocation: /next\r\nContent-Length: 0\r\nConnection: close\r\n\r\n']},
 {name:'error status chunked stays exact',url:'http://fixture.invalid/',options:{method:'DELETE'},responses:['HTTP/1.1 429 Rate Limited\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n3;x=y\r\nno!\r\n0\r\nX-Trailer: done\r\n\r\n']},
 {name:'103 then final response',url:'http://fixture.invalid/',options:{},responses:['HTTP/1.1 103 Early Hints\r\nLink: </a>\r\n\r\nHTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok']},
 {name:'unsafe mixed DNS rejected',url:'http://fixture.invalid/',options:{},addresses:[{address:'8.8.8.8',family:4},{address:'127.0.0.1',family:4}],responses:[]},
 {name:'private redirect rejected',url:'http://fixture.invalid/',options:{},responses:['HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1/\r\nContent-Length: 0\r\nConnection: close\r\n\r\n']},
 {name:'forbidden header rejected',url:'http://fixture.invalid/',options:{headers:{Host:'wrong'}},responses:[]},
 {name:'HEAD body rejected',url:'http://fixture.invalid/',options:{method:'HEAD',body:''},responses:[]},
 {name:'same casing overwrite order',url:'http://fixture.invalid/',options:{headers:{'x-test':'first','X-Test':'last'}},responses:['HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n']},
]
const output=[]
for (const row of rows) {
 const requests=[],lookups=[];let responseIndex=0
 const sockets=new Set()
 const server=net.createServer(socket=>{sockets.add(socket);socket.on('close',()=>sockets.delete(socket));socket.on('error',()=>{});let bytes=Buffer.alloc(0),sent=false
  socket.on('data',chunk=>{bytes=Buffer.concat([bytes,chunk]);const end=bytes.indexOf('\r\n\r\n');if(end<0||sent)return
   const lines=bytes.subarray(0,end).toString('latin1').split('\r\n');const [method,path]=lines.shift().split(' ');const headers={}
   for(const line of lines){const i=line.indexOf(':');headers[line.slice(0,i).toLowerCase()]=line.slice(i+1).trim()}
   const length=Number(headers['content-length']??0);if(bytes.length<end+4+length)return;sent=true
   requests.push({method,path,headers,headerNames:lines.map(line=>line.slice(0,line.indexOf(':'))),body:bytes.subarray(end+4,end+4+length).toString('base64')})
   const response=row.responses[responseIndex++];if(response===undefined)throw new Error('unexpected local request')
   socket.end(Buffer.from(response,'latin1'))
  })
 })
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const port=server.address().port
 const transport={lookup:async hostname=>{lookups.push(hostname);return row.addresses??[{address:'8.8.8.8',family:4}]},request:(options,callback)=>httpRequest({...options,hostname:'127.0.0.1',family:4,port},callback)}
 let expected
 try {const result=await requestPublicResource(row.url,row.options,transport);const chunks=[];for await(const c of result.response)chunks.push(c)
  expected={statusCode:result.statusCode,headers:result.headers,rawHeaders:result.rawHeaders,finalUrl:result.finalUrl,contentType:result.contentType,body:Buffer.concat(chunks).toString('base64')}
 }catch(e){expected={error:e.message,typeError:e instanceof TypeError}}
 for(const socket of sockets)socket.destroy();await new Promise(resolve=>server.close(resolve))
 output.push({...row,requests,lookups,expected})
}
if(process.argv[2]==='--print')console.log(JSON.stringify(output,null,2))
else{deepStrictEqual(output,JSON.parse(readFileSync(new URL('./core/testdata/node-network.json',import.meta.url),'utf8')));console.log(`PASS ${output.length} source network fixtures`)}
