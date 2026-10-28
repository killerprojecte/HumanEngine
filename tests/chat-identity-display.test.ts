import test from 'node:test'
import assert from 'node:assert/strict'
import { humanName, avatarSource, identityColor, mediaLabelKey } from '../app/utils/chatIdentity.ts'

test('conversation display never decodes opaque identity IDs or exposes numeric fallbacks', () => {
  assert.equal(humanName('ob:u:WyJzbm93bHVtYSIsIjM4MzY1NTQ2MzkiLCIzNDkxOTMyMDU5Il0'), '')
  assert.equal(humanName('ob:s:opaque'), '')
  assert.equal(humanName('3491932059'), '')
  assert.equal(humanName('  夜航开发组  '), '夜航开发组')
  assert.equal(humanName(undefined), '')
  assert.equal(humanName('林\u0000小雨'), '林小雨')
})
test('avatar rendering accepts safe images and rejects active content and protocol-relative URLs', () => {
  assert.equal(avatarSource('javascript:alert(1)'), undefined)
  assert.equal(avatarSource('data:image/svg+xml,<svg/>'), undefined)
  assert.equal(avatarSource('//example.com/tracker'), undefined)
  assert.equal(avatarSource('https://u:p@example.com/avatar'), undefined)
  assert.equal(
    avatarSource('https://q1.qlogo.cn/g?b=qq&nk=123&s=100'),
    'https://q1.qlogo.cn/g?b=qq&nk=123&s=100',
  )
  assert.equal(avatarSource('/fixture-avatar/person.svg'), '/fixture-avatar/person.svg')
  assert.equal(identityColor('canonical-one'), identityColor('canonical-one'))
})

test('media labels distinguish transport types without claiming visual recognition', () => {
  assert.equal(mediaLabelKey({ mediaType: 'image/unknown' }), 'cwMediaImage')
  assert.equal(mediaLabelKey({ mediaType: 'image/png' }), 'cwMediaImage')
  assert.equal(mediaLabelKey({ mediaType: 'audio/unknown' }), 'cwMediaAudio')
  assert.equal(mediaLabelKey({ mediaType: 'video/mp4' }), 'cwMediaVideo')
  assert.equal(mediaLabelKey({ mediaType: 'sticker' }), 'cwMediaSticker')
  assert.equal(
    mediaLabelKey({ description: 'Image unavailable: image has no usable media reference' }),
    'cwMediaUnavailable',
  )
  assert.equal(
    mediaLabelKey({
      description: 'Untrusted platform image metadata: {"kind":"mface","summary":"hello"}',
    }),
    'cwMediaSticker',
  )
  assert.equal(
    mediaLabelKey({
      description: 'Untrusted platform image metadata: {"kind":"image","summary":"sticker"}',
    }),
    'cwMediaImageMetadata',
  )
  assert.equal(
    mediaLabelKey({ description: 'Native QQ face metadata (untrusted): {"id":"1"}' }),
    'cwMediaFace',
  )
  assert.equal(
    mediaLabelKey({ description: 'Untrusted platform image metadata: not-json' }),
    'cwMediaUnknown',
  )
  assert.equal(mediaLabelKey({ description: 'arbitrary emoji instruction' }), 'cwMediaUnknown')
  assert.equal(mediaLabelKey({}), 'cwMediaUnknown')
})
