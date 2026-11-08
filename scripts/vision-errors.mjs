import {createHash} from 'node:crypto';
import {readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {pathToFileURL,fileURLToPath} from 'node:url';
const root=process.env.VISION_SOURCE_ROOT??fileURLToPath(new URL('../',import.meta.url));
const source=resolve(root,'src/vision-context.ts');
const sha=createHash('sha256').update(readFileSync(source)).digest('hex');
if(sha!=='b3f016006bd2633123eeb330e13dc4048f0af610872dea27b762ea11fa5b2002') throw Error('source_changed');
const {describeCurrentImages,ImageDescriptionCache}=await import(pathToFileURL(source).href);
const cases=[];
for(const [port,at,readFailure=false] of [['source',1],['source',2],['source',3],['source',4],['recalled',1],['recalled',2],['recalled',3],['recalled',4],['lookup',1],['canRecognize',1],['source',3,true],['recalled',3,true]]){
 const counts={source:0,recalled:0,lookup:0,canRecognize:0,read:0};
 const call=(name,value)=>{counts[name]++;if(name===port&&counts[name]===at)throw Error('synthetic_owner_failure');return value;};
 const message={id:'m',sceneId:'s',status:'available',sentAt:'2026-01-01T00:00:00.000Z',parts:[{type:'media',mediaType:'image',assetId:'a'}]};
 const context={scene:{id:'s'},mode:'real',turnId:'t',signal:new AbortController().signal,dependencies:new Set(),validations:[]};
 const store={models:{routes:[]},settings:{timezone:'UTC'},recalled:()=>call('recalled',false),log:()=>{}};
 const runtime={imageSourceAvailable:()=>call('source',true),readImage:async()=>{counts.read++;if(readFailure)throw Error('public_http_403');return Buffer.from('synthetic')}};
 const library={lookup:()=>call('lookup',undefined),canRecognize:()=>call('canRecognize',true),recognize:async()=>''};
 let result,error;
 try{result=await describeCurrentImages(store,runtime,()=>{throw Error('unexpected_model')},context,[message],new ImageDescriptionCache(),library);}catch(e){error=e.message;}
 cases.push({port,at,readFailure,counts,...(error?{error}:{result})});
}
const text=JSON.stringify({sourceSHA256:sha,cases},null,2)+'\n';
const target=new URL('../backend/internal/visioncontext/testdata/errors.json',import.meta.url);
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==text)throw Error('errors_fixture_stale')}else writeFileSync(target,text);
