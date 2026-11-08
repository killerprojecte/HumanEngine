import {readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {pathToFileURL,fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../../../',import.meta.url)));
const {modelsReadValue}=await import(pathToFileURL(resolve(root,'src/config-read-values.ts')));
const provider={id:'p',name:'Synthetic',protocol:'openai-chat',baseUrl:'https://synthetic.invalid'};
const model={id:'m',displayName:'Synthetic',providerId:'p',apiModelName:'synthetic',capabilities:['text']};
const cases=[];
for(const [name,input,present] of [
 ['empty',{providers:[],models:[],routes:[]},false],
 ['defaults',{providers:[provider],models:[model],routes:[]},false],
 ['thinker',{providers:[provider],models:[model],routes:[{role:'thinker',orderedModelIds:['m'],timeoutMs:10000}]},false],
 ['credential-present',{providers:[provider],models:[model],routes:[]},true],
 ['stale-credential-true',{providers:[{...provider,credentialConfigured:true}],models:[model],routes:[]},false],
 ['stale-credential-false',{providers:[{...provider,credentialConfigured:false}],models:[model],routes:[]},true],
 ['order',{routes:[],models:[model],providers:[{baseUrl:provider.baseUrl,protocol:provider.protocol,name:provider.name,id:provider.id}]},false],
 ]){
 const queries=[];const value=modelsReadValue(input,id=>{queries.push(id);return present});
 const output=JSON.stringify(value);const key=createHash('sha256').update(JSON.stringify(['visual-expression-v2','s','m','a',value])).digest('hex');
 cases.push({name,input,present,queries,output,key});
}
const sourceHashes=Object.fromEntries(['src/config-read-values.ts','src/model-roles.ts','src/schema.ts'].map(f=>[f,createHash('sha256').update(readFileSync(resolve(root,f))).digest('hex')]));
const text=JSON.stringify({sourceHashes,cases},null,2)+'\n';const target=new URL('./model-read-source.json',import.meta.url);
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('model_read_fixture_stale')}else writeFileSync(target,text);
