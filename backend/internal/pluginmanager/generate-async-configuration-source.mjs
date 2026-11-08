import { Store } from '../../../src/store.ts'
import { Runtime } from '../../../src/runtime.ts'
import { PluginManager } from '../../../src/plugins/manager.ts'
import { readFileSync, writeFileSync } from 'node:fs'
const store=new Store(':memory:'),runtime=new Runtime(store),manager=new PluginManager(store,runtime)
const trace=[];let count=0,entered,release,late
const entering=new Promise(resolve=>entered=resolve),unblocked=new Promise(resolve=>release=resolve),finished=new Promise(resolve=>late=resolve)
const plugin={manifest:{id:'queued',name:'Async fixture',version:'1',capabilities:[],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(){trace.push(`validate:${++count}`)},async setup(c){trace.push(`setup:${count}:${c.config.who??'initial'}`);if(c.config.who==='a'){entered();await unblocked;try{c.registerAdminAction('stale',async()=>0);late('accepted')}catch(e){late(e.code??e.message)};return}c.registerAdminAction('count',async()=>count)}}
try{
 await manager.register(plugin);await manager.enable('queued')
 const first=manager.configure('queued',{who:'a'});const observed=first.then(()=>({status:'fulfilled'}),e=>({status:'rejected',code:e.code??e.message}))
 await entering
 await manager.configure('queued',{who:'b'})
 const outcome=await observed,action=await manager.action('queued','count',{})
 release();const lateRegistration=await finished
 publish(new URL('./testdata/async-configuration-source.json',import.meta.url),JSON.stringify({trace,first:outcome,action,lateRegistration},null,2)+'\n')
}finally{release();await manager.dispose();runtime.dispose();store.close()}

function publish(path,value){
 if(process.argv.includes('--check')){
  if(readFileSync(path,'utf8')!==value)throw Error('plugin_configuration_source_fixture_mismatch:'+path.pathname)
 }else writeFileSync(path,value)
}
