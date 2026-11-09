import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { prepareKnowledgeAssertion } from '../../src/memory/knowledge/admission.ts'
const declaration={registryVersion:1,predicate:'prefers_topic',subject:{kind:'self',personId:'p'},object:{kind:'existing',entityId:'e'},polarity:'positive',modality:'asserted',temporalScope:'current',conditions:[],sourceSpans:[{messageId:'source',partIndex:0,start:9,end:24,stance:'self'}]}
const reference={id:'backing',version:1,deletionEpoch:0}
const context={sceneId:'s',simulation:false,provenance:'contextual',exposedKnowledgeEntities:[{entityId:'e',backingRef:reference}],exposedKnowledgeEdges:[{id:'edge',version:1,backing:reference}]}
const candidate={content:'Knowledge synthetic ordinary preference.',kind:'fact',epistemicKind:'explicit',sources:[{sceneId:'s',messageId:'source',senderId:'p'}],subject:{kind:'self',sourceMessageId:'source'},knowledge:[declaration]}
const result=prepareKnowledgeAssertion(context,candidate)
assert(result.ok)
const hash=createHash('sha256').update(JSON.stringify([context.sceneId,candidate.sources,[declaration],context.exposedKnowledgeEntities,context.exposedKnowledgeEdges])).digest('hex')
assert.equal(result.candidate.content,`Model-extracted structured knowledge declaration; reference ${hash}. Not independently verified.`)
assert.equal(result.candidate.kind,'claim')
assert.equal(result.context.internalDeclaration,'knowledge_declaration')
assert.equal(result.context.provenance,'source-only')
assert.deepEqual(result.context.dependencyMemoryIds,[])
assert.deepEqual(result.candidate.sources,candidate.sources)
assert.notEqual(result.candidate.content,candidate.content)
console.log(JSON.stringify({source:'actual TS prepareKnowledgeAssertion, synthetic typed input',result},null,2))
