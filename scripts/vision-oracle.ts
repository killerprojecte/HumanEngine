// Actual pinned source oracle. Synthetic inputs and test-only ports; no production stubs.
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
const sourceRoot=process.env.VISION_SOURCE_ROOT ?? fileURLToPath(new URL('../',import.meta.url))
const sourcePath=resolve(sourceRoot,'src/vision-context.ts')
const sourceSHA256=createHash('sha256').update(readFileSync(sourcePath)).digest('hex')
if(sourceSHA256!=='b3f016006bd2633123eeb330e13dc4048f0af610872dea27b762ea11fa5b2002')throw new Error('vision_source_fingerprint_changed')
const { describeCurrentImages, ImageDescriptionCache }=await import(pathToFileURL(sourcePath).href)
const sharp=createRequire(pathToFileURL(resolve(sourceRoot,'package.json')))('sharp')
const OriginalDate=Date
const fixedTime='2026-01-02T03:04:05.678Z'
globalThis.Date=class extends OriginalDate{constructor(value?:any){super(value??fixedTime)}static now(){return OriginalDate.parse(fixedTime)}} as any
const models={routes:[{role:'vision',orderedModelIds:['synthetic']}]}
const names=['budget','priority','simulation','unavailable','no_route','library','empty','failure','repeat','content_cache','library_empty','utf16','unicode_mime','distinct_bytes','model_order','seeded_empty']
const png=await sharp({create:{width:2,height:2,channels:3,background:'#00f'}}).png().toBuffer()
const alternatePNG=await sharp({create:{width:2,height:2,channels:3,background:'#f00'}}).png().toBuffer()
const cases=[]
for(const name of names){
 const messages=Array.from({length:name==='budget'||name==='priority'?6:2},(_,i)=>({id:`m${i}`,sceneId:'s',status:'available',sentAt:'2026-01-01T00:00:00.000Z',parts:[{type:'media',mediaType:name==='unicode_mime'&&i===0?'İMAGE/png':i%2?'IMAGE/png':'image',assetId:`a${i}`}]}))
 const controller=new AbortController();const context:any={scene:{id:'s'},mode:name==='simulation'?'simulation':'real',turnId:'turn',signal:controller.signal,dependencies:new Set(),validations:[],...(name==='priority'?{imagePriorityIds:['m0','m2']}: {})}
 const store:any={models:name==='no_route'?{routes:[]}:name==='model_order'?{...models,providers:[]}:models,settings:{timezone:'UTC'},recalled:()=>false,log:()=>{}}
 let reads=0,calls=0,recognitions=0,systemPrompt='',userInstruction=''
 const prompts:any[]=[];const cacheKeys:string[]=[]
 const runtime:any={imageSourceAvailable:(_:any,id:string)=>!(name==='unavailable'&&id==='m0'),readImage:async(_:any,assetId:string)=>{reads++;if(['failure','unavailable'].includes(name))throw new Error(name==='failure'?'public_http_403':'image_payload_invalid');return name==='distinct_bytes'&&assetId==='a1'?alternatePNG:png}}
 const library:any=['library','library_empty','seeded_empty'].includes(name)?{lookup:()=>name==='library'?'synthetic observation':undefined,recognize:async()=>{recognitions++;return ''}}:undefined
 const router:any=()=>({complete:async(role:any,prompt:any,tools:any)=>{calls++;systemPrompt=prompt[0].content;userInstruction=JSON.parse(prompt[1].content).instruction;prompts.push({role,messages:prompt,tools});return {text:name==='empty'?'   ':name==='utf16'?'a'.repeat(3999)+'😀':' synthetic vision '}}})
 const cache=new ImageDescriptionCache();const originalPut=cache.put.bind(cache);cache.put=(...args:any[])=>{cacheKeys.push(args[0]);return originalPut(...args as [string,string,string,string])}
 if(name==='seeded_empty'){const key=createHash('sha256').update(JSON.stringify(['visual-expression-v2','s',createHash('sha256').update(png).digest('hex'),store.models])).digest('hex');originalPut(key,'s','seed','cached description')}
 const firstMessages=name==='content_cache'?messages.slice(0,1):messages
 const result=await describeCurrentImages(store,runtime,router,context,firstMessages as any,cache,library)
 let second:any
 if(name==='repeat')second=await describeCurrentImages(store,runtime,router,context,messages as any,cache,library)
 if(name==='content_cache')second=await describeCurrentImages(store,runtime,router,{...context,dependencies:new Set()} as any,messages.slice(1) as any,cache,library)
 if(name==='model_order'){store.models={providers:[],...models};second=await describeCurrentImages(store,runtime,router,context,messages as any,cache,library)}
 cases.push({name,messages,result,...(second?{second}:{}),reads,calls,recognitions,systemPrompt,userInstruction,prompts,cacheKeys,dependencies:[...context.dependencies],validations:context.validations.length})
}
const serialized=JSON.stringify({source:'src/vision-context.ts',sourceSHA256,node:process.version,fixedTime,png:png.toString('base64'),alternatePNG:alternatePNG.toString('base64'),cases},null,2)+'\n'
const target=new URL('../backend/internal/visioncontext/testdata/source.json',import.meta.url)
if(process.argv.includes('--check')){if(readFileSync(target,'utf8')!==serialized)throw new Error('vision_oracle_fixture_stale')}else writeFileSync(target,serialized)
