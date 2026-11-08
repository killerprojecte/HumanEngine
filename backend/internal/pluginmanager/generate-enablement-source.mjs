// Real source manager and native SQLite Store: no substitute lifecycle ports.
import { Store } from '../../../src/store.ts'
import { Runtime } from '../../../src/runtime.ts'
import { PluginManager } from '../../../src/plugins/manager.ts'
import { readFileSync, writeFileSync } from 'node:fs'
const results=[]
for(const ordering of ['enable-then-configure','configure-then-enable']){
 const store=new Store(':memory:'),runtime=new Runtime(store),manager=new PluginManager(store,runtime)
 const trace=[];let count=0,held
 const plugin={manifest:{id:'queued',name:'Enable fixture',version:'1',capabilities:[],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(c){held=c;trace.push(`validate:${++count}`)},prepareConfiguration(c){if(c!==held)throw Error('identity');trace.push(`prepare:${count}`);return {...c,count}},setup(c){trace.push(`setup:${count}`);c.registerAdminAction('count',async()=>count)}}
 try{
  await manager.register(plugin)
  let enabled,configured
  if(ordering==='enable-then-configure'){enabled=manager.enable('queued');configured=manager.configure('queued',{who:'configured'})}
  else{configured=manager.configure('queued',{who:'configured'});enabled=manager.enable('queued')}
  const outcomes=await Promise.allSettled([enabled,configured])
  results.push({ordering,trace:[...trace],outcomes:outcomes.map(r=>r.status==='fulfilled'?{status:r.status}:{status:r.status,code:r.reason.code??r.reason.message}),config:manager.config('queued'),action:await manager.action('queued','count',{})})
 }finally{await manager.dispose();runtime.dispose();store.close()}
}
publish(new URL('./testdata/enablement-source.json',import.meta.url),JSON.stringify(results,null,2)+'\n')

function publish(path,value){
 if(process.argv.includes('--check')){
  if(readFileSync(path,'utf8')!==value)throw Error('plugin_configuration_source_fixture_mismatch:'+path.pathname)
 }else writeFileSync(path,value)
}
