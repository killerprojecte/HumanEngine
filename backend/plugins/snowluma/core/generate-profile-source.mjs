import {readFileSync,writeFileSync}from'node:fs'
import {OneBotProfiles}from'../../../../src/plugins/builtin/snowluma/profiles.ts'
const scene=n=>({id:`scene${n}`,kind:'group',platformSceneId:String(n),adapterId:'snowluma',accountId:'42'})
const message=n=>({id:`message${n}`,senderId:`user${n}`,senderPlatformId:String(n+10),status:'available'})
const scenarios=[
 {name:'success-cache-expire',steps:[{op:'observe',n:1},{op:'complete',index:0,value:{group_id:'1',group_name:'Group'}},{op:'complete',index:1,value:{user_id:'11',nickname:'User'}},{op:'observe',n:1},{op:'clock',now:1800001},{op:'observe',n:1}]},
 {name:'stale-completion',steps:[{op:'observe',n:1},{op:'observe',n:1,sceneName:'New scene',senderName:'New user'},{op:'complete',index:0,value:{group_id:1,group_name:'Old scene'}},{op:'complete',index:1,value:{user_id:11,nickname:'Old user'}}]},
 {name:'failed-negative-cache',steps:[{op:'observe',n:1},{op:'complete',index:0,failed:true},{op:'complete',index:1,value:null},{op:'observe',n:1},{op:'clock',now:300001},{op:'observe',n:1}]},
 {name:'pending-source-four',steps:[{op:'observe',n:1},{op:'observe',n:2},{op:'observe',n:3},{op:'complete',index:0,value:{group_id:'wrong',group_name:'Wrong'}},{op:'observe',n:3}]},
 {name:'disposed-pending',steps:[{op:'observe',n:1},{op:'dispose'},{op:'complete',index:0,value:{group_id:1,group_name:'Late'}},{op:'complete',index:1,failed:true},{op:'observe',n:2}]},
 {name:'recalled-ignored',steps:[{op:'observe',n:1,status:'recalled'}]},
 {name:'display-name-rules',steps:[{op:'observe',n:1},{op:'complete',index:0,value:{group_id:1,group_name:'\x00\u202e  Group  '}},{op:'complete',index:1,value:{user_id:11,nickname:'😀'.repeat(65)}}]},
 {name:'profile-callback-throws',throwUpdate:true,steps:[{op:'observe',n:1},{op:'complete',index:0,value:{group_id:1,group_name:'Group'}},{op:'complete',index:1,value:{user_id:11,nickname:'User'}}]}
]
const rows=[]
for(const scenario of scenarios){
 let now=0;const trace=[],calls=[],pending=[]
 const profiles=new OneBotProfiles((action,params,signal)=>{const index=calls.length;calls.push({action,params});trace.push({kind:'action',index,action,params});return new Promise((resolve,reject)=>pending.push({resolve,reject}))},profile=>{trace.push({kind:'profile',profile:structuredClone(profile)});if(scenario.throwUpdate)throw Error('synthetic_callback')},()=>trace.push({kind:'failure'}),()=>now)
 const steps=[]
 for(const step of scenario.steps){
  let observed
  if(step.op==='observe'){const s=scene(step.n),m=message(step.n);if(step.sceneName)s.displayName=step.sceneName;if(step.senderName)m.senderDisplayName=step.senderName;if(step.status)m.status=step.status;observed={input:{scene:structuredClone(s),message:structuredClone(m)}};profiles.observe(m,s);observed.result={scene:s,message:m}}
  else if(step.op==='complete'){if(step.failed)pending[step.index].reject(Error('synthetic_peer'));else pending[step.index].resolve(step.value)}
  else if(step.op==='clock')now=step.now
  else profiles.dispose()
  await new Promise(resolve=>setImmediate(resolve))
  steps.push({step,now,...observed,trace:structuredClone(trace)})
 }
 profiles.dispose();rows.push({name:scenario.name,throwUpdate:scenario.throwUpdate??false,steps})
}
const text=JSON.stringify(rows,null,2)+'\n',file=new URL('./testdata/profiles-source.json',import.meta.url)
if(process.argv.includes('--check')){if(readFileSync(file,'utf8')!==text)throw Error('profile source changed')}else writeFileSync(file,text)
console.log(`actual OneBotProfiles ${rows.length} scenarios, ${rows.reduce((n,r)=>n+r.steps.length,0)} steps`)
