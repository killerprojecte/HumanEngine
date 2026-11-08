// Actual source manager, Runtime and SQLite Store. No substitute manager/ports.
// Run Node with --experimental-transform-types from the repository root.
import { Store } from '../../../src/store.ts'
import { Runtime } from '../../../src/runtime.ts'
import { PluginManager } from '../../../src/plugins/manager.ts'
import { readFileSync, writeFileSync } from 'node:fs'
const store=new Store(':memory:')
const runtime=new Runtime(store)
const manager=new PluginManager(store,runtime)
const trace=[]
let count=0,held
const plugin={manifest:{id:'queued',name:'Counter fixture',version:'1',capabilities:[],configSchema:{type:'object',additionalProperties:true}},validateConfiguration(config){held=config;trace.push(`validate:${++count}`)},prepareConfiguration(config){if(config!==held)throw Error('identity_lost');trace.push(`prepare:${count}`);return {...config,count}},setup(context){context.registerAdminAction('count',async()=>count)}}
try{
 await manager.register(plugin)
 await manager.enable('queued')
 const a=manager.configure('queued',{who:'a'})
 const b=manager.configure('queued',{who:'b'})
 await Promise.all([a,b])
 const result={trace:[...trace],config:manager.config('queued'),action:await manager.action('queued','count',{})}
 publish(new URL('./testdata/configuration-source.json',import.meta.url),JSON.stringify(result,null,2)+'\n')
}finally{await manager.dispose();runtime.dispose();store.close()}

function publish(path,value){
 if(process.argv.includes('--check')){
  if(readFileSync(path,'utf8')!==value)throw Error('plugin_configuration_source_fixture_mismatch:'+path.pathname)
 }else writeFileSync(path,value)
}
