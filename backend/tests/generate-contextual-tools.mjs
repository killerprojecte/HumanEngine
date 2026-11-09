import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import {pathToFileURL} from 'node:url'
const root=process.env.HE_SOURCE_ROOT;if(!root)throw Error('HE_SOURCE_ROOT required')
const contracts=await import(pathToFileURL(path.join(root,'src/memory/learning/contracts.ts')))
const instructions=await import(pathToFileURL(path.join(root,'src/memory/learning/subject-input.ts')))
const engine=fs.readFileSync(path.join(root,'src/engine.ts'),'utf8')
const name=engine.indexOf("'learning.propose',")
const start=engine.lastIndexOf('definition(',name),end=engine.indexOf('...(settings.memoryExperienceEnabled',name)
const expression=engine.slice(start,end).trim().replace(/,$/,'')
const build=new Function('settings','definition','buildLearningCandidateSchema','relationExtractionInstructions','commitmentExtractionInstructions','knowledgeExtractionInstructions',`return ${expression}`)
const definitions=[]
for(let flags=0;flags<8;flags++){
 const settings={memoryGraphEnabled:!!(flags&1),memoryCommitmentsEnabled:!!(flags&2),memoryKnowledgeEnabled:!!(flags&4)}
 definitions.push(build(settings,contracts.definition,contracts.buildLearningCandidateSchema,instructions.relationExtractionInstructions,instructions.commitmentExtractionInstructions,instructions.knowledgeExtractionInstructions))
}
const prefixStart=engine.indexOf('const ctx = { sceneId: context.scene.id',engine.indexOf('execute: async (name, input, context)'))
const prefixEnd=engine.indexOf("if (name === 'experience.outcomes')",prefixStart)
const branchStart=engine.indexOf("if (name === 'learning.propose')",prefixEnd)
const branchEnd=engine.indexOf("return { error: 'Unknown tool' }",branchStart)
const AsyncFunction=Object.getPrototypeOf(async function(){}).constructor
const execute=new AsyncFunction('name','input','context',engine.slice(prefixStart,prefixEnd)+engine.slice(branchStart,branchEnd))
const outcomes=[]
for(const scenario of ['ordinary','semantic','identity','aborted','invalid','workspace','historical','commitment-history','timeline','current']){
 const trace=[];const guard=name=>{trace.push(name);return scenario===name?name:undefined}
 const owner={gateModelTool:()=>trace.push('gate'),semanticExposure:{captureGuard:()=>guard('semantic')},identityExposure:{captureGuard:()=>guard('identity')},workspaceExposure:{captureGuard:()=>guard('workspace')},historicalExposure:{captureGuard:()=>guard('historical')},commitmentHistoryExposure:{captureGuard:()=>guard('commitment-history')},commitmentTimelineExposure:{captureGuard:()=>guard('timeline')},memoryExposure:{readExposed:()=>{trace.push('memories');return []}},relationExposure:{readExposedEntities:()=>[],captureLearningGuard:()=>undefined},commitmentExposure:{readExposedRefs:()=>[],captureGuard:()=>guard('current')},modelRead:{captureGuard:()=>undefined},knowledgeExposure:{captureGuard:()=>undefined,readEntities:()=>[],readEdges:()=>[]},contextualLearning:{submit:(_context,input,...capabilities)=>{trace.push('submit');return {status:'queued',input,capabilities:capabilities.map(v=>v??null)}}}}
 const context={scene:{id:'s'},mode:'real',signal:{aborted:scenario==='aborted'},validations:[()=>{trace.push('validate');return scenario!=='invalid'}]}
 const result=await execute.call(owner,'learning.propose',{content:'synthetic'},context)
 outcomes.push({scenario,trace,result})
}
// Execute the actual source scene-change methods, not a hand-written cancellation policy.
const runtime=fs.readFileSync(path.join(root,'src/runtime.ts'),'utf8')
const methodBody=(source,start,end)=>source.slice(source.indexOf(start)+start.length,source.indexOf(end,source.indexOf(start))).trim().replace(/}$/, '')
const runtimeChanged=new Function('scene',methodBody(runtime,'sceneChanged(scene: Scene) {','  private normalizeBot'))
const engineChanged=new Function('scene',methodBody(engine,'sceneChanged(scene: Scene) {','  private cancelLearning'))
const cancelLearning=new Function('sceneId',methodBody(engine,'private cancelLearning(sceneId: string) {','  async dispose()'))
const sceneChanges=[]
for(const learningEnabled of [true,false])for(const participationEnabled of [true,false]){
 const trace=[];const owner={experienceRuntime:{learningPaused:id=>trace.push('experience:'+id)},runtime:{sceneChanged(scene){runtimeChanged.call({cancel:id=>trace.push('reply-cancel:'+id)},scene)}},semanticLearning:{configure:()=>trace.push('semantic-configure'),cancelScene:id=>trace.push('semantic-cancel:'+id)},learning:{cancelLearning:id=>trace.push('learning-cancel:'+id)},contextualLearning:{queue:{cancelScene:id=>trace.push('contextual-cancel:'+id)}},mediaTasks:new Map(),cancelLearning(sceneId){cancelLearning.call(this,sceneId)}}
 engineChanged.call(owner,{id:'s',learningEnabled,participationEnabled})
 if(trace.includes('reply-cancel:s')!==!participationEnabled||trace.includes('contextual-cancel:s')!==!learningEnabled)throw Error('source cancellation policy changed')
 sceneChanges.push({learningEnabled,participationEnabled,trace})
}
const result={sourceSHA256:crypto.createHash('sha256').update(engine).digest('hex'),definitions,outcomes,runtimeSHA256:crypto.createHash('sha256').update(runtime).digest('hex'),sceneChanges}
const encoded=JSON.stringify(result,null,2)+'\n',target=process.argv[2];if(!target)throw Error('target required')
if(process.argv.includes('--check')){if(fs.readFileSync(target,'utf8')!==encoded)throw Error('contextual source oracle drift')}else fs.writeFileSync(target,encoded)
