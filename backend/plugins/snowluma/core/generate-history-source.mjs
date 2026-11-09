import {createServer}from'node:http'
import {once}from'node:events'
import {WebSocketServer}from'ws'
import {readFileSync,writeFileSync}from'node:fs'
import {OneBotAdapter}from'../../../../src/plugins/builtin/snowluma/transport.ts'
const RealDate=globalThis.Date;globalThis.Date=class extends RealDate{constructor(...args){super(...(args.length?args:['2026-01-01T00:00:00.000Z']))}static now(){return RealDate.parse('2026-01-01T00:00:00.000Z')}}
const event=(id,time=1700000000,text='hello')=>({self_id:42,post_type:'message',message_type:'group',group_id:99,user_id:7,message_id:id,time,message:[{type:'text',data:{text}}]})
const scenarios=[
 {name:'history-sort-deduplicate',kind:'history',input:{limit:2},data:{messages:[event(2,1700000002),event(1),event(2,1700000003,'replacement'),event(3,1700000001)]}},
 {name:'history-cursor',kind:'history',input:{limit:2},cursor:'1',data:{messages:[event(1),event(2,1700000001)]}},
 {name:'history-recalled-oldest',kind:'history',input:{limit:2},recalled:['1'],data:{messages:[event(1),event(2,1700000001)]}},
 {name:'history-empty',kind:'history',input:{limit:3},data:{messages:[]}},
 {name:'history-invalid-response',kind:'history',input:{limit:3},data:{messages:null}},
 {name:'history-zero-limit',kind:'history',input:{limit:0},data:{messages:[event(1),event(2,1700000001)]}},
 {name:'history-string-infinity',kind:'history',input:{limit:'Infinity'},data:{messages:[]}},
 {name:'history-missing-limit',kind:'history',input:{},data:{messages:[]}},
 {name:'history-invalid-limit',kind:'history',input:{limit:'not-number'},data:{messages:[]}},
 {name:'history-extended-dates',kind:'history',input:{limit:5},data:{messages:[event(1,-62167219200),event(2,253402300800),event(3,-62198755200),event(4,0)]}},
 {name:'history-failed-action',kind:'history',input:{limit:3},failed:true,data:null},
 {name:'message-sent',kind:'message',id:'1',data:event(1)},
 {name:'message-recalled',kind:'message',id:'1',recalled:['1'],data:event(1)},
 {name:'message-recall-before-platform-scope',kind:'message',id:'bad',rawId:'wrong-scope',recalledRaw:['wrong-scope'],data:event(1)},
 {name:'message-mismatch',kind:'message',id:'1',data:event(2)},
 {name:'message-invalid',kind:'message',id:'1',data:null},
 {name:'message-failed-action',kind:'message',id:'1',failed:true,data:null},
]
const rows=[],dates=new Set()
try{for(const s of scenarios){
 const trace=[],requests=[];let adapter,scene
 const server=createServer(),sockets=new WebSocketServer({server});sockets.on('connection',socket=>socket.on('message',raw=>{const q=JSON.parse(raw);if(q.action==='get_login_info'){socket.send(JSON.stringify({echo:q.echo,status:'ok',retcode:0,data:{user_id:42}}));return};requests.push({action:q.action,params:q.params});socket.send(JSON.stringify({echo:q.echo,status:s.failed?'failed':'ok',retcode:s.failed?1404:0,data:s.data}))}));server.listen(0,'127.0.0.1');await once(server,'listening')
 adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url:`ws://127.0.0.1:${server.address().port}`,token:'synthetic'},{isRecalled:(sceneId,id)=>{trace.push({kind:'recalled',sceneId,id});return(s.recalled??[]).some(x=>id===adapter.messageId(scene,x))||(s.recalledRaw??[]).includes(id)}})
 try{await adapter.connect();scene=adapter.scene('group','99');const input={...s.input};if(s.cursor)input.cursor=adapter.messageId(scene,s.cursor);const id=s.rawId??(s.id?adapter.messageId(scene,s.id):undefined)
 const result=s.kind==='message'?await adapter.getMessage(scene,id):await adapter.history(scene,input)
 for(const e of s.data?.messages??[])dates.add(new Date(e.time*1000).toISOString())
 rows.push({name:s.name,kind:s.kind,scenario:s,recalledMessageIds:[...(s.recalled??[]).map(x=>adapter.messageId(scene,x)),...(s.recalledRaw??[])],scene,input,...(id?{messageId:id}:{}),trace,requests,result})
 }finally{adapter.dispose();for(const c of sockets.clients)c.terminate();await new Promise(r=>sockets.close(r));await new Promise(r=>server.close(r))}
}}finally{globalThis.Date=RealDate}
const comparisons=[...dates].flatMap(a=>[...dates].map(b=>[a,b,a.localeCompare(b)]));const value={cases:rows,comparisons};const text=JSON.stringify(value,null,2)+'\n',file=new URL('./testdata/history-source.json',import.meta.url)
if(process.argv.includes('--check')){if(readFileSync(file,'utf8')!==text)throw Error('history source changed')}else writeFileSync(file,text)
console.log(`actual OneBotAdapter local WebSocket history/getMessage ${rows.length} cases`)
