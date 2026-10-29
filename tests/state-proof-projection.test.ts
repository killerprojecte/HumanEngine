import assert from 'node:assert/strict'
import test from 'node:test'
import { ContextProjection } from '../src/runtime/context-projection.ts'
test('state proof wire keeps dependency groups while omitting canonical proof keys and projecting identity arrays', () => {
  const scene = 'RAW_SCENE_CANARY_WITH_LONG_PRIVATE_ID'
  const person = 'RAW_PERSON_CANARY_WITH_LONG_PRIVATE_ID'
  const message = 'RAW_MESSAGE_CANARY_WITH_LONG_PRIVATE_ID'
  const parent = 'RAW_PARENT_CANARY_WITH_LONG_PRIVATE_ID'
  const projection = new ContextProjection(scene)
  const serialized = projection.stringify({
    participantIds: [person],
    requiredProposalGroups: [
      [
        {
          proofId: JSON.stringify([scene, person, message]),
          sources: [{ sceneId: scene, senderId: person, messageId: message }],
          parents: [parent],
          parentReferences: [{ id: parent, version: 2, deletionEpoch: 1 }],
        },
      ],
    ],
  })
  for (const canonical of [scene, person, message, parent])
    assert.equal(serialized.includes(canonical), false)
  const value = JSON.parse(serialized)
  const proof = value.requiredProposalGroups[0][0]
  assert.equal('proofId' in proof, false)
  assert.equal(proof.parents[0], proof.parentReferences[0].id)
  assert.equal(value.participantIds[0], proof.sources[0].senderId)
  assert.deepEqual(
    projection.restoreArguments({ participantIds: value.participantIds, parents: proof.parents }),
    { participantIds: [person], parents: [parent] },
  )
})
