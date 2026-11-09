import {readFileSync,writeFileSync,mkdirSync} from 'node:fs'
import {OneBotAdapter,OneBotError} from '../../../../src/plugins/builtin/snowluma/transport.ts'
const output=[]
const scenarios=[
 {name:'text'}, {name:'empty',parts:[]}, {name:'unknown-part',parts:[{type:'unsupported'}]},
 {name:'cancel-before',cancel:true}, {name:'mention-all',parts:[{type:'mention',userId:'all'}]},
 {name:'bad-mention',parts:[{type:'mention',userId:'ob:u:broken'}]},
 {name:'managed',parts:[{type:'media',assetId:'managed'}]},
 {name:'fallback',parts:[{type:'media',assetId:'fallback'}]},
 {name:'fallback-array-type',parts:[{type:'media',assetId:'fallback-array'}]},
 {name:'source-array-type',parts:[{type:'media',assetId:'source-array'}]},
 {name:'source-before-bad-part',parts:[{type:'media',assetId:'managed'},{type:'unsupported'}]},
 {name:'bad-part-before-source',parts:[{type:'unsupported'},{type:'media',assetId:'managed'}]},
 {name:'missing-media',parts:[{type:'media',assetId:'missing'}]},
 {name:'source-validation-fails',parts:[{type:'media',assetId:'managed'}],failValidation:true},
 {name:'before-transmit-fails',failBefore:true},
 {name:'missing-receipt',response:{}}, {name:'invalid-response',response:null},
 {name:'failed-action',actionError:{code:'unsupported',transmitted:false}},
 {name:'unknown-action',actionError:{code:'connection_closed',transmitted:true}},
 {name:'ordinary-action-error',ordinaryError:true},
 {name:'reply'}, {name:'cross-scene-reply',reply:'other:m:abc'},
 {name:'private',private:true}, {name:'cross-account-scene',crossAccount:true},
]
for(const scenario of scenarios){
 const trace=[],requests=[]
 const adapter=new OneBotAdapter({id:'snowluma',accountId:'42',url:'ws://127.0.0.1:1'},{
  resolveMediaSource:(id,scene)=>{
   trace.push({kind:'resolve_source',assetId:id,sceneId:scene.id})
   if(id==='managed'||id==='source-array')return {type:id==='source-array'?['image']:'image',path:'/synthetic/fixture',bytes:3,hash:'a'.repeat(64),validate:()=>{trace.push({kind:'validate'});if(scenario.failValidation)throw Error('synthetic_changed')}}
  },
  resolveMedia:(id,scene)=>{trace.push({kind:'resolve_media',assetId:id,sceneId:scene.id});if(id==='fallback'||id==='fallback-array')return {type:id==='fallback-array'?['image']:'image',file:'https://example.invalid/image'}}
 })
 const scene=adapter.scene(scenario.private?'private':'group','99');if(scenario.crossAccount)scene.accountId='43'
 const segment={parts:scenario.parts??[{type:'text',text:'hello'}]}
 if(scenario.name==='reply')segment.replyToId=adapter.messageId(scene,'previous')
 if(scenario.reply)segment.replyToId=scenario.reply
 adapter.action=async(name,params,signal,bootstrap,preparation)=>{
  requests.push({name,params,sources:preparation.sources})
  preparation.validate()
  if(scenario.ordinaryError)throw Error('synthetic_action')
  if(scenario.actionError)throw new OneBotError(scenario.actionError.code,scenario.actionError.transmitted)
  return Object.hasOwn(scenario,'response')?scenario.response:{message_id:11}
 }
 const controller=new AbortController();if(scenario.cancel)controller.abort()
 try{
  const receipt=await adapter.send(scene,segment,controller.signal,()=>{trace.push({kind:'before_transmit'});if(scenario.failBefore)throw Error('synthetic_before')})
  output.push({name:scenario.name,scenario,input:{operation:'send_begin',adapter:'snowluma',account:'42',scene,segment,aborted:scenario.cancel??false},trace,requests,receipt})
 }finally{adapter.dispose()}
}
const target=new URL('./testdata/send-source.json',import.meta.url)
mkdirSync(new URL('./testdata/',import.meta.url),{recursive:true})
const text=JSON.stringify(output,null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('send source changed')}else writeFileSync(target,text)
