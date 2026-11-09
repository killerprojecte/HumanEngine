import {readFileSync,writeFileSync} from 'node:fs'
import {snowLumaPlugin} from '../../../../src/plugins/builtin/snowluma/index.ts'
import {SnowLumaConfigSchema} from '../../../../src/plugins/builtin/snowluma/config.ts'
const base={transport:'websocket',url:'ws://127.0.0.1:1',accountId:'42'}
const cases=[
 {name:'prepare-ws-retains-http',kind:'prepare',config:{...base,eventPort:777,eventToken:'discard',clearEventToken:true,token:'new'},previous:{eventPort:123,eventPath:'/prior',eventCredentialEnv:'SNOW_EVENT'}},
 {name:'prepare-http-both',kind:'prepare',config:{...base,transport:'http',eventPort:123,token:'new',eventToken:'next'}},
 {name:'prepare-clear-before-set',kind:'prepare',config:{...base,transport:'http',eventPort:123,token:'new',clearToken:true,credentialEnv:'OLD',eventToken:'next'}},
 {name:'prepare-empty',kind:'prepare',config:{...base,token:''}},
 {name:'prepare-unknown-retained',kind:'prepare',config:{...base,extra:7}},
 ...['secret_key_unavailable','secret_key_missing','secret_key_permissions','secret_key_invalid','secret_decryption_failed','private_failure'].map(error=>({name:'prepare-error-'+error,kind:'prepare',config:{...base,token:'new',eventToken:'next',transport:'http',eventPort:123},fail:'token',error})),
 {name:'setup-token-absent',kind:'setup',config:base,secrets:{}},
 {name:'setup-empty-does-not-fallback',kind:'setup',config:{...base,credentialEnv:'SNOW_TEST_TOKEN'},secrets:{token:''},env:{SNOW_TEST_TOKEN:'fallback'}},
 {name:'setup-fallback',kind:'setup',config:{...base,credentialEnv:'SNOW_TEST_TOKEN'},secrets:{},env:{SNOW_TEST_TOKEN:'fallback'}},
 {name:'setup-stored',kind:'setup',config:base,secrets:{token:'stored',eventToken:'event'}},
 {name:'setup-http-event-missing',kind:'setup',config:{...base,transport:'http',url:'http://127.0.0.1:1',eventPort:123},secrets:{token:'stored'}},
 {name:'setup-ws-still-reads-event',kind:'setup',config:base,secrets:{token:'stored'},fail:'eventToken',error:'secret_key_missing'},
 {name:'setup-secret-error',kind:'setup',config:base,fail:'token',error:'private_failure'},
 {name:'setup-event-fallback',kind:'setup',config:{...base,transport:'http',url:'http://127.0.0.1:1',eventPort:123,eventCredentialEnv:'SNOW_TEST_EVENT'},secrets:{token:'stored'},env:{SNOW_TEST_EVENT:'event'}},
 {name:'setup-abort-after-credentials',kind:'setup',config:base,secrets:{token:'stored'}}
]
for(const [name,delta] of Object.entries({
 valid:{},legacy:{transport:undefined},http:{transport:'http',url:'http://localhost',eventPort:123},
 unknown:{extra:1},empty:{url:'',accountId:''},types:{url:7,accountId:null},
 scheme:{url:'http://localhost'},credentials:{url:'ws://user:pass@localhost'},query:{url:'ws://localhost?x=1'},hash:{url:'ws://localhost#x'},
 whitespace:{url:' ws://localhost '},invalidhost:{url:'ws://[broken'},enum:{transport:null},
 wsIgnored:{eventPort:'bad',eventHost:7,eventToken:3,eventPath:3},
 conflict:{token:'x',clearToken:true},tokenType:{token:7},tokenBlank:{token:' \ufeff '},tokenLong:{token:'x'.repeat(16385)},
 badBoolean:{clearToken:7},env:{credentialEnv:'bad-env'},envEmpty:{credentialEnv:''},
 portMissing:{transport:'http',url:'http://localhost'},portFraction:{transport:'http',url:'http://localhost',eventPort:1.5},
 eventBad:{transport:'http',url:'http://localhost',eventPort:1,eventHost:'localhost',eventPath:'//bad'},
 eventConflict:{transport:'http',url:'http://localhost',eventPort:1,eventToken:'x',clearEventToken:true},
 unknownAndBadURL:{extra:1,url:'ws://bad host'},emptyQueryHash:{url:'ws://localhost?#'},
 utf16Account:{accountId:'😀'.repeat(129)}
}))cases.push({name:'validate-'+name,kind:'validate',config:{...base,...delta}})
const output=[]
for(const c of cases){
 const trace=[];const env={};for(const [k,v]of Object.entries(c.env??{})){env[k]=process.env[k];process.env[k]=v}
 const plugin=snowLumaPlugin({addAdapter(){trace.push({kind:'add'})},removeAdapter(){trace.push({kind:'remove'})}},{get(){return c.previous??{}},secrets:{has(){return false},get(scope,key){trace.push({kind:'get',key});if(c.fail===key)throw Error(c.error);return c.secrets?.[key]},apply(scope,key,action){trace.push({kind:'apply',key,...action});if(c.fail===key)throw Error(c.error)}}},{adapterStopped(){trace.push({kind:'stopped'})}})
 let result,error
 try{
  if(c.kind==='validate')result=plugin.validateConfiguration(c.config)
  else if(c.kind==='prepare')result=plugin.prepareConfiguration(c.config)
  else{const controller=new AbortController();controller.abort();await plugin.setup({config:c.config,signal:controller.signal,onDispose(){trace.push({kind:'onDispose'})},requestId:'synthetic'})}
 }catch(e){error=c.kind==='validate'?{issues:e.issues}:{code:e.code??e.message}}
 finally{for(const [k,v]of Object.entries(env)){if(v===undefined)delete process.env[k];else process.env[k]=v}}
 output.push({name:c.name,scenario:c,...(c.kind==='setup'?{parsed:SnowLumaConfigSchema.parse(c.config)}:{}),trace,...(result===undefined?{}:{result}),...(error?{error}:{})})
}
const text=JSON.stringify(output,null,2)+'\n',target=new URL('./testdata/installation-source.json',import.meta.url)
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('installation source changed')}else writeFileSync(target,text)
console.log(`actual plugin prepare/setup ${output.length} cases`)
