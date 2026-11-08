import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { validateConfig } from '../../../src/plugins/config.ts'
import { PluginManager, PluginConfigurationError } from '../../../src/plugins/manager.ts'

const schema={type:'object',properties:{speed:{type:'number',minimum:1}},required:['speed'],additionalProperties:false}
const cases=[
 {name:'required',config:{}},
 {name:'minimum',config:{speed:0}},
 {name:'unknown',config:{speed:2,unknown:true}},
]
// Execute the actual source validation method. This method does not touch Store,
// Runtime or a queue; its ordinary validator errors deliberately lose raw details.
const item={plugin:{manifest:{configSchema:schema}}}
const output=cases.map(({name,config})=>{
 let reason,wrapped
 try{validateConfig(config,schema)}catch(error){reason=error.message}
 try{PluginManager.prototype.validateConfiguration.call(null,item,config)}
 catch(error){wrapped={typed:error instanceof PluginConfigurationError,name:error.name,message:error.message,issues:error.issues}}
 if(!reason||!wrapped)throw Error('invalid fixture accepted: '+name)
 return {name,config,reason,wrapped}
})
const file=fileURLToPath(new URL('./testdata/configuration-errors.json',import.meta.url))
const encoded=JSON.stringify(output,null,2)+'\n'
if(process.argv.includes('--check')){if(readFileSync(file,'utf8')!==encoded)throw Error('configuration error source fixture changed')}
else writeFileSync(file,encoded)
