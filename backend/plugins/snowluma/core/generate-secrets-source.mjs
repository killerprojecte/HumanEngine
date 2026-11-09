import {readFileSync,writeFileSync}from'node:fs'
import {snowLumaPlugin}from'../../../../src/plugins/builtin/snowluma/index.ts'
const rows=[]
for(const secrets of [{},{token:true},{eventToken:true},{token:true,eventToken:true}]){const trace=[];const plugin=snowLumaPlugin({}, {secrets:{has(scope,key){trace.push({scope,key});return secrets[key]??false}}},{});rows.push({secrets,trace,result:plugin.configuredSecrets()})}
const text=JSON.stringify(rows,null,2)+'\n',file=new URL('./testdata/configured-secrets-source.json',import.meta.url)
if(process.argv.includes('--check')){if(readFileSync(file,'utf8')!==text)throw Error('configured secret source changed')}else writeFileSync(file,text)
console.log('actual configuredSecrets 4 cases')
