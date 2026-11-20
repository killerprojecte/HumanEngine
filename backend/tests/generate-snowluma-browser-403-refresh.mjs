import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { readOneBotImage } from '../../src/plugins/builtin/snowluma/media.ts'
import { encode } from '../../src/plugins/builtin/snowluma/protocol.ts'

assert.equal(process.version,'v24.22.1')
const scene = { adapterId:'snowluma', accountId:'42', kind:'group', platformSceneId:'99',
  id:'ob:s:'+encode(['snowluma','42','group','99']) }
const file = 'https://fixture.invalid/stale'
const specs = [
  { name:'direct-success', fileId:'opaque', first:'' },
  { name:'expired-403-inline', fileId:'opaque', first:'public_http_403' },
  { name:'expired-403-chosen-public', fileId:'opaque', first:'public_http_403',
    reply:{url:'https://fixture.invalid/fresh',file:'base64://YQ=='} },
  { name:'returned-403-not-retried', fileId:'opaque', first:'public_http_403', second:'public_http_403',
    reply:{url:'https://fixture.invalid/still-expired',file:'base64://YQ=='} },
  { name:'missing-file-id', first:'public_http_403' },
  { name:'same-file-id', fileId:file, first:'public_http_403' },
  { name:'cancelled', fileId:'opaque', first:'public_http_403', cancel:true },
  { name:'404-is-scope-limit', fileId:'opaque', first:'public_http_404', goLimited:true },
  { name:'policy-is-scope-limit', fileId:'opaque', first:'media_address_not_public', goLimited:true },
  { name:'invalid-token-is-scope-limit', fileId:'/local/key', first:'public_http_403', goLimited:true },
]
const cases = []
for (const spec of specs) {
  const asset = 'ob:media:'+encode({sceneId:scene.id,type:'image',file,fileId:spec.fileId})
  const controller = new AbortController()
  const actions = [], urls = []
  let bytes = '', error = ''
  try {
    bytes = (await readOneBotImage(scene,asset,async (action,params)=>{
      actions.push({action,...params})
      return spec.reply ?? {file:'base64://Yg=='}
    },controller.signal,async url=>{
      urls.push(url)
      const failure = urls.length === 1 ? spec.first : spec.second
      if (spec.cancel) controller.abort()
      if (failure) throw new Error(failure)
      return Buffer.from(urls.length === 1 ? 'first bytes' : 'fresh bytes')
    })).toString()
  } catch (e) {
    error = e.message
  }
  cases.push({...spec,asset,actions,urls,bytes,error})
}
assert.deepEqual(cases[1].actions,[{action:'get_image',file:'opaque'}])
assert.equal(cases[1].bytes,'b')
assert.deepEqual(cases[3].actions,[{action:'get_image',file:'opaque'}])
assert.equal(cases[3].error,'public_http_403')
const fixture = new URL('../plugins/snowluma/host/runtime/testdata/browser-403-refresh-source.json',import.meta.url)
const output = JSON.stringify({scene,file,cases},null,2)+'\n'
if (process.argv.includes('--check')) assert.equal(readFileSync(fixture,'utf8'),output)
else writeFileSync(fixture,output)
console.log('PASS',cases.length,'actual SnowLuma direct-locator refresh cases')
