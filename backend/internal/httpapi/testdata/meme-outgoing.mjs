import {readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {pathToFileURL,fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../../../',import.meta.url)));
const {Store}=await import(pathToFileURL(resolve(root,'src/store.ts')));
const {Engine}=await import(pathToFileURL(resolve(root,'src/engine.ts')));
const id='a'.repeat(64),cases=[];
for(const name of ['managed','uppercase','short','non-media','duplicate','no-receipt','recalled','usage-throws']){
 const store=new Store(':memory:'),engine=new Engine(store),runtime=engine.runtime;
 const scene={id:'s',kind:'group',adapterId:'a',accountId:'account',platformSceneId:'synthetic',learningEnabled:false,participationEnabled:false};
 const calls=[],original=[];
 // Instrument the actual installed Engine callbacks, not a reimplemented listener.
 engine.memes.recordUsage=(context,assetId)=>{calls.push({context,assetId});if(name==='usage-throws')throw Error('synthetic_usage')};
 engine.memes.original=(context,assetId)=>{original.push({context,assetId});return {}};
 const part={type:name==='non-media'?'text':'media',mediaType:'image',assetId:name==='uppercase'?id.toUpperCase():name==='short'?'abc':id};
 const parts=name==='duplicate'?[part,{...part}]:[part];
 try{
  const gate=runtime.hooks.find(h=>h.id==='media-scope');
  const gated=await gate.handler([{parts}],{scene,mode:'real'});
  runtime.isRecalled=()=>name==='recalled';
  runtime.emit({type:'message.sent',scene,turnId:'turn',segment:{parts},result:{status:'sent',...(name==='no-receipt'?{}:{messageId:'sent'})}});
  const before=calls.length;
  await Promise.resolve();await Promise.resolve();await Promise.resolve();
  cases.push({name,priority:gate.priority,action:gated.action,before,original,calls});
 }finally{await engine.dispose();store.close()}
}
const sourceHashes=Object.fromEntries(['src/engine.ts','src/runtime.ts','src/memes.ts'].map(f=>[f,createHash('sha256').update(readFileSync(resolve(root,f))).digest('hex')]));
const output=JSON.stringify({sourceHashes,cases},null,2)+'\n',target=new URL('./meme-outgoing.json',import.meta.url);
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==output)throw Error('outgoing_fixture_stale')}else writeFileSync(target,output);
