import { readFileSync,writeFileSync,mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { OneBotAdapter, OneBotError } from '../../../../src/plugins/builtin/snowluma/transport.ts'
import { parseCqMessage } from '../../../../src/plugins/builtin/snowluma/normalization.ts'
const receivedAt='2026-10-18T04:05:06.007Z'
const NativeDate=globalThis.Date
globalThis.Date=class extends NativeDate{
 constructor(...args){super(...(args.length?args:[receivedAt]))}
 static now(){return new NativeDate(receivedAt).getTime()}
}
let recalled=false
const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url:'ws://127.0.0.1:1',token:'synthetic'},{isRecalled:()=>recalled})
const base={self_id:42,message_type:'group',group_id:99,user_id:7,message_id:10,time:1700000000,sender:{nickname:' Sender '},group_name:' Group ',message:[{type:'text',data:{text:'Hello'}}]}
const scenarios=[
 {name:'text',event:{}},
 {name:'cq-mention-reply',event:{message:'a[CQ:at,qq=all][CQ:reply,id=3]b&#91;'}},
 {name:'private-incoming',event:{message_type:'private'}},
 {name:'private-outgoing',event:{message_type:'private',user_id:42,target_id:8}},
 {name:'private-temporary',event:{message_type:'private',sub_type:'group'}},
 {name:'account-mismatch',event:{self_id:43}},
 {name:'null-account',event:{self_id:null}},
 {name:'missing-account-history',omit:['self_id'],event:{},origin:'history'},
 {name:'sender-fallback',omit:['user_id'],event:{sender:{user_id:'8',card:' Card ',nickname:'Nick'}}},
 {name:'invalid-sender-object',omit:['user_id'],event:{sender:[]}},
 {name:'missing-message-format',omit:['message'],event:{}},
 {name:'invalid-segment',event:{message:[null]}},
 {name:'invalid-data',event:{message:[{type:'text',data:null}]}},
 {name:'recalled-before-parts',event:{message:[null]},recalled:true},
 {name:'expected-scene-mismatch',event:{},expected:adapter.scene('group','100')},
 {name:'array-scene-coercion',event:{message_type:['group']}},
 {name:'nested-array-scene',event:{message_type:[['group']]}},
 {name:'array-private-scene',event:{message_type:['private'],sub_type:'group'}},
 {name:'array-scene-invalid-message-id',event:{message_type:['group'],message_id:null}},
 {name:'array-scene-invalid-sender',event:{message_type:['group'],user_id:{}}},
 {name:'array-scene-self-invalid-target',event:{message_type:['group'],user_id:42,target_id:null}},
 {name:'array-scene-expected-mismatch',event:{message_type:['group']},expected:adapter.scene('group','99')},
 {name:'array-scene-recalled',event:{message_type:['group']},recalled:true},
 {name:'array-empty-scene',event:{message_type:[]}},
 {name:'array-double-scene',event:{message_type:['group','private']}},
 {name:'object-scene',event:{message_type:{toString:'group'}}},
 {name:'null-scene',event:{message_type:null}},
 {name:'boolean-scene',event:{message_type:true}},
 {name:'numeric-scene',event:{message_type:1}},
 {name:'invalid-scene',event:{message_type:'channel'}},
 {name:'unsafe-numeric-id',event:{message_id:9007199254740992}},
 {name:'lone-id',event:{user_id:'\ud800',message_id:'\udfff'}},
 {name:'label-units',event:{sender:{card:'\u202e A\u0000'+ 'x'.repeat(124)+'😀'},group_name:'\u2066 G\u2069'}},
 {name:'image-metadata',event:{message:[{type:'image',data:{url:'https://example.invalid/a',file_id:'f',summary:' untrusted\u0001 ',sub_type:2,emoji_id:'1'}}]}},
 {name:'image-unavailable',event:{message:[{type:'image',data:{file:12}}]}},
 {name:'inline-image',event:{message:[{type:'image',data:{base64:'YWJj',file:'stored'}}]}},
 {name:'sticker-label',event:{message:[{type:'sticker',data:{file_id:'abc',name:'x'.repeat(199)+'😀'}}]}},
 {name:'faces',event:{message:[{type:'face',data:{id:12,name:'Smile'}},{type:'face',data:{id:['13']}},{type:'face',data:{id:'123456789'}}]}},
 {name:'audio-video',event:{message:[{type:'record',data:{url:'record-url',file_id:''}},{type:'video',data:{base64:''}}]}},
 {name:'unknown-segments',event:{message:[{type:'text',data:{text:12}},{type:['image'],data:{file:'asset'}},{data:{}},{type:{},data:{}}]}},
 {name:'minimum-date',event:{time:-8640000000000}},
 {name:'maximum-date',event:{time:8640000000000}},
 {name:'fractional-date',event:{time:-0.0005}},
 {name:'invalid-date',event:{time:8640000000001}},
]
const output=[]
try{
 for(const scenario of scenarios){
  const event={...base,...scenario.event};for(const key of scenario.omit??[])delete event[key]
  recalled=scenario.recalled??false
  const input={operation:'normalize',adapter:'snowluma',account:'42',event,origin:scenario.origin??'live',receivedAt,recalled,...(scenario.expected?{expected:scenario.expected}:{})}
  let expected
  try{expected=adapter.normalize(event,input.origin,scenario.expected)}
  catch(error){expected={error:error instanceof OneBotError?error.code:error.message,oneBot:error instanceof OneBotError}}
  output.push({name:scenario.name,input,expected})
 }
 for(const text of ['[CQ:text,__proto__=ignored,text=actual]','prefix[CQ:bad[CQ:at,qq=all]tail','[CQ:image,file=a&#44;b]&amp;']){
  output.push({name:'cq-'+output.length,input:{operation:'parse_cq',text},expected:parseCqMessage(text)})
 }
 const file=fileURLToPath(new URL('./testdata/normalization-source.json',import.meta.url))
 mkdirSync(fileURLToPath(new URL('./testdata/',import.meta.url)),{recursive:true})
 const encoded=JSON.stringify(output,null,2)+'\n'
 if(process.argv.includes('--check')){if(readFileSync(file,'utf8')!==encoded)throw Error('source normalization fixture changed')}
 else writeFileSync(file,encoded)
}finally{adapter.dispose();globalThis.Date=NativeDate}
