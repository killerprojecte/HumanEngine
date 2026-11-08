import {readFileSync,writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {pathToFileURL,fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
const root=resolve(process.env.HE_SOURCE_ROOT??fileURLToPath(new URL('../',import.meta.url)));
const {describeCurrentImages,ImageDescriptionCache}=await import(pathToFileURL(resolve(root,'src/vision-context.ts')));
const base=JSON.parse(readFileSync(resolve(root,'backend/internal/visioncontext/testdata/source.json')));
const bytes=Buffer.from(base.png,'base64');
const DateOriginal=Date;globalThis.Date=class extends DateOriginal{constructor(value){super(value??base.fixedTime)}static now(){return DateOriginal.parse(base.fixedTime)}};
const cases=[];
for(const name of ['read-route-off','read-credential','recognize-route-off','recognize-timezone','decode-change','complete-change','models-error-1','models-error-2','models-error-3','models-error-4','timezone-error']){
 const make=(configured=false,route=true)=>({providers:[{id:'p',credentialConfigured:configured}],routes:route?[{role:'vision',orderedModelIds:['m']}]:[]});
 let models=make(),timezone='UTC',modelReads=0,timezoneReads=0,reads=0,recognitions=0,calls=0;
 const cacheKeys=[],prompts=[],modelValues=[];
 const store={get models(){modelReads++;if(name===`models-error-${modelReads}`)throw Error('synthetic_models_read');modelValues.push(JSON.stringify(models));if(name==='decode-change'&&modelReads===4)queueMicrotask(()=>{models=make(true);timezone='Europe/Paris'});return models},get settings(){timezoneReads++;if(name==='timezone-error')throw Error('synthetic_timezone_read');return {timezone}},recalled:()=>false,log:()=>{}};
 const runtime={imageSourceAvailable:()=>true,readImage:async()=>{reads++;if(name==='read-route-off')models=make(false,false);if(name==='read-credential')models=make(true);return bytes}};
 const library={lookup:()=>undefined,canRecognize:()=>true,recognize:async()=>{recognitions++;if(name==='recognize-route-off')models=make(false,false);if(name==='recognize-timezone')timezone='Asia/Tokyo';return undefined}};
 const router=()=>({complete:async(role,messages)=>{calls++;prompts.push(JSON.parse(messages[1].content));if(name==='complete-change')models=make(true);return {text:'synthetic description'}}});
 const count=name==='complete-change'?2:1;
 const messages=Array.from({length:count},(_,i)=>({id:`m${i}`,sceneId:'s',status:'available',sentAt:'2026-01-01T00:00:00.000Z',parts:[{type:'media',mediaType:'image',assetId:`a${i}`}]}));
 const context={scene:{id:'s'},mode:'real',turnId:'t',signal:new AbortController().signal,dependencies:new Set(),validations:[]};
 const cache=new ImageDescriptionCache(),put=cache.put.bind(cache);cache.put=(...args)=>{cacheKeys.push(args[0]);put(...args)};
 let result,error;try{result=await describeCurrentImages(store,runtime,router,context,messages,cache,library)}catch(e){error=e.message}
 cases.push({name,modelReads,timezoneReads,reads,recognitions,calls,modelValues,cacheKeys,prompts,...(error?{error}:{result})});
}
const sourceHashes=Object.fromEntries(['src/vision-context.ts','src/media.ts','src/image-upload.ts'].map(f=>[f,createHash('sha256').update(readFileSync(resolve(root,f))).digest('hex')]));
const output=JSON.stringify({sourceHashes,png:base.png,fixedTime:base.fixedTime,cases},null,2)+'\n',target=new URL('../backend/internal/visioncontext/testdata/dynamic.json',import.meta.url);
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==output)throw Error('dynamic_fixture_stale')}else writeFileSync(target,output);
