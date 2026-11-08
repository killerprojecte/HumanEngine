import {resolve,join} from 'node:path'
import {pathToFileURL} from 'node:url'
import {readFileSync} from 'node:fs'
const root=resolve(process.env.HUMANENGINE_SOURCE_ROOT||'../../../..')
const {usageBucket}=await import(pathToFileURL(join(root,'src/usage-time.ts')).href)
const inputs=[['2026-11-01T05:45:12.123Z','America/New_York'],['2026-11-01T06:45:12.123Z','America/New_York'],['2026-04-04T15:05:00.001Z','Australia/Lord_Howe'],['2026-10-03T15:35:00.001Z','Australia/Lord_Howe'],['2026-11-01T03:13:02.123Z','+05:45'],['2026-11-01T03:13:02.123Z','UTC']]
const result=inputs.map(([at,zone])=>({at,zone,day:usageBucket(at,zone,'day'),hour:usageBucket(at,zone,'hour')}))
if(process.argv.includes('--check')){if(JSON.stringify(result)!==JSON.stringify(JSON.parse(readFileSync(new URL('./usage.json',import.meta.url),'utf8'))))throw Error('usage source drift');console.log('Actual-source usage oracle: 6 cases matched')}else console.log(JSON.stringify(result,null,2))
