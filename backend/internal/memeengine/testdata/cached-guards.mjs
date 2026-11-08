import {readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {fileURLToPath,pathToFileURL} from 'node:url';
import {createHash} from 'node:crypto';
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../../../../',import.meta.url)));
const {Store}=await import(pathToFileURL(resolve(root,'src/store.ts')));
const {Engine}=await import(pathToFileURL(resolve(root,'src/engine.ts')));
const store=new Store(':memory:'), engine=new Engine(store), cases=[];
const scene={id:'s',kind:'group',adapterId:'a',accountId:'c',platformSceneId:'s',participationEnabled:false,learningEnabled:false,memoryReadEnabled:true,personalizationEnabled:true};
const make=()=>({scene,mode:'real',signal:new AbortController().signal,dependencies:new Set(),validations:[]});
try {
 store.saveScene(scene);
 const extension=engine.runtime.extensions.find(e=>e.tools?.some(t=>t.name==='memes.search'));
 if(!extension)throw Error('engine_extension_missing');
 const turn=make();
 cases.push({name:'before-read',guards:engine.runtime.captureToolResultGuards(turn).length});
 let visible=true;
 // Exercise the actual Engine search/capture hooks; only synthetic library
 // content/availability is instrumented, not the capture implementation.
 engine.memes.search=()=>[{id:'a'.repeat(64)}];
 engine.memes.original=()=>{if(!visible)throw Error('meme_not_available');return {}};
 await extension.execute('memes.search',{query:''},turn);
 const guards=engine.runtime.captureToolResultGuards(turn);
 cases.push({name:'after-search',guards:guards.length,ordinary:turn.validations.length,valid:guards.every(g=>g())});
 visible=false;
 cases.push({name:'meme-hidden',ordinary:turn.validations.every(g=>g()),cached:guards.every(g=>g())});
 store.saveScene({...scene,memoryReadEnabled:false});
 cases.push({name:'read-disabled',cached:guards.every(g=>g())});
 store.saveScene(scene);
 const fresh=make();engine.modelRead.begin(fresh);
 cases.push({name:'new-turn-after-reenable',old:guards.every(g=>g()),fresh:engine.runtime.captureToolResultGuards(fresh).every(g=>g())});
} finally {await engine.dispose();store.close()}
const hashes=Object.fromEntries(['src/engine.ts','src/plugins/manager.ts','src/memory/read/model-read-policy.ts'].map(p=>[p,createHash('sha256').update(readFileSync(resolve(root,p))).digest('hex')]));
const out=JSON.stringify({hashes,cases},null,2)+'\n',file=new URL('./cached-guards.json',import.meta.url);
if(process.argv.includes('--check')){if(readFileSync(file,'utf8')!==out)throw Error('cached_guards_fixture_stale')}else writeFileSync(file,out);
