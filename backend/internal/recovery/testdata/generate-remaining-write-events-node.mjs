// Run from repository root with Node's --experimental-transform-types.
// Actual production RecoveryAuthority.append is the oracle, not a reimplementation.
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryAuthority } from '../../../../src/memory/recovery/ledger.ts'
import { parseExternalCostRecoveryEvent } from '../../../../src/memory/recovery/external-cost-contract.ts'
import { parseExperienceTrialRecoveryEvent } from '../../../../src/memory/recovery/experience-trial-contract.ts'
import { parseSemanticRecoveryEvent } from '../../../../src/memory/recovery/semantic-contract.ts'
import { parseIdentityRecoveryEvent } from '../../../../src/memory/recovery/identity-contract.ts'
import { parseWorkingSetPinRecoveryEvent } from '../../../../src/memory/recovery/working-set-contract.ts'
import { parseCommitmentRecoveryEvent } from '../../../../src/memory/recovery/commitment-contract.ts'
const parse = {external_cost_receipt:parseExternalCostRecoveryEvent,experience_trial:parseExperienceTrialRecoveryEvent,semantic_mutation:parseSemanticRecoveryEvent,identity_mutation:parseIdentityRecoveryEvent,working_set_pin:parseWorkingSetPinRecoveryEvent,commitment_head:parseCommitmentRecoveryEvent,commitment_delete:parseCommitmentRecoveryEvent}

const check = process.argv[2] === '--check'
if (process.argv.length > (check ? 3 : 2)) throw new Error('unexpected arguments')
const fixture = JSON.parse(readFileSync(new URL('authority-domains-node.json', import.meta.url)))
const kinds = new Set(['external_cost_receipt','experience_trial','semantic_mutation','identity_mutation','working_set_pin','commitment_head','commitment_delete'])
const directory = mkdtempSync(join(tmpdir(), 'go-remaining-write-oracle-'))
const rows = [], rejected = []
let authority
try {
 authority = new RecoveryAuthority(join(directory,'authority'), fixture.instanceId)
 for (const entry of fixture.cases) {
  let input = JSON.parse(entry.raw)
  if (!kinds.has(input?.kind)) continue
  const before = authority.head()
  if (!entry.accepted) {
   let error
   try { authority.append(input,before) } catch(e) { error = e.message }
   if (!error || JSON.stringify(authority.head()) !== JSON.stringify(before)) throw new Error('invalid fixture accepted or changed authority: '+entry.label)
   rejected.push({label:entry.label,raw:entry.raw,error})
   continue
  }
  // Reverse every object to prove parser/schema order wins over input order.
  const reverse = v => Array.isArray(v) ? v.map(reverse) : v && typeof v==='object' ? Object.fromEntries(Object.entries(v).reverse().map(([k,x])=>[k,reverse(x)])) : v
  input = reverse(input)
  const after=authority.append(input,before)
  const payload=JSON.stringify(parse[input.kind](input))
  if(!authority.db.prepare('SELECT 1 FROM revocations WHERE payload=?').get(payload)) throw new Error('canonical payload not recorded')
  const repeated=authority.append(input,after)
  const forced=authority.append(input,repeated,true)
  rows.push({label:entry.label,raw:entry.raw,payload,before,after,repeated,forced})
 }
 const special = []
 for (const [kind,path] of [ ['external_cost_receipt',['receipt','receipt','amountNanoUsd']],['experience_trial',['expectedReceiptVersion']],['semantic_mutation',['previousVersion']],['identity_mutation',['expectedRevision']],['working_set_pin',['policy','selection','ref','deletionEpoch']],['commitment_head',['deletionEpoch']],['commitment_delete',['deletionEpoch']] ]) {
  // Use a known-valid original base, not an arbitrary accepted variant.
  const base = JSON.parse(fixture.cases.find(c=>c.label===kind+':base').raw)
  for(const [name,value] of [['NaN',NaN],['Infinity',Infinity],['-Infinity',-Infinity]]) {
   const input=structuredClone(base); let target=input
   for(const key of path.slice(0,-1)) target=target[key]
   target[path.at(-1)]=value
   let error
   try { authority.append(input,authority.head()) } catch(e) {error=e.message}
   if(!error) throw new Error(kind+' accepted '+name)
   special.push({kind,path,name,error})
  }
 }
 const sources={}
 for(const path of [...Object.keys(fixture.sources),'backend/internal/recovery/testdata/authority-domains-node.json','backend/internal/recovery/testdata/generate-remaining-write-events-node.mjs']) sources[path]=createHash('sha256').update(readFileSync(path)).digest('hex')
 const generated=JSON.stringify({node:process.version,sources,rows,rejected,special},null,2)+'\n'
 const file=new URL('remaining-write-events-node.json',import.meta.url)
 if(check) { if(readFileSync(file,'utf8')!==generated) throw new Error('remaining write oracle stale'); console.log(`Verified ${rows.length} writes and ${rejected.length} rejected inputs`) }
 else {writeFileSync(file,generated);console.log(`Generated ${rows.length} writes and ${rejected.length} rejected inputs`)}
} finally { authority?.close();rmSync(directory,{recursive:true,force:true}) }
