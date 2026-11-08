// Run from repository root with pinned Node v24.22.1. Reads real TS, synthetic data only.
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
if (process.version !== 'v24.22.1') throw new Error('source_node_version_mismatch');
const root=resolve(process.argv[2]),out=resolve(process.argv[3]);
const load=rel=>import(pathToFileURL(resolve(root,rel)));
const {createOutcome}=await load('src/memory/experience/outcomes.ts');
const {candidateFromOutcome}=await load('src/memory/experience/candidates.ts');
const {validateExperiencePolicy}=await load('src/memory/experience/validation.ts');
const {experienceBehaviorHoldouts,runExperienceBehaviorHoldouts}=await load('src/memory/experience/behavior-holdouts.ts');
const {deriveExplicitPolicyFeedback}=await load('src/memory/experience/feedback-adapter.ts');
const {experienceCandidateHash}=await load('src/memory/domain/experience.ts');
const files=['src/memory/domain/experience.ts',...['outcomes','candidates','policy-actions','operation-eligibility','validation','behavior-holdouts','feedback-adapter'].map(x=>`src/memory/experience/${x}.ts`)];
const provenance=Object.fromEntries(files.map(f=>[f,createHash('sha256').update(readFileSync(resolve(root,f))).digest('hex')]));
const ctx={sceneId:'synthetic-scene',simulation:false};const feedback={source:{messageId:'source',senderId:'person'},target:{messageId:'target'},code:'request_conclusion_first',observedAt:'2026-01-01T00:00:00Z'};
const candidates=[];
for(const code of ['request_conclusion_first','request_exact_person_lookup','request_verify_plan_qualifiers']){const input={...feedback,code};const outcome=createOutcome(ctx,input,'outcome-'+code);const candidate=candidateFromOutcome(outcome,'candidate-'+code,1767225600000);candidates.push({ctx,input,outcome,candidate,receipt:validateExperiencePolicy(candidate)});}
const base=candidates[0].candidate;const validation=[];
for(const [name,patch]of Object.entries({badHash:{hash:'bad'},emptySupport:{supportIds:[]},counterexample:{counterexampleIds:['negative']},badCondition:{condition:{kind:'plan_question'}},emptyPerson:{condition:{kind:'response_style',personId:''}},extraCondition:{condition:{kind:'response_style',personId:'person',extra:true}},longExpiry:{expiresAt:'2028-01-01T00:00:00Z'},duplicateRoots:{generationRoots:['x','x']},unicodeOrder:{generationRoots:['\ue000','😀','\ud800']}})){const candidate={...base,...patch};if(name!=='badHash')candidate.hash=experienceCandidateHash(candidate);validation.push({name,candidate,receipt:validateExperiencePolicy(candidate)});}
const source={sceneId:ctx.sceneId,id:'source',senderId:'person',origin:'live',status:'available',receivedAt:feedback.observedAt,replyToId:'target',parts:[]};const target={sceneId:ctx.sceneId,id:'target',senderId:'bot',origin:'live',status:'available',receivedAt:'2025-12-31T23:59:59Z',parts:[]};const feedbackCases=[];
for(const text of ['请先说结论',' 请，先给结论再给细节！ ','涉及我的问题请先查我的记忆','回答计划问题前请先核对条件','取消先说结论','这次先查我的记忆有帮助','这次先查我的记忆有帮助？','这次先核对计划条件不合适','谢谢','请先说结论\u0085','请先说结论\ufeff','请先说结论'.repeat(20)]){const message={...source,parts:[{type:'text',text}]};const selectedPolicy={id:'p',version:2};const appliedPolicy={policyRef:selectedPolicy,operation:text.includes('计划')?'plan_qualifiers_checked':'memory_person'};feedbackCases.push({message,target,selectedPolicy,appliedPolicy,result:deriveExplicitPolicyFeedback(ctx.sceneId,message,target,selectedPolicy,appliedPolicy)??null});}
writeFileSync(resolve(out,'domain-oracle.json'),JSON.stringify({node:process.version,provenance,candidates,validation,feedbackCases,behaviors:experienceBehaviorHoldouts,behaviorResults:runExperienceBehaviorHoldouts()},null,2)+'\n');
writeFileSync(resolve(out,'behavior-holdouts.json'),JSON.stringify(experienceBehaviorHoldouts)+'\n');
