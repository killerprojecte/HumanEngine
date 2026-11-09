// Executes the Engine.start-installed callback, real plugin setup and real local
// peers. Only the underlying original-file IO owner is synthetic here.
import {Engine}from'../../../../../src/engine.ts'
import {createServer}from'node:http'
import {once}from'node:events'
import {WebSocketServer}from'ws'
import {mkdtempSync,writeFileSync,readFileSync,rmSync}from'node:fs'
import {tmpdir}from'node:os'
import {join}from'node:path'
import {createHash}from'node:crypto'
const root=mkdtempSync(join(tmpdir(),'snow-engine-'));const file=join(root,'image.bin'),bytes=Buffer.from([0,1,2,253,254,255]);writeFileSync(file,bytes);const hash=createHash('sha256').update(bytes).digest('hex');const rows=[]
try{for(const scenario of ['sent','not-managed','original-error','path-changed','hash-changed','bytes-changed','contentType-changed','deleted-before-wire']){
 const trace=[],requests=[];let adapter,plugin,reads=0;const sentinel=Error('captured_source_registration')
 const runtime={addAdapter(id,value){adapter=value;trace.push('add')},removeAdapter(){trace.push('remove')}}
 const store={secrets:{get(scope,key){return key==='token'?'synthetic':undefined}},scenes(){return[]},log(){}}
 const owner={workspaceMaintenance:{start(){}},memory:{flushProjections(){}},runtime,store,plugins:{register(value){plugin=value;throw sentinel}},memes:{original(context,id){trace.push({kind:'original',context,id});reads++;if(scenario==='original-error'||(scenario==='deleted-before-wire'&&reads>1))throw Error('meme_not_found');const value={path:file,hash,bytes:bytes.length,contentType:'image/png'};if(reads>1){if(scenario==='path-changed')value.path=file+'.changed';if(scenario==='hash-changed')value.hash='b'.repeat(64);if(scenario==='bytes-changed')value.bytes++;if(scenario==='contentType-changed')value.contentType='image/jpeg'}return value}}}
 try{await Engine.prototype.start.call(owner)}catch(e){if(e!==sentinel)throw e}
 const server=createServer(),sockets=new WebSocketServer({server});sockets.on('connection',socket=>socket.on('message',raw=>{const r=JSON.parse(raw);if(r.action!=='get_login_info')requests.push({action:r.action,params:r.params});socket.send(JSON.stringify({echo:r.echo,status:'ok',retcode:0,data:r.action==='get_login_info'?{user_id:42}:{message_id:11}}))}));server.listen(0,'127.0.0.1');await once(server,'listening')
 const controller=new AbortController(),cleanup=[]
 try{
  await plugin.setup({config:{transport:'websocket',url:`ws://127.0.0.1:${server.address().port}`,accountId:'42'},signal:controller.signal,onDispose:f=>cleanup.push(f),requestId:'synthetic'})
  const scene=adapter.scene('group','99'),assetId=scenario==='not-managed'?'not-managed':hash
  const receipt=await adapter.send(scene,{parts:[{type:'text',text:'hello'},{type:'media',assetId}]},undefined,()=>trace.push('before'))
  controller.abort();for(const f of cleanup)f()
  rows.push({scenario,trace,requests,receipt})
 }finally{controller.abort();for(const f of cleanup)f();for(const c of sockets.clients)c.terminate();await new Promise(r=>sockets.close(r));await new Promise(r=>server.close(r))}
}}finally{rmSync(root,{recursive:true,force:true})}
const text=JSON.stringify(rows,null,2)+'\n',fileURL=new URL('./testdata/engine-source.json',import.meta.url)
if(process.argv.includes('--check')){if(readFileSync(fileURL,'utf8')!==text)throw Error('engine source changed')}else writeFileSync(fileURL,text)
console.log(`actual Engine.start callback + plugin setup + WebSocket send ${rows.length} cases`)
