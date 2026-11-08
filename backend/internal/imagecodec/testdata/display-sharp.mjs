import {readFileSync,writeFileSync} from 'node:fs'
import {createRequire} from 'node:module'
import {pathToFileURL} from 'node:url'
import {resolve,join} from 'node:path'
import {createHash} from 'node:crypto'
const root=resolve(process.env.HE_SOURCE_ROOT??new URL('../../../..',import.meta.url).pathname),require=createRequire(join(root,'package.json')),sharp=require('sharp')
const {validateDisplayImage}=await import(pathToFileURL(join(root,'src/chat-media.ts')))
const previous=JSON.parse(readFileSync(new URL('meme-sharp.json',import.meta.url),'utf8'))
const inputs=previous.cases.map(c=>({name:c.name,bytes:Buffer.from(c.input,'base64')}))
inputs.push({name:'empty',bytes:Buffer.alloc(0)},{name:'invalid',bytes:Buffer.from('synthetic invalid image')})
for(const frames of [200,201])inputs.push({name:'frames-'+frames,bytes:await sharp(Buffer.alloc(2*2*frames*4,127),{raw:{width:2,height:2*frames,channels:4,pageHeight:2}}).gif({delay:Array(frames).fill(50)}).toBuffer()})
const jpeg=inputs.find(x=>x.name==='jpeg').bytes;inputs.push({name:'truncated-jpeg',bytes:jpeg.subarray(0,Math.floor(jpeg.length*.75))})
const cases=[];for(const {name,bytes} of inputs){try{cases.push({name,input:bytes.toString('base64'),mime:await validateDisplayImage(bytes),error:null})}catch(e){cases.push({name,input:bytes.toString('base64'),mime:null,error:e.message})}}
const output=new URL('display-sharp.json',import.meta.url),text=JSON.stringify({node:process.version,sharp:sharp.versions,sourceHash:createHash('sha256').update(readFileSync(join(root,'src/chat-media.ts'))).digest('hex'),cases},null,2)+'\n';if(process.argv.includes('--check')){if(readFileSync(output,'utf8')!==text)throw new Error('display_fixture_mismatch')}else writeFileSync(output,text);console.log(cases.length+' actual display Sharp cases')
