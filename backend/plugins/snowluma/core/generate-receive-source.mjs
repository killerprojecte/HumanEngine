import {readFileSync,writeFileSync} from 'node:fs'
import {OneBotAdapter} from '../../../../src/plugins/builtin/snowluma/transport.ts'
const now='2026-10-18T04:05:06.007Z',NativeDate=Date
const base={self_id:42,post_type:'message',message_type:'group',group_id:99,user_id:7,message_id:10,time:1700000000,message:[{type:'text',data:{text:'hello'}}]}
const cases=[
 {name:'live-message'}, {name:'sent-message',event:{post_type:'message_sent'}},
 {name:'not-ready',ready:false},{name:'disposed',disposed:true},
 {name:'wrong-account',event:{self_id:43}},{name:'missing-account',omit:['self_id']},
 {name:'invalid-account',event:{self_id:{}}},{name:'unknown-post',event:{post_type:'request'}},
 {name:'group-recall',event:{post_type:'notice',notice_type:'group_recall'}},
 {name:'friend-recall',event:{post_type:'notice',notice_type:'friend_recall'}},
 {name:'array-recall',event:{post_type:'notice',notice_type:['group_recall']}},
 {name:'recall-invalid-time',event:{post_type:'notice',notice_type:'group_recall',time:null}},
 {name:'recall-no-callback-invalid-time',recallCallback:false,event:{post_type:'notice',notice_type:'group_recall',time:null}},
 {name:'recall-callback-throws',throwRecall:true,event:{post_type:'notice',notice_type:'group_recall'}},
 {name:'message-callback-throws',throwMessage:true},
 {name:'known-echo-before-ready',ready:false,knownEcho:true,event:{echo:'pending',status:'ok',retcode:0,data:{answer:1}}},
 {name:'unknown-echo-continues-event',event:{echo:'missing'}},
 ...[1404,1400,1,0,'0'].map(code=>({name:'echo-failed-'+typeof code+'-'+code,knownEcho:true,event:{echo:'pending',status:'failed',retcode:code}})),
 {name:'echo-ok-numeric',knownEcho:true,event:{echo:'pending',status:'ok',retcode:0,data:null}},
 {name:'echo-ok-string',knownEcho:true,event:{echo:'pending',status:'ok',retcode:'0'}},
]
globalThis.Date=class extends NativeDate{constructor(...args){super(...(args.length?args:[now]))}static now(){return new NativeDate(now).getTime()}}
const output=[]
try{
 for(const scenario of cases){
  const trace=[]
  const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url:'ws://127.0.0.1:1',token:'synthetic'},{
   onMessage:(message,scene)=>{trace.push({kind:'message',message,scene});if(scenario.throwMessage)throw Error('synthetic_callback')},
   ...(scenario.recallCallback===false?{}:{onRecall:(recall,scene)=>{trace.push({kind:'recall',recall,scene});if(scenario.throwRecall)throw Error('synthetic_callback')}}),
   onError:code=>trace.push({kind:'error',code})
  })
  adapter.ready=scenario.ready??true
  if(scenario.disposed)adapter.dispose()
  if(scenario.knownEcho)adapter.pending.set('pending',{cleanup:()=>trace.push({kind:'cleanup'}),resolve:data=>trace.push({kind:'response',data}),reject:error=>trace.push({kind:'response_error',code:error.code,transmitted:error.transmitted})})
  const event={...base,...scenario.event};for(const key of scenario.omit??[])delete event[key]
  adapter.receive(JSON.stringify(event))
  output.push({name:scenario.name,input:{operation:'receive',adapter:'snowluma',account:'42',ready:scenario.ready??true,disposed:scenario.disposed??false,knownEcho:scenario.knownEcho??false,recallCallback:scenario.recallCallback!==false,receivedAt:now,event},trace,remembered:[...adapter.recalled]})
  adapter.dispose()
 }
 const target=new URL('./testdata/receive-source.json',import.meta.url),text=JSON.stringify(output,null,2)+'\n'
 if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('receive source changed')}else writeFileSync(target,text)
}finally{globalThis.Date=NativeDate}
